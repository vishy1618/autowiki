package chat

import (
	"bufio"
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
		_ = h.store.AppendMessage(store.Message{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   "",
		})
		http.Error(w, "llm error", http.StatusInternalServerError)
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
				var saveInput vaultWriteInput
				if err := json.Unmarshal([]byte(tc.json), &saveInput); err == nil && len(saveInput.Pages) > 0 {
					paths := make([]string, len(saveInput.Pages))
					for i, p := range saveInput.Pages {
						paths[i] = p.Path
					}
					writeSSE(w, "status", fmt.Sprintf(`{"message":"Saving %s\u2026"}`, strings.Join(paths, ", ")))
					if canFlush {
						flusher.Flush()
					}
				}
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

// scanStream reads an Anthropic SSE body, forwarding text deltas to w and
// accumulating any tool call inputs. It returns the assembled text and all
// tool calls emitted in this stream (there may be more than one).
func (h *Handler) scanStream(body io.Reader, w io.Writer, canFlush bool, flusher http.Flusher) streamResult {
	var assembled strings.Builder
	var toolJSONBuf strings.Builder
	var currentID, currentName string
	var calls []toolCall
	inToolUseBlock := false

	scanner := bufio.NewScanner(body)
	var lastEvent string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			lastEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")

		switch lastEvent {
		case "content_block_start":
			var payload struct {
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(raw), &payload); err == nil {
				inToolUseBlock = payload.ContentBlock.Type == "tool_use"
				if inToolUseBlock {
					currentID = payload.ContentBlock.ID
					currentName = payload.ContentBlock.Name
					toolJSONBuf.Reset()
				}
			}

		case "content_block_delta":
			text, inputJSON, err := extractDelta(raw)
			if err == nil {
				if text != "" {
					assembled.WriteString(text)
					writeSSE(w, "delta", fmt.Sprintf(`{"text":%q}`, text))
					if canFlush {
						flusher.Flush()
					}
				}
				if inputJSON != "" && inToolUseBlock {
					toolJSONBuf.WriteString(inputJSON)
				}
			}

		case "content_block_stop":
			if inToolUseBlock {
				calls = append(calls, toolCall{
					id:   currentID,
					name: currentName,
					json: toolJSONBuf.String(),
				})
			}
			inToolUseBlock = false
		}
	}

	return streamResult{
		assembled: assembled.String(),
		toolCalls: calls,
		scanErr:   scanner.Err(),
	}
}

// buildAssistantContent serialises a text + tool_use content-block array.
// Only includes a text block when text is non-empty (Anthropic rejects empty
// text blocks on subsequent turns). Handles multiple tool calls.
func buildAssistantContent(text string, calls []toolCall) string {
	var blocks []any
	if text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, tc := range calls {
		var toolInput json.RawMessage
		if err := json.Unmarshal([]byte(tc.json), &toolInput); err != nil {
			toolInput = json.RawMessage("{}")
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    tc.id,
			"name":  tc.name,
			"input": toolInput,
		})
	}
	b, err := json.Marshal(blocks)
	if err != nil {
		return text
	}
	return string(b)
}

// vaultWriteInput is the parsed tool input for save_to_vault.
type vaultWriteInput struct {
	Pages []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"pages"`
}

// applyVaultWrites parses the tool JSON, writes each page, appends the log,
// emits a vault SSE event, and stores a tool_result message.
func (h *Handler) applyVaultWrites(w io.Writer, sessionID, toolUseID, toolJSON string, canFlush bool, flusher http.Flusher) {
	var input vaultWriteInput
	if err := json.Unmarshal([]byte(toolJSON), &input); err != nil {
		h.storeToolResult(sessionID, toolUseID, "invalid tool input", true)
		return
	}
	if len(input.Pages) == 0 {
		h.storeToolResult(sessionID, toolUseID, "no pages provided", true)
		return
	}

	var changed []string
	for _, page := range input.Pages {
		if err := h.vault.WriteFile(page.Path, page.Content); err == nil {
			changed = append(changed, page.Path)
		}
	}
	if len(changed) == 0 {
		h.storeToolResult(sessionID, toolUseID, "all page writes failed", true)
		return
	}

	_ = h.vault.AppendLog(fmt.Sprintf("wrote %s", strings.Join(changed, ", ")))
	h.storeToolResult(sessionID, toolUseID, fmt.Sprintf("saved: %s", strings.Join(changed, ", ")), false)

	type change struct {
		Path string `json:"path"`
	}
	changes := make([]change, len(changed))
	for i, p := range changed {
		changes[i] = change{Path: p}
	}
	payload, err := json.Marshal(map[string]any{"changes": changes})
	if err != nil {
		return
	}
	writeSSE(w, "vault", string(payload))
	if canFlush {
		flusher.Flush()
	}
}

// storeToolResult persists a tool_result message for the given tool_use_id.
func (h *Handler) storeToolResult(sessionID, toolUseID, content string, isError bool) {
	tr, err := json.Marshal(map[string]any{
		"tool_use_id": toolUseID,
		"content":     content,
		"is_error":    isError,
	})
	if err != nil {
		return
	}
	_ = h.store.AppendMessage(store.Message{
		SessionID: sessionID,
		Role:      "tool_result",
		Content:   string(tr),
	})
}

// writeSSE writes a single SSE event to w.
func writeSSE(w io.Writer, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

// extractDelta parses an Anthropic content_block_delta payload.
// Returns (textDelta, inputJSONDelta, error).
func extractDelta(raw string) (string, string, error) {
	var payload struct {
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", "", err
	}
	switch payload.Delta.Type {
	case "text_delta":
		return payload.Delta.Text, "", nil
	case "input_json_delta":
		return "", payload.Delta.PartialJSON, nil
	}
	return "", "", nil
}
