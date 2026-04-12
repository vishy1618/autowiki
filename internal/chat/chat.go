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
}

// NewHandler returns an http.Handler for POST /api/chat.
func NewHandler(cs store.ChatStore, streamer Streamer) http.Handler {
	h := &Handler{store: cs, streamer: streamer}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", h.handleChat)
	return mux
}

// handleChat processes a chat message: persists it, streams the LLM reply,
// then persists the assembled assistant message.
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

	// Open the SSE stream from the LLM.
	body, err := h.streamer.Stream(r.Context(), history, "")
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

	// Parse Anthropic SSE and forward text_delta events as our own SSE.
	var assembled strings.Builder
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

		if lastEvent == "content_block_delta" {
			text, err := extractTextDelta(raw)
			if err == nil && text != "" {
				assembled.WriteString(text)
				writeSSE(w, "delta", fmt.Sprintf(`{"text":%q}`, text))
				if canFlush {
					flusher.Flush()
				}
			}
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

	// Emit done event.
	writeSSE(w, "done", fmt.Sprintf(`{"session_id":%q}`, session.ID))
	if canFlush {
		flusher.Flush()
	}
}

// writeSSE writes a single SSE event to w.
func writeSSE(w io.Writer, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

// extractTextDelta parses the text out of an Anthropic content_block_delta
// data payload.
func extractTextDelta(raw string) (string, error) {
	var payload struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", err
	}
	if payload.Delta.Type != "text_delta" {
		return "", nil
	}
	return payload.Delta.Text, nil
}
