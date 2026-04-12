package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/auth"
	"github.com/suvish/autowiki/internal/store"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func storeWithSession(t *testing.T) (store.SessionStore, string) {
	t.Helper()
	s := store.NewMemStore()
	sess := store.Session{
		Token:     "valid-token",
		Email:     "allowed@example.com",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return s, sess.Token
}

func TestMiddleware_API_NoCookie_Returns401(t *testing.T) {
	s := store.NewMemStore()
	mw := auth.NewMiddleware(s)

	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	w := httptest.NewRecorder()

	mw.Require(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: want %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestMiddleware_API_InvalidToken_Returns401(t *testing.T) {
	s := store.NewMemStore()
	mw := auth.NewMiddleware(s)

	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: "bad-token"})
	w := httptest.NewRecorder()

	mw.Require(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: want %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestMiddleware_API_ValidToken_PassesThrough(t *testing.T) {
	s, token := storeWithSession(t)
	mw := auth.NewMiddleware(s)

	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: token})
	w := httptest.NewRecorder()

	mw.Require(okHandler()).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: want %d, got %d", http.StatusOK, w.Code)
	}
}
