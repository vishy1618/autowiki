package chatsessions

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/suvish/autowiki/internal/store"
)

// Handler handles GET /api/chat-sessions and GET /api/chat-sessions/{id}.
type Handler struct {
	store store.ChatStore
}

// NewHandler returns an http.Handler for the chat-sessions endpoints.
func NewHandler(cs store.ChatStore) http.Handler {
	h := &Handler{store: cs}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat-sessions", h.handleList)
	mux.HandleFunc("GET /api/chat-sessions/{id}", h.handleGet)
	return mux
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 3)
	offset := queryInt(r, "offset", 0)

	sessions, err := h.store.ListSessions(limit, offset)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"sessions": sessions})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("id"), "")

	msgs, err := h.store.ListMessages(id)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if len(msgs) == 0 {
		// Distinguish "session exists but empty" from "session not found" by
		// checking ListSessions — simpler: treat empty as not-found only when
		// the session has never existed.
		// For now: if msgs is empty AND no session with this ID exists → 404.
		sessions, _ := h.store.ListSessions(10000, 0)
		found := false
		for _, s := range sessions {
			if s.ID == id {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"messages": sanitiseMessages(msgs)})
}

// sanitiseMessages filters internal LLM scaffolding messages and normalises
// assistant content-block arrays down to plain text before sending to clients.
func sanitiseMessages(msgs []store.Message) []store.Message {
	out := make([]store.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "tool_result" || m.Role == "tool_use" {
			continue
		}
		if m.Role == "assistant" {
			m.Content = extractAssistantText(m.Content)
			if m.Content == "" {
				continue // tool-only assistant turn — nothing to display
			}
		}
		out = append(out, m)
	}
	return out
}

// extractAssistantText returns the concatenated text from a JSON content-block
// array, or the original string if it is not a content-block array.
// Returns "" when the array contains no text blocks (tool-only turn).
func extractAssistantText(content string) string {
	if len(content) == 0 || content[0] != '[' {
		return content
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		return content
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
