package store_test

import (
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/store"
)

// runChatStoreTests is a shared behavioural suite run against every ChatStore
// implementation. Pass a freshly-initialised, empty store each call.
func runChatStoreTests(t *testing.T, cs store.ChatStore) {
	t.Helper()

	t.Run("ResolveSession_WhenEmpty_CreatesNewSession", func(t *testing.T) {
		// Act
		session, err := cs.ResolveSession()

		// Assert
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if session.ID == "" {
			t.Error("expected non-empty session ID")
		}
		if session.CreatedAt.IsZero() {
			t.Error("expected non-zero CreatedAt")
		}
		if session.LastActiveAt.IsZero() {
			t.Error("expected non-zero LastActiveAt")
		}
	})

	t.Run("ResolveSession_WhenRecentSession_ReturnsSameSession", func(t *testing.T) {
		// Arrange — first call creates the session
		first, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}

		// Act — second call within 30 min returns the same session
		second, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Assert
		if second.ID != first.ID {
			t.Errorf("expected same session ID %q, got %q", first.ID, second.ID)
		}
	})

	t.Run("ResolveSession_BumpsLastActiveAt_SoWindowSlides", func(t *testing.T) {
		// Arrange — create a session with LastActiveAt set to 25 minutes ago.
		// Without bumping, a second ResolveSession 10 minutes later (35 min total)
		// would create a new session even though the gap between calls is only 10 min.
		first, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		aged := first
		aged.LastActiveAt = time.Now().Add(-25 * time.Minute)
		if err := cs.UpdateSession(aged); err != nil {
			t.Fatalf("setup UpdateSession: %v", err)
		}

		// Act — ResolveSession should return the same session AND update LastActiveAt.
		second, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Assert — same session, and LastActiveAt is now recent (within last second).
		if second.ID != first.ID {
			t.Errorf("expected same session ID %q, got %q", first.ID, second.ID)
		}
		if time.Since(second.LastActiveAt) > time.Second {
			t.Errorf("expected LastActiveAt to be bumped to now, got %v", second.LastActiveAt)
		}
	})

	t.Run("ResolveSession_WhenSessionStale_CreatesNewSession", func(t *testing.T) {
		// Arrange — create a session then mark it stale
		first, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		stale := first
		stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
		if err := cs.UpdateSession(stale); err != nil {
			t.Fatalf("setup UpdateSession: %v", err)
		}

		// Act
		second, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Assert
		if second.ID == first.ID {
			t.Error("expected a new session after stale timeout, got same ID")
		}
	})

	t.Run("AppendMessage_AndListMessages_RoundTrip", func(t *testing.T) {
		// Arrange
		session, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		msg := store.Message{
			SessionID: session.ID,
			Role:      "user",
			Content:   "hello",
		}

		// Act
		if err := cs.AppendMessage(msg); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
		msgs, err := cs.ListMessages(session.ID)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}

		// Assert
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		if msgs[0].Content != "hello" {
			t.Errorf("expected content %q, got %q", "hello", msgs[0].Content)
		}
		if msgs[0].ID == "" {
			t.Error("expected AppendMessage to assign a non-empty ID")
		}
		if msgs[0].CreatedAt.IsZero() {
			t.Error("expected AppendMessage to assign CreatedAt")
		}
	})

	t.Run("ListMessages_PreservesInsertionOrder", func(t *testing.T) {
		// Arrange
		session, err := cs.ResolveSession()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		for _, content := range []string{"first", "second", "third"} {
			if err := cs.AppendMessage(store.Message{
				SessionID: session.ID,
				Role:      "user",
				Content:   content,
			}); err != nil {
				t.Fatalf("AppendMessage %q: %v", content, err)
			}
		}

		// Act
		msgs, err := cs.ListMessages(session.ID)
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}

		// Assert — must come back in insertion order
		want := []string{"first", "second", "third"}
		if len(msgs) < len(want) {
			t.Fatalf("expected at least %d messages, got %d", len(want), len(msgs))
		}
		// Grab the last three (earlier subtests may have left messages in the session).
		tail := msgs[len(msgs)-3:]
		for i, w := range want {
			if tail[i].Content != w {
				t.Errorf("position %d: expected %q, got %q", i, w, tail[i].Content)
			}
		}
	})

	t.Run("ListSessions_WhenEmpty_ReturnsEmptySlice", func(t *testing.T) {
		cs := store.NewMemChatStore()

		sessions, err := cs.ListSessions(10, 0)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 0 {
			t.Errorf("expected 0 sessions, got %d", len(sessions))
		}
	})

	t.Run("ListSessions_ReturnsNewestFirst", func(t *testing.T) {
		cs := store.NewMemChatStore()
		// Create two sessions by forcing a timeout between them.
		first, _ := cs.ResolveSession()
		stale := first
		stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
		_ = cs.UpdateSession(stale)
		second, _ := cs.ResolveSession()

		sessions, err := cs.ListSessions(10, 0)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(sessions))
		}
		if sessions[0].ID != second.ID {
			t.Errorf("expected newest session first, got %q", sessions[0].ID)
		}
		if sessions[1].ID != first.ID {
			t.Errorf("expected oldest session second, got %q", sessions[1].ID)
		}
	})

	t.Run("ListSessions_RespectsLimit", func(t *testing.T) {
		cs := store.NewMemChatStore()
		// Create 3 sessions.
		for i := 0; i < 3; i++ {
			s, _ := cs.ResolveSession()
			stale := s
			stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
			_ = cs.UpdateSession(stale)
		}

		sessions, err := cs.ListSessions(2, 0)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 2 {
			t.Errorf("expected 2 sessions, got %d", len(sessions))
		}
	})

	t.Run("ListSessions_RespectsOffset", func(t *testing.T) {
		cs := store.NewMemChatStore()
		// Create 3 sessions; track their IDs newest→oldest.
		var ids []string
		for i := 0; i < 3; i++ {
			s, _ := cs.ResolveSession()
			ids = append([]string{s.ID}, ids...) // prepend = newest first
			stale := s
			stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
			_ = cs.UpdateSession(stale)
		}

		sessions, err := cs.ListSessions(10, 1)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 2 {
			t.Fatalf("expected 2 sessions after offset 1, got %d", len(sessions))
		}
		if sessions[0].ID != ids[1] {
			t.Errorf("expected second-newest session first after offset, got %q", sessions[0].ID)
		}
	})

	t.Run("ListMessages_WhenSessionUnknown_ReturnsEmpty", func(t *testing.T) {
		// Act
		msgs, err := cs.ListMessages("nonexistent-session")

		// Assert
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) != 0 {
			t.Errorf("expected 0 messages, got %d", len(msgs))
		}
	})

	t.Run("SearchMessages_WhenNoSessions_ReturnsEmpty", func(t *testing.T) {
		cs := store.NewMemChatStore()

		results, err := cs.SearchMessages("anything", 0, 3)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("SearchMessages_WhenNoMatch_ReturnsEmpty", func(t *testing.T) {
		cs := store.NewMemChatStore()
		sess, _ := cs.ResolveSession()
		_ = cs.AppendMessage(store.Message{SessionID: sess.ID, Role: "user", Content: "hello world"})

		results, err := cs.SearchMessages("zebra", 0, 3)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for non-matching query, got %d", len(results))
		}
	})

	t.Run("SearchMessages_MatchesUserMessages", func(t *testing.T) {
		cs := store.NewMemChatStore()
		sess, _ := cs.ResolveSession()
		_ = cs.AppendMessage(store.Message{SessionID: sess.ID, Role: "user", Content: "I love Go programming"})

		results, err := cs.SearchMessages("go programming", 0, 3)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Role != "user" {
			t.Errorf("role: want %q, got %q", "user", results[0].Role)
		}
	})

	t.Run("SearchMessages_MatchesAssistantMessages", func(t *testing.T) {
		cs := store.NewMemChatStore()
		sess, _ := cs.ResolveSession()
		_ = cs.AppendMessage(store.Message{SessionID: sess.ID, Role: "assistant", Content: "Go is a statically typed language"})

		results, err := cs.SearchMessages("statically typed", 0, 3)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Role != "assistant" {
			t.Errorf("role: want %q, got %q", "assistant", results[0].Role)
		}
	})

	t.Run("SearchMessages_SkipsToolResultMessages", func(t *testing.T) {
		cs := store.NewMemChatStore()
		sess, _ := cs.ResolveSession()
		_ = cs.AppendMessage(store.Message{SessionID: sess.ID, Role: "tool_result", Content: "tool result about gophers"})

		results, err := cs.SearchMessages("gophers", 0, 3)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected tool_result messages to be skipped, got %d results", len(results))
		}
	})

	t.Run("SearchMessages_SnippetsTrimmedTo300Chars", func(t *testing.T) {
		cs := store.NewMemChatStore()
		sess, _ := cs.ResolveSession()
		long := "needle " + string(make([]byte, 400)) // >300 chars total
		_ = cs.AppendMessage(store.Message{SessionID: sess.ID, Role: "user", Content: long})

		results, err := cs.SearchMessages("needle", 0, 3)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if len(results[0].Snippet) > 300 {
			t.Errorf("snippet length: want ≤300, got %d", len(results[0].Snippet))
		}
	})

	t.Run("SearchMessages_SessionOffset_SkipsNewestSessions", func(t *testing.T) {
		cs := store.NewMemChatStore()

		// Create 2 sessions: newer has "alpha", older has "beta"
		older, _ := cs.ResolveSession()
		_ = cs.AppendMessage(store.Message{SessionID: older.ID, Role: "user", Content: "beta content"})
		stale := older
		stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
		_ = cs.UpdateSession(stale)

		newer, _ := cs.ResolveSession()
		_ = cs.AppendMessage(store.Message{SessionID: newer.ID, Role: "user", Content: "alpha content"})

		// offset=1 skips the newest session — should only find "beta" not "alpha"
		results, err := cs.SearchMessages("content", 1, 3)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result at offset 1, got %d", len(results))
		}
		if results[0].Snippet != "beta content" {
			t.Errorf("snippet: want %q, got %q", "beta content", results[0].Snippet)
		}
	})
}
