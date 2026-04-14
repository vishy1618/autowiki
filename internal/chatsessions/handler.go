package chatsessions

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// messageResponse is the API representation of a single message.
type messageResponse struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"session_id"`
	Role         string    `json:"role"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"created_at"`
	VaultChanges []string  `json:"vault_changes,omitempty"`
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
func sanitiseMessages(msgs []store.Message) []messageResponse {
	out := make([]messageResponse, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "tool_result" || m.Role == "tool_use" {
			continue
		}
		resp := messageResponse{
			ID:        m.ID,
			SessionID: m.SessionID,
			Role:      m.Role,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
		}
		if m.Role == "assistant" {
			text, vaultPaths := parseAssistantContent(m.Content)
			if text == "" && len(vaultPaths) == 0 {
				continue // pure retrieval tool turn — nothing to display
			}
			resp.Content = text
			resp.VaultChanges = vaultPaths
		}
		out = append(out, resp)
	}
	return out
}

// parseAssistantContent parses a stored assistant content value.
// If it is a JSON content-block array, it returns the concatenated text and
// any page paths written by a save_to_vault tool_use block.
// Returns ("", nil) for tool-only turns (no text block present).
func parseAssistantContent(content string) (string, []string) {
	if len(content) == 0 || content[0] != '[' {
		return content, nil
	}
	var blocks []struct {
		Type  string `json:"type"`
		Text  string `json:"text"`
		Name  string `json:"name"`
		Input struct {
			Pages []struct {
				Path string `json:"path"`
			} `json:"pages"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		return content, nil
	}
	var sb strings.Builder
	var vaultPaths []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "tool_use":
			if b.Name == "save_to_vault" {
				for _, p := range b.Input.Pages {
					vaultPaths = append(vaultPaths, p.Path)
				}
			}
		}
	}
	return sb.String(), vaultPaths
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
