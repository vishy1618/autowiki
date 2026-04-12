package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			// Detect the start of a tool_use block.
			var payload struct {
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(raw), &payload); err == nil {
				inToolUseBlock = payload.ContentBlock.Type == "tool_use"
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

	// Persist the assembled assistant reply.
	_ = h.store.AppendMessage(store.Message{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   assembled.String(),
	})

	// If the LLM called save_to_vault, execute writes and emit vault event.
	if toolJSONBuf.Len() > 0 {
		h.applyVaultWrites(w, toolJSONBuf.String(), canFlush, flusher)
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
// and emits a vault SSE event.
func (h *Handler) applyVaultWrites(w io.Writer, toolJSON string, canFlush bool, flusher http.Flusher) {
	var input vaultWriteInput
	if err := json.Unmarshal([]byte(toolJSON), &input); err != nil {
		return
	}
	if len(input.Pages) == 0 {
		return
	}

	var changed []string
	for _, page := range input.Pages {
		if err := h.vault.WriteFile(page.Path, page.Content); err == nil {
			changed = append(changed, page.Path)
		}
	}
	if len(changed) == 0 {
		return
	}

	// Append a single log entry listing all changed paths.
	_ = h.vault.AppendLog(fmt.Sprintf("wrote %s", strings.Join(changed, ", ")))

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
