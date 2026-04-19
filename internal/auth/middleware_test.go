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
		Token:             "valid-token",
		Email:             "allowed@example.com",
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(24 * time.Hour),
		AbsoluteExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
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

func TestMiddleware_Rotation_WhenInSecondHalf_IssuesNewCookie(t *testing.T) {
	// Arrange — session is 11 hours from expiry (inside the <12h threshold)
	s := store.NewMemStore()
	sess := store.Session{
		Token:             "rot-old",
		Email:             "user@example.com",
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(11 * time.Hour),
		AbsoluteExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	newTokens := []string{"new-token-1"}
	mw := auth.NewMiddlewareWithTokenGen(s, func() (string, error) { return newTokens[0], nil })

	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: "rot-old"})
	w := httptest.NewRecorder()

	// Act
	mw.Require(okHandler()).ServeHTTP(w, req)

	// Assert — request succeeded
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}

	// Assert — new cookie is set with the rotated token
	var newCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "autowiki_session" {
			newCookie = c
		}
	}
	if newCookie == nil {
		t.Fatal("Set-Cookie: want new autowiki_session cookie, got none")
	}
	if newCookie.Value != "new-token-1" {
		t.Errorf("cookie value: want %q, got %q", "new-token-1", newCookie.Value)
	}

	// Assert — new token is valid in the store
	got, err := s.GetSession("new-token-1")
	if err != nil || got == nil {
		t.Fatalf("GetSession new token: %v, %v", got, err)
	}

	// Assert — old token is still accessible during grace (GraceOnly=true)
	grace, err := s.GetSession("rot-old")
	if err != nil || grace == nil {
		t.Fatalf("GetSession old token during grace: %v, %v", grace, err)
	}
	if !grace.GraceOnly {
		t.Error("old token: want GraceOnly=true")
	}
}

func TestMiddleware_Rotation_WhenInFirstHalf_DoesNotRotate(t *testing.T) {
	// Arrange — session is 13 hours from expiry (first half, no rotation needed)
	s := store.NewMemStore()
	sess := store.Session{
		Token:             "no-rot-token",
		Email:             "user@example.com",
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(13 * time.Hour),
		AbsoluteExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mw := auth.NewMiddlewareWithTokenGen(s, func() (string, error) {
		t.Error("token generator called unexpectedly")
		return "", nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: "no-rot-token"})
	w := httptest.NewRecorder()

	// Act
	mw.Require(okHandler()).ServeHTTP(w, req)

	// Assert — request succeeded, no new cookie set
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "autowiki_session" {
			t.Errorf("Set-Cookie: want no rotation, got cookie %q", c.Value)
		}
	}
}

func TestMiddleware_Rotation_GraceToken_DoesNotRotateAgain(t *testing.T) {
	// Arrange — grace tombstone (GraceOnly=true, very short expiry)
	s := store.NewMemStore()
	sess := store.Session{
		Token:     "grace-token",
		Email:     "user@example.com",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(25 * time.Second), // short, but GraceOnly
		GraceOnly: true,
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	mw := auth.NewMiddlewareWithTokenGen(s, func() (string, error) {
		t.Error("token generator called on grace token")
		return "", nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: "grace-token"})
	w := httptest.NewRecorder()

	// Act
	mw.Require(okHandler()).ServeHTTP(w, req)

	// Assert — passes through without rotating
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "autowiki_session" {
			t.Errorf("Set-Cookie: want no rotation for grace token, got %q", c.Value)
		}
	}
}
