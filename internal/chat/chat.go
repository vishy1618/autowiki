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
	Stream(ctx context.Context, messages []store.Message, indexMD string, attachments []llm.Attachment) (io.ReadCloser, error)
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

// streamResult holds the outcome of scanning one LLM SSE stream.
type streamResult struct {
	assembled   string
	toolUseID   string
	toolUseName string
	toolJSON    string
	scanErr     error
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

	// Open the first LLM stream before setting SSE headers so we can still
	// return a proper 500 if the initial call fails.
	body, err := h.streamer.Stream(r.Context(), history, indexMD, pdfAttachments)
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
	// call) or save_to_vault, or we hit the retrieval cap.
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

		// Persist the assistant message (text + optional tool_use block).
		assistantContent := sr.assembled
		if sr.toolUseID != "" {
			assistantContent = buildAssistantContent(sr.assembled, sr.toolUseID, sr.toolUseName, sr.toolJSON)
		}
		_ = h.store.AppendMessage(store.Message{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   assistantContent,
		})

		// No tool call → final answer; we're done.
		if sr.toolUseID == "" {
			writeSSE(w, "done", fmt.Sprintf(`{"session_id":%q}`, session.ID))
			if canFlush {
				flusher.Flush()
			}
			return
		}

		// save_to_vault: execute writes, emit vault event, finish.
		if sr.toolUseName == "save_to_vault" {
			h.applyVaultWrites(w, session.ID, sr.toolUseID, sr.toolJSON, canFlush, flusher)
			writeSSE(w, "done", fmt.Sprintf(`{"session_id":%q}`, session.ID))
			if canFlush {
				flusher.Flush()
			}
			return
		}

		// Retrieval tool: enforce cap, then execute and loop.
		retrievalCount++
		if retrievalCount > maxRetrievalCalls {
			writeSSE(w, "error", `{"message":"exceeded maximum tool call limit"}`)
			if canFlush {
				flusher.Flush()
			}
			return
		}

		switch sr.toolUseName {
		case "read_page":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal([]byte(sr.toolJSON), &input)
			writeSSE(w, "status", fmt.Sprintf(`{"message":"Reading %s\u2026"}`, input.Path))
			if canFlush {
				flusher.Flush()
			}
			content, _ := h.vault.ReadFile(input.Path)
			h.storeToolResult(session.ID, sr.toolUseID, content, false)

		case "search_vault":
			var input struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal([]byte(sr.toolJSON), &input)
			writeSSE(w, "status", fmt.Sprintf(`{"message":"Searching for %s\u2026"}`, input.Query))
			if canFlush {
				flusher.Flush()
			}
			results, _ := h.vault.SearchPages(input.Query, 10)
			resultJSON, _ := json.Marshal(results)
			h.storeToolResult(session.ID, sr.toolUseID, string(resultJSON), false)

		default:
			// Unknown retrieval tool: store an error result and continue.
			h.storeToolResult(session.ID, sr.toolUseID, "unknown tool: "+sr.toolUseName, true)
		}

		// Re-fetch history (tool_result just appended) and open the next stream.
		history, err = h.store.ListMessages(session.ID)
		if err != nil {
			writeSSE(w, "error", `{"message":"store error"}`)
			if canFlush {
				flusher.Flush()
			}
			return
		}
		body, err = h.streamer.Stream(r.Context(), history, indexMD, pdfAttachments)
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
// accumulating any tool call input. It returns the assembled text, tool call
// identifiers, and the raw tool input JSON.
func (h *Handler) scanStream(body io.Reader, w io.Writer, canFlush bool, flusher http.Flusher) streamResult {
	var assembled strings.Builder
	var toolJSONBuf strings.Builder
	var toolUseID, toolUseName string
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
					toolUseID = payload.ContentBlock.ID
					toolUseName = payload.ContentBlock.Name
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
			inToolUseBlock = false
		}
	}

	return streamResult{
		assembled:   assembled.String(),
		toolUseID:   toolUseID,
		toolUseName: toolUseName,
		toolJSON:    toolJSONBuf.String(),
		scanErr:     scanner.Err(),
	}
}

// buildAssistantContent serialises a text + tool_use content-block array.
// Only includes a text block when text is non-empty (Anthropic rejects empty
// text blocks on subsequent turns).
func buildAssistantContent(text, toolUseID, toolUseName, toolJSON string) string {
	var toolInput json.RawMessage
	if err := json.Unmarshal([]byte(toolJSON), &toolInput); err != nil {
		toolInput = json.RawMessage("{}")
	}
	var blocks []any
	if text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	blocks = append(blocks, map[string]any{
		"type":  "tool_use",
		"id":    toolUseID,
		"name":  toolUseName,
		"input": toolInput,
	})
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
