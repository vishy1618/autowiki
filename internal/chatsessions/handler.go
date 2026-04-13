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
	json.NewEncoder(w).Encode(map[string]any{"messages": msgs})
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
