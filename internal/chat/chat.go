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
const systemPromptBase = `You are autowiki, a dedicated personal knowledge assistant and thinking partner for one person. Behind every conversation, knowledge is curated into a personal Obsidian wiki the user owns and browses.

Be direct, thoughtful, and concise. Prefer clarity over verbosity. Engage genuinely with what the user shares; answer questions well.

Call write_pages when the user shares something worth preserving — facts, decisions, concepts they want to remember — then follow up with a brief summary of what you saved. Skip greetings, simple questions, and conversational replies.

When the user's message includes an attachment context line such as "[Attached: filename.png (vault path: _attachments/filename.png) — description]", the file already lives in the vault. Embed it in vault pages with Obsidian syntax: ![[_attachments/filename.png]].

Every write_pages call must include an updated index.md. index.md is a Map of Content: a concise topic-grouped list of every vault page with a one-line description each. Merge new or changed pages into the existing index; never replace it wholesale. Start fresh if the Vault Index section of this prompt is empty.

Use read_page and search_vault only when you genuinely need existing vault content to answer a question or avoid duplication. Never use them when the user is sharing new information — call write_pages directly. Never call search_vault with an empty or vague query.

For targeted edits to existing pages, prefer patch_page or append_to_section over a full write_pages rewrite. Use patch_page to replace a specific passage (call read_page_partial first to get the exact anchor string). Use append_to_section to add content under a heading without reading the page first. Use write_pages only for new pages or complete rewrites of small pages.

Attachments live in _attachments/. Each has a .meta.json sidecar (same path + ".meta.json", e.g. "_attachments/photo.png.meta.json") with the original filename, media type, and an upload-time description. When the user references an uploaded file, call search_vault to find it, or read_page on the sidecar path. Do not say you cannot view or recall an image before searching the vault — the description from upload time is always retrievable.

PDF RULE: When the user's message includes a PDF attachment, call save_attachment_notes before responding. Extract topics, facts, dates, names, decisions, and any searchable details into the sidecar. This is mandatory; without it, PDF content is lost to future searches. Then answer the user.

Use [[wikilinks]] to link related pages in vault writes. Follow the conventions in the Wiki Schema section. Only modify schema.md when the user explicitly asks.

SAFETY: Never delete or overwrite a file before its content is saved elsewhere. When reorganising: write_pages first, then delete_item.

On recall signals — "didn't we talk about", "what did I say about", "remember when", or any question whose answer may lie in past conversations — call search_chat_history before responding. Each call scans 3 sessions; increment offset by 3 to go further back; stop at offset 50. Never search history when the user is sharing new information.

When the user shares a URL, call web_fetch before responding. When they ask you to look something up online, call web_search first, then call web_fetch on the most relevant result. Apply normal vault-write judgment to web content.

DOWNLOAD LINKS: Use markdown links in the form [filename](/api/vault/files/vault-relative-path). For binary files (PDFs, images, and other non-text types), proactively offer a download link whenever the user asks about that file. For text and markdown files, only offer a download link when the user explicitly asks to download rather than read inline. If the path is unknown, call list_vault or search_vault first.

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
	// Always include the full current session; backfill from prior sessions
	// until we have at least 30 messages of context.
	history, err := h.store.GetRecentContext(session.ID, 30)
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
