package chat

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// Streamer is the subset of llm.Client used by the chat handler.
// Defined here so the handler can be tested with a stub.
type Streamer interface {
	Stream(ctx context.Context, systemPrompt string, messages []store.Message, attachments []llm.Attachment) (io.ReadCloser, error)
}

// systemPromptBase is the fixed part of the system prompt sent with every chat request.
const systemPromptBase = `You are autowiki, a personal knowledge assistant. Your job is to help the user think, learn, and capture knowledge through natural conversation.

You are not a generic assistant — you are a dedicated thinking partner for one person. You know that behind the scenes, the knowledge you help surface is being curated into a personal Obsidian wiki that the user owns and can browse at any time.

Be direct, thoughtful, and concise. Prefer clarity over verbosity. When the user shares something they've learned, engage with it genuinely. When they ask a question, answer it well.

When the user shares information that is worth preserving — something they've learned, a decision they've made, a concept they want to remember — call the save_to_vault tool with the relevant pages. Use your judgment: greetings, simple questions, and conversational replies do not need vault writes.

When the user's message includes an attachment context line such as "[Attached: filename.png (vault path: _attachments/filename.png) — description]", the file already lives in the vault at that path. Embed it in vault pages using Obsidian syntax: ![[_attachments/filename.png]].

Whenever you call save_to_vault, always include an updated index.md as one of the pages. index.md is a Map of Content: a concise list of every page in the vault grouped by topic, with a one-line description of each. Merge any new or changed pages into the existing index rather than replacing it wholesale. If the Vault Index section of this prompt is empty, start a fresh index.md.

You have two retrieval tools — read_page and search_vault — to look up existing vault content. Use them only when you genuinely need vault content to answer a question or to avoid duplicating something already there. Do NOT use them when the user is sharing new information to capture (a fact, a document, a PDF): in that case you already have the content and should call save_to_vault directly. Never call search_vault with an empty or vague query.

Attachments (images, PDFs, and other files) are stored in _attachments/. Each attachment has a .meta.json sidecar at the same path with ".meta.json" appended (e.g. "_attachments/photo-20260416-abc123.png.meta.json"). The sidecar contains the original filename, media type, and a description generated when the file was uploaded. When the user references an uploaded file — by description, name, or topic — call search_vault to find it, or read_page on the sidecar path to fetch its details directly. Do not say you cannot view or recall an image before you have searched the vault for it; the description from upload time is always retrievable.

PDF RULE: Whenever the user's message includes a PDF attachment, you MUST call save_attachment_notes before responding. Extract the key content — topics, facts, dates, names, decisions, and any other details worth searching for later — and write them to the sidecar using save_attachment_notes. This is not optional: without it, the PDF content will be lost and future searches will not surface it. Call save_attachment_notes first, then answer the user's question.

When writing vault pages, use [[wikilinks]] to link to related pages wherever appropriate. Follow the conventions in the Wiki Schema section of this prompt. Only modify schema.md if the user explicitly asks you to update their wiki conventions.

SAFETY RULE: Never delete or overwrite a file unless its content has been confirmed saved to another location first. When reorganising the vault, always save_to_vault the updated content before calling delete_item on the original.

When the user signals recall — phrases like "didn't we talk about", "what did I say about", "remember when", or any question whose answer may lie in a past conversation — use search_chat_history before responding. Each call scans 3 sessions; call again with offset+3 to go further back. Stop after a reasonable number of calls without a relevant hit (do not search more than offset 50). Never search chat history when the user is sharing new information.

When the user shares a URL, call web_fetch on it before responding. When the user asks you to look something up online, call web_search first, then call web_fetch on the most relevant result. After reading web content, apply the same vault-write judgment as for any other information — save substantive articles, documentation, or research to the vault, but skip ephemeral or low-value pages.

Do not mention Claude, Anthropic, or any underlying model. You are autowiki.`

// buildSystemPrompt assembles the full system prompt from the base, optional
// schema, and optional vault index.
func buildSystemPrompt(schemaContent, indexMD string) string {
	system := systemPromptBase
	if schemaContent != "" {
		system += "\n\n## Wiki Schema\n\n" + schemaContent
	}
	if indexMD != "" {
		system += "\n\n## Vault Index\n\n" + indexMD
	}
	return system
}

