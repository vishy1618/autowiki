package store_test

import (
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/store"
)

// runSessionStoreTests runs the shared behavioural test suite against any
// SessionStore implementation. Call it from implementation-specific test
// files (e.g. TestMemStore, TestRocksDBStore).
func runSessionStoreTests(t *testing.T, s store.SessionStore) {
	t.Helper()

	t.Run("CreateAndGet", func(t *testing.T) {
		// Arrange
		sess := store.Session{
			Token:     "tok-create-get",
			Email:     "user@example.com",
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			ExpiresAt: time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second),
		}

		// Act
		if err := s.CreateSession(sess); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		got, err := s.GetSession(sess.Token)

		// Assert
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got == nil {
			t.Fatal("GetSession: want session, got nil")
		}
		if got.Email != sess.Email {
			t.Errorf("Email: want %q, got %q", sess.Email, got.Email)
		}
		if !got.ExpiresAt.Equal(sess.ExpiresAt) {
			t.Errorf("ExpiresAt: want %v, got %v", sess.ExpiresAt, got.ExpiresAt)
		}
	})

	t.Run("Get_NotFound", func(t *testing.T) {
		// Arrange — nothing stored for this token

		// Act
		got, err := s.GetSession("tok-does-not-exist")

		// Assert
		if err != nil {
			t.Fatalf("GetSession on missing token: want nil error, got %v", err)
		}
		if got != nil {
			t.Errorf("GetSession on missing token: want nil, got %+v", got)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		// Arrange
		sess := store.Session{
			Token:     "tok-delete",
			Email:     "user@example.com",
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
		}
		if err := s.CreateSession(sess); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// Act
		if err := s.DeleteSession(sess.Token); err != nil {
			t.Fatalf("DeleteSession: %v", err)
		}
		got, err := s.GetSession(sess.Token)

		// Assert
		if err != nil {
			t.Fatalf("GetSession after delete: %v", err)
		}
		if got != nil {
			t.Errorf("GetSession after delete: want nil, got %+v", got)
		}
	})

	t.Run("Get_Expired", func(t *testing.T) {
		// Arrange — session that expired in the past
		sess := store.Session{
			Token:     "tok-expired",
			Email:     "user@example.com",
			CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
			ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
		}
		if err := s.CreateSession(sess); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// Act
		got, err := s.GetSession(sess.Token)

		// Assert
		if err != nil {
			t.Fatalf("GetSession on expired session: want nil error, got %v", err)
		}
		if got != nil {
			t.Errorf("GetSession on expired session: want nil, got %+v", got)
		}
	})

	t.Run("Delete_NoOp", func(t *testing.T) {
		// Arrange — nothing stored for this token

		// Act + Assert — should not return an error
		if err := s.DeleteSession("tok-nonexistent"); err != nil {
			t.Errorf("DeleteSession on missing token: want nil error, got %v", err)
		}
	})
}
