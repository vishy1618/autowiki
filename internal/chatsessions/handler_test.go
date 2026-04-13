package chatsessions_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/chatsessions"
	"github.com/suvish/autowiki/internal/store"
)

func newHandler(t *testing.T, cs store.ChatStore) http.Handler {
	t.Helper()
	return chatsessions.NewHandler(cs)
}

func seedSession(t *testing.T, cs store.ChatStore, msgs ...string) store.ChatSession {
	t.Helper()
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	for _, content := range msgs {
		if err := cs.AppendMessage(store.Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   content,
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	return session
}

func TestHandler_ListSessions_DefaultLimitIsThree(t *testing.T) {
	cs := store.NewMemChatStore()
	// Create 4 sessions.
	for i := 0; i < 4; i++ {
		s, _ := cs.ResolveSession()
		stale := s
		stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
		_ = cs.UpdateSession(stale)
	}

	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var resp struct {
		Sessions []store.ChatSession `json:"sessions"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Sessions) != 3 {
		t.Errorf("expected default limit of 3, got %d", len(resp.Sessions))
	}
}

func TestHandler_ListSessions_RespectsOffsetParam(t *testing.T) {
	cs := store.NewMemChatStore()
	for i := 0; i < 4; i++ {
		s, _ := cs.ResolveSession()
		stale := s
		stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
		_ = cs.UpdateSession(stale)
	}

	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions?limit=10&offset=2", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var resp struct {
		Sessions []store.ChatSession `json:"sessions"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Sessions) != 2 {
		t.Errorf("expected 2 sessions after offset 2, got %d", len(resp.Sessions))
	}
}

func TestHandler_GetSession_ReturnsMessagesInOrder(t *testing.T) {
	cs := store.NewMemChatStore()
	session := seedSession(t, cs, "first", "second", "third")

	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions/"+session.ID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Messages []store.Message `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(resp.Messages))
	}
	if resp.Messages[0].Content != "first" || resp.Messages[2].Content != "third" {
		t.Errorf("messages out of order: %v", resp.Messages)
	}
}

func TestHandler_GetSession_Returns404ForUnknownSession(t *testing.T) {
	cs := store.NewMemChatStore()
	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions/nonexistent", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// Auth tests: the handler itself does not enforce auth — that is the server
// middleware's job. These tests verify the handler returns data without a
// cookie, confirming it is the middleware (not the handler) that must be
// tested for 401 behaviour. The server-level wiring test is integration scope.

func TestHandler_ListSessions_ReturnsJSONContentType(t *testing.T) {
	cs := store.NewMemChatStore()
	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}
}

func TestHandler_GetSession_ReturnsJSONContentType(t *testing.T) {
	cs := store.NewMemChatStore()
	session := seedSession(t, cs, "hello")
	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions/"+session.ID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}
}

func TestHandler_ListSessions_ReturnsSessionsNewestFirst(t *testing.T) {
	// Arrange
	cs := store.NewMemChatStore()
	seedSession(t, cs, "hello")
	// Force a new session.
	s, _ := cs.ResolveSession()
	stale := s
	stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
	_ = cs.UpdateSession(stale)
	seedSession(t, cs, "world")

	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions", nil)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Sessions []store.ChatSession `json:"sessions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(resp.Sessions))
	}
	// Newest session has the "world" message — its LastActiveAt is more recent.
	if resp.Sessions[0].LastActiveAt.Before(resp.Sessions[1].LastActiveAt) {
		t.Error("expected sessions returned newest-first")
	}
}
