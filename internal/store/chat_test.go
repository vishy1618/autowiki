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
}
