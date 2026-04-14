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

func TestHandler_GetSession_IncludesVaultChangesOnAssistantMessage(t *testing.T) {
	cs := store.NewMemChatStore()
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	// Assistant message with text + save_to_vault tool_use block.
	content := `[{"type":"text","text":"Saved it."},{"type":"tool_use","id":"tu_1","name":"save_to_vault","input":{"pages":[{"path":"notes/go.md","content":"..."},{"path":"notes/concurrency.md","content":"..."}]}}]`
	for _, m := range []store.Message{
		{SessionID: session.ID, Role: "user", Content: "save this", CreatedAt: time.Now()},
		{SessionID: session.ID, Role: "assistant", Content: content, CreatedAt: time.Now()},
	} {
		if err := cs.AppendMessage(m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions/"+session.ID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Messages []struct {
			Role         string   `json:"role"`
			Content      string   `json:"content"`
			VaultChanges []string `json:"vault_changes"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(resp.Messages))
	}
	assistant := resp.Messages[1]
	if assistant.Content != "Saved it." {
		t.Errorf("expected plain text content, got %q", assistant.Content)
	}
	if len(assistant.VaultChanges) != 2 {
		t.Errorf("expected 2 vault_changes, got %d: %v", len(assistant.VaultChanges), assistant.VaultChanges)
	}
	if len(assistant.VaultChanges) == 2 {
		if assistant.VaultChanges[0] != "notes/go.md" || assistant.VaultChanges[1] != "notes/concurrency.md" {
			t.Errorf("unexpected vault_changes: %v", assistant.VaultChanges)
		}
	}
}

func TestHandler_GetSession_OmitsToolResultMessages(t *testing.T) {
	cs := store.NewMemChatStore()
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	for _, m := range []store.Message{
		{SessionID: session.ID, Role: "user", Content: "save something", CreatedAt: time.Now()},
		{SessionID: session.ID, Role: "assistant", Content: "sure", CreatedAt: time.Now()},
		{SessionID: session.ID, Role: "tool_result", Content: `{"tool_use_id":"x","content":"ok"}`, CreatedAt: time.Now()},
	} {
		if err := cs.AppendMessage(m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

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
	for _, m := range resp.Messages {
		if m.Role == "tool_result" {
			t.Errorf("tool_result message should be filtered out, got: %+v", m)
		}
	}
	if len(resp.Messages) != 2 {
		t.Errorf("expected 2 messages (user + assistant), got %d", len(resp.Messages))
	}
}

func TestHandler_GetSession_ExtractsTextFromAssistantToolUseContent(t *testing.T) {
	cs := store.NewMemChatStore()
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	// Assistant message whose content is a JSON content-block array (text + tool_use).
	toolUseContent := `[{"type":"text","text":"I will save that now."},{"type":"tool_use","id":"tu_1","name":"save_to_vault","input":{"pages":[]}}]`
	for _, m := range []store.Message{
		{SessionID: session.ID, Role: "user", Content: "save this", CreatedAt: time.Now()},
		{SessionID: session.ID, Role: "assistant", Content: toolUseContent, CreatedAt: time.Now()},
	} {
		if err := cs.AppendMessage(m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

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
	if len(resp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(resp.Messages))
	}
	assistantMsg := resp.Messages[1]
	if assistantMsg.Content != "I will save that now." {
		t.Errorf("expected plain text content, got: %q", assistantMsg.Content)
	}
}

func TestHandler_GetSession_OmitsToolOnlyAssistantMessages(t *testing.T) {
	cs := store.NewMemChatStore()
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	// Assistant message that is a pure tool_use block with no text — happens
	// when the model decides to call a tool without any preamble.
	toolOnlyContent := `[{"id":"toolu_01","input":{"query":"storage"},"name":"search_vault","type":"tool_use"}]`
	for _, m := range []store.Message{
		{SessionID: session.ID, Role: "user", Content: "find storage info", CreatedAt: time.Now()},
		{SessionID: session.ID, Role: "assistant", Content: toolOnlyContent, CreatedAt: time.Now()},
		{SessionID: session.ID, Role: "tool_result", Content: `{"tool_use_id":"toolu_01","content":"[]"}`, CreatedAt: time.Now()},
		{SessionID: session.ID, Role: "assistant", Content: "Here is what I found.", CreatedAt: time.Now()},
	} {
		if err := cs.AppendMessage(m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

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
	// Expect only user + final assistant text; tool-only assistant and tool_result filtered.
	if len(resp.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d: %+v", len(resp.Messages), resp.Messages)
	}
	if len(resp.Messages) == 2 && resp.Messages[1].Content != "Here is what I found." {
		t.Errorf("expected final assistant text, got: %q", resp.Messages[1].Content)
	}
}

func TestHandler_GetSession_AssistantPlainTextContentUnchanged(t *testing.T) {
	cs := store.NewMemChatStore()
	session := seedSession(t, cs, "hello")
	if err := cs.AppendMessage(store.Message{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   "world",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	h := newHandler(t, cs)
	req := httptest.NewRequest(http.MethodGet, "/api/chat-sessions/"+session.ID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var resp struct {
		Messages []store.Message `json:"messages"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(resp.Messages))
	}
	if resp.Messages[1].Content != "world" {
		t.Errorf("plain assistant content should be unchanged, got: %q", resp.Messages[1].Content)
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
