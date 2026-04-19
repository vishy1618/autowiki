package chat

import (
	"context"
	"encoding/json"
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
	Stream(ctx context.Context, messages []store.Message, indexMD string, schemaContent string, attachments []llm.Attachment) (io.ReadCloser, error)
}

// Handler handles chat API requests.
type Handler struct {
	store    store.ChatStore
	streamer Streamer
	vault    *vault.Manager
}

// NewHandler returns an http.Handler for POST /api/chat.
func NewHandler(cs store.ChatStore, streamer Streamer, vm *vault.Manager) http.Handler {
	h := &Handler{store: cs, streamer: streamer, vault: vm}
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
	body, err := h.streamer.Stream(r.Context(), history, indexMD, schemaContent, pdfAttachments)
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

	flusher, canFlush := w.(http.Flusher)

	// Agentic loop: continue until the LLM produces a final answer (no tool
	// calls) or save_to_vault, or we hit the retrieval cap.
	for retrievalCount := 0; ; {
		sr := h.scanStream(body, w, canFlush, flusher)
		body.Close()

		if sr.scanErr != nil {
			writeSSE(w, "error", fmt.Sprintf(`{"message":%q}`, sr.scanErr.Error()))
			if canFlush {
				flusher.Flush()
			}
			return
		}

		// Persist the assistant message (text + all tool_use blocks if any).
		assistantContent := sr.assembled
		if len(sr.toolCalls) > 0 {
			assistantContent = buildAssistantContent(sr.assembled, sr.toolCalls)
		}
		_ = h.store.AppendMessage(store.Message{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   assistantContent,
		})

		// No tool calls → final answer; we're done.
		if len(sr.toolCalls) == 0 {
			writeSSE(w, "done", fmt.Sprintf(`{"session_id":%q}`, session.ID))
			if canFlush {
				flusher.Flush()
			}
			return
		}

		// Dispatch each tool call and collect results.
		hasSaveToVault := false
		for _, tc := range sr.toolCalls {
			switch tc.name {
			case "save_to_vault":
				hasSaveToVault = true
				h.applyVaultWrites(w, session.ID, tc.id, tc.json, canFlush, flusher)

			case "read_page":
				var input struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal([]byte(tc.json), &input)
				writeSSE(w, "status", fmt.Sprintf(`{"message":"Reading %s\u2026"}`, input.Path))
				if canFlush {
					flusher.Flush()
				}
				content, _ := h.vault.ReadFile(input.Path)
				h.storeToolResult(session.ID, tc.id, content, false)

			case "search_vault":
				var input struct {
					Query string `json:"query"`
				}
				_ = json.Unmarshal([]byte(tc.json), &input)
				writeSSE(w, "status", fmt.Sprintf(`{"message":"Searching for %s\u2026"}`, input.Query))
				if canFlush {
					flusher.Flush()
				}
				results, _ := h.vault.SearchPages(input.Query, 10)
				resultJSON, _ := json.Marshal(results)
				h.storeToolResult(session.ID, tc.id, string(resultJSON), false)

			case "list_vault":
				var input struct {
					Path      string `json:"path"`
					Recursive bool   `json:"recursive"`
				}
				_ = json.Unmarshal([]byte(tc.json), &input)
				writeSSE(w, "status", `{"message":"Listing vault\u2026"}`)
				if canFlush {
					flusher.Flush()
				}
				entries, _ := h.vault.ListVault(input.Path, input.Recursive)
				resultJSON, _ := json.Marshal(entries)
				h.storeToolResult(session.ID, tc.id, string(resultJSON), false)

			case "read_page_partial":
				var input struct {
					Path     string `json:"path"`
					MaxChars int    `json:"max_chars"`
				}
				_ = json.Unmarshal([]byte(tc.json), &input)
				writeSSE(w, "status", fmt.Sprintf(`{"message":"Reading %s\u2026"}`, input.Path))
				if canFlush {
					flusher.Flush()
				}
				content, _ := h.vault.ReadFilePartial(input.Path, input.MaxChars)
				h.storeToolResult(session.ID, tc.id, content, false)

			case "move_page":
				var input struct {
					From string `json:"from"`
					To   string `json:"to"`
				}
				if err := json.Unmarshal([]byte(tc.json), &input); err != nil {
					slog.Warn("move_page: bad tool JSON", "err", err, "json", tc.json)
				}
				writeSSE(w, "status", fmt.Sprintf(`{"message":"Moving %s\u2026"}`, input.From))
				if canFlush {
					flusher.Flush()
				}
				if err := h.vault.MoveFile(input.From, input.To); err != nil {
					h.storeToolResult(session.ID, tc.id, err.Error(), true)
				} else {
					_ = h.vault.AppendLog(fmt.Sprintf("moved %s → %s", input.From, input.To))
					h.storeToolResult(session.ID, tc.id, fmt.Sprintf("moved %s to %s", input.From, input.To), false)
					payload, _ := json.Marshal(map[string]any{"action": "moved", "from": input.From, "to": input.To})
					writeSSE(w, "vault", string(payload))
					if canFlush {
						flusher.Flush()
					}
				}

			case "save_attachment_notes":
			var input struct {
				Path  string `json:"path"`
				Notes string `json:"notes"`
			}
			if err := json.Unmarshal([]byte(tc.json), &input); err != nil {
				slog.Warn("save_attachment_notes: bad tool JSON", "err", err, "json", tc.json)
				h.storeToolResult(session.ID, tc.id, "invalid tool input: "+err.Error(), true)
				continue
			}
			if err := h.vault.UpdateAttachmentDescription(input.Path, input.Notes); err != nil {
				h.storeToolResult(session.ID, tc.id, err.Error(), true)
			} else {
				h.storeToolResult(session.ID, tc.id, "notes saved for "+input.Path, false)
			}

		case "delete_item":
				var input struct {
					Path      string `json:"path"`
					Recursive bool   `json:"recursive"`
				}
				if err := json.Unmarshal([]byte(tc.json), &input); err != nil {
					slog.Warn("delete_item: bad tool JSON", "err", err, "json", tc.json)
				}
				slog.Debug("delete_item: dispatching", "raw_json", tc.json, "path", input.Path, "recursive", input.Recursive)
				writeSSE(w, "status", fmt.Sprintf(`{"message":"Deleting %s\u2026"}`, input.Path))
				if canFlush {
					flusher.Flush()
				}
				if err := h.vault.DeleteItem(input.Path, input.Recursive); err != nil {
					slog.Debug("delete_item: vault error", "path", input.Path, "recursive", input.Recursive, "err", err)
					h.storeToolResult(session.ID, tc.id, err.Error(), true)
				} else {
					_ = h.vault.AppendLog(fmt.Sprintf("deleted %s", input.Path))
					h.storeToolResult(session.ID, tc.id, fmt.Sprintf("deleted %s", input.Path), false)
					payload, _ := json.Marshal(map[string]any{"action": "deleted", "path": input.Path})
					writeSSE(w, "vault", string(payload))
					if canFlush {
						flusher.Flush()
					}
				}

			case "search_chat_history":
				var input struct {
					Query  string `json:"query"`
					Offset int    `json:"offset"`
				}
				_ = json.Unmarshal([]byte(tc.json), &input)
				writeSSE(w, "status", `{"message":"Searching chat history\u2026"}`)
				if canFlush {
					flusher.Flush()
				}
				results, _ := h.store.SearchMessages(input.Query, input.Offset, 3)
				resultJSON, _ := json.Marshal(results)
				h.storeToolResult(session.ID, tc.id, string(resultJSON), false)

			default:
				h.storeToolResult(session.ID, tc.id, "unknown tool: "+tc.name, true)
			}
		}

		// save_to_vault ends the turn (no further LLM call needed).
		if hasSaveToVault {
			writeSSE(w, "done", fmt.Sprintf(`{"session_id":%q}`, session.ID))
			if canFlush {
				flusher.Flush()
			}
			return
		}

		// Enforce retrieval cap across all tool calls in this turn.
		retrievalCount += len(sr.toolCalls)
		if retrievalCount > maxRetrievalCalls {
			writeSSE(w, "error", `{"message":"exceeded maximum tool call limit"}`)
			if canFlush {
				flusher.Flush()
			}
			return
		}

		// Re-fetch history (tool_results just appended) and open the next stream.
		// PDF attachments are not re-sent on loop iterations 2+ — the LLM
		// already has the document in context from the first call.
		history, err = h.store.ListMessages(session.ID)
		if err != nil {
			writeSSE(w, "error", `{"message":"store error"}`)
			if canFlush {
				flusher.Flush()
			}
			return
		}
		body, err = h.streamer.Stream(r.Context(), history, indexMD, schemaContent, nil)
		if err != nil {
			slog.Error("LLM stream failed in agentic loop", "error", err, "session_id", session.ID)
			writeSSE(w, "error", fmt.Sprintf(`{"message":%q}`, err.Error()))
			if canFlush {
				flusher.Flush()
			}
			return
		}
	}
}

// isRateLimitError reports whether err is an Anthropic 429 response.
func isRateLimitError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "429")
}