// Handler handles chat API requests.
type Handler struct {
	store    store.ChatStore
	streamer Streamer
	vault    *vault.Manager
	runner   *AgenticRunner
}

// NewHandler returns an http.Handler for POST /api/chat.
func NewHandler(cs store.ChatStore, streamer Streamer, vm *vault.Manager) http.Handler {
	h := &Handler{
		store:    cs,
		streamer: streamer,
		vault:    vm,
		runner:   NewAgenticRunner(streamer, cs, vm),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", h.handleChat)
	return mux
}

// maxRetrievalCalls is the maximum number of read_page/search_vault tool calls
// permitted per request to prevent runaway agentic loops.
const maxRetrievalCalls = 15

// toolCall holds one tool_use block emitted by the LLM.
type toolCall struct {
	id   string
	name string
	json string
}

// streamResult holds the outcome of scanning one LLM SSE stream.
type streamResult struct {
	assembled string
	toolCalls []toolCall
	scanErr   error
}

// handleChat processes a chat message: persists it, runs the agentic loop
// (retrieval tools loop up to maxRetrievalCalls times), emits SSE events, and
// persists all messages including tool results.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	// Resolve attachment context.
	var pdfAttachments []llm.Attachment
	attachmentIDs := r.Form["attachment_ids"]
	if len(attachmentIDs) > 0 {
		var contextLines []string
		for _, id := range attachmentIDs {
			meta, err := h.vault.ReadAttachmentMeta(id)
			if err != nil {
				continue
			}
			if meta.MediaType == "application/pdf" {
				data, err := h.vault.ReadAttachmentData(id)
				if err != nil {
					slog.Error("chat: failed to read PDF attachment", "path", id, "error", err)
					continue
				}
				pdfAttachments = append(pdfAttachments, llm.Attachment{
					MediaType: meta.MediaType,
					Data:      data,
				})
				contextLines = append(contextLines,
					fmt.Sprintf("[Attached PDF: %s (vault path: %s)]", meta.OriginalName, id))
			} else if meta.Description != "" {
				contextLines = append(contextLines,
					fmt.Sprintf("[Attached: %s (vault path: %s) — %s]", meta.OriginalName, id, meta.Description))
			} else {
				contextLines = append(contextLines,
					fmt.Sprintf("[Attached: %s (vault path: %s)]", meta.OriginalName, id))
			}
		}
		if len(contextLines) > 0 {
			message = strings.Join(contextLines, "\n") + "\n\n" + message
		}
	}

	// Resolve or create the active session.
	session, err := h.store.ResolveSession()
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}

	// Persist the user message.
	if err := h.store.AppendMessage(store.Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   message,
	}); err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}

	// Build conversation history and read vault index.
	history, err := h.store.ListMessages(session.ID)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	indexMD, _ := h.vault.ReadIndex()
	schemaContent, err := h.vault.EnsureSchema()
	if err != nil {
		slog.Error("chat: failed to ensure schema.md", "error", err)
		schemaContent = ""
	}

	// Open the first LLM stream before setting SSE headers so we can still
	// return a proper 500 if the initial call fails.
	body, err := h.streamer.Stream(r.Context(), buildSystemPrompt(schemaContent, indexMD), history, pdfAttachments)
	if err != nil {
		slog.Error("LLM stream failed", "error", err, "session_id", session.ID)
		if isRateLimitError(err) {
			const rateLimitMsg = "I was unable to complete this request because the content was too large. I cannot retry this."
			_ = h.store.AppendMessage(store.Message{
				SessionID: session.ID,
				Role:      "assistant",
				Content:   rateLimitMsg,
			})
			http.Error(w, rateLimitMsg, http.StatusTooManyRequests)
			return
		}
		const genericErrMsg = "I encountered an error and could not respond. Please try again."
		_ = h.store.AppendMessage(store.Message{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   genericErrMsg,
		})
		http.Error(w, genericErrMsg, http.StatusInternalServerError)
		return
	}

	// Set SSE headers before writing the first byte.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	_ = h.runner.Run(r.Context(), session.ID, buildSystemPrompt(schemaContent, indexMD), body, w, maxRetrievalCalls)
}

// isRateLimitError reports whether err is an Anthropic 429 response.
func isRateLimitError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "429")
}
