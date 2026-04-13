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

	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// Streamer is the subset of llm.Client used by the chat handler.
// Defined here so the handler can be tested with a stub.
type Streamer interface {
	Stream(ctx context.Context, messages []store.Message, indexMD string) (io.ReadCloser, error)
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

// handleChat processes a chat message: persists it, streams the LLM reply,
// then persists the assembled assistant message and performs any vault writes.
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

	// Resolve attachment context: load sidecar metadata for each referenced
	// attachment and prepend descriptions to the user message.
	attachmentIDs := r.Form["attachment_ids"]
	if len(attachmentIDs) > 0 {
		var contextLines []string
		for _, id := range attachmentIDs {
			meta, err := h.vault.ReadAttachmentMeta(id)
			if err != nil {
				continue // silently skip unknown/missing attachments
			}
			if meta.Description != "" {
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

	// Build conversation history to send to the LLM.
	history, err := h.store.ListMessages(session.ID)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}

	// Read vault index so the LLM has context on existing pages.
	indexMD, _ := h.vault.ReadIndex()

	// Open the SSE stream from the LLM.
	body, err := h.streamer.Stream(r.Context(), history, indexMD)
	if err != nil {
		slog.Error("LLM stream failed", "error", err, "session_id", session.ID)
		// Persist an empty assistant message so the conversation history
		// stays properly alternated (user→assistant→user…). Without this,
		// the next request would send two consecutive user messages and
		// Anthropic would reject it, making all subsequent chats fail too.
		_ = h.store.AppendMessage(store.Message{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   "",
		})
		http.Error(w, "llm error", http.StatusInternalServerError)
		return
	}
	defer body.Close()

	// Set SSE headers before writing the first byte.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)

	// Parse Anthropic SSE, forwarding text deltas and collecting tool input.
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
	if err := scanner.Err(); err != nil {
		writeSSE(w, "error", fmt.Sprintf(`{"message":%q}`, err.Error()))
		if canFlush {
			flusher.Flush()
		}
		return
	}

	// Persist the assistant reply. If a tool was called, store the full
	// content-block array (text + tool_use) so the history is complete and
	// Anthropic can match it with the tool_result on the next turn.
	assistantContent := assembled.String()
	if toolUseID != "" {
		// Parse the accumulated input JSON so we can embed it as an object.
		var toolInput json.RawMessage
		if err := json.Unmarshal([]byte(toolJSONBuf.String()), &toolInput); err != nil {
			toolInput = json.RawMessage("{}")
		}
		blocks, err := json.Marshal([]any{
			map[string]any{"type": "text", "text": assembled.String()},
			map[string]any{"type": "tool_use", "id": toolUseID, "name": toolUseName, "input": toolInput},
		})
		if err == nil {
			assistantContent = string(blocks)
		}
	}
	_ = h.store.AppendMessage(store.Message{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   assistantContent,
	})

	// If the LLM called save_to_vault, execute writes, emit vault event, and
	// store a tool_result so the LLM knows the outcome on the next turn.
	if toolJSONBuf.Len() > 0 {
		h.applyVaultWrites(w, session.ID, toolUseID, toolJSONBuf.String(), canFlush, flusher)
	}

	// Emit done event.
	writeSSE(w, "done", fmt.Sprintf(`{"session_id":%q}`, session.ID))
	if canFlush {
		flusher.Flush()
	}
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

	// Append a single log entry listing all changed paths.
	_ = h.vault.AppendLog(fmt.Sprintf("wrote %s", strings.Join(changed, ", ")))

	// Store tool_result so the LLM knows the outcome on the next turn.
	h.storeToolResult(sessionID, toolUseID, fmt.Sprintf("saved: %s", strings.Join(changed, ", ")), false)

	// Build vault SSE payload.
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
