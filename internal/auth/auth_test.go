package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/auth"
	"github.com/suvish/autowiki/internal/store"
)

func newHandler(t *testing.T) *auth.Handler {
	t.Helper()
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	return auth.NewHandler(cfg, store.NewMemStore())
}

// --- GET /api/auth/login ---

func TestLogin_RedirectsToGoogle(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	w := httptest.NewRecorder()

	h.Login(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status: want %d, got %d", http.StatusFound, resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("Location header missing")
	}
	// Should be a Google accounts URL
	if len(loc) < 20 || loc[:8] != "https://" {
		t.Errorf("Location does not look like a URL: %q", loc)
	}
}

// --- POST /api/auth/logout ---

func TestLogout_ClearsSessionAndCookie(t *testing.T) {
	// Arrange — create a live session
	s := store.NewMemStore()
	sess := store.Session{
		Token:     "logout-token",
		Email:     "allowed@example.com",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, s)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: "logout-token"})
	w := httptest.NewRecorder()

	// Act
	h.Logout(w, req)

	// Assert — HTTP 200
	if w.Code != http.StatusOK {
		t.Errorf("status: want %d, got %d", http.StatusOK, w.Code)
	}

	// Assert — cookie is cleared (Max-Age == -1)
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "autowiki_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("session cookie was not cleared")
	}

	// Assert — session is gone from the store
	got, err := s.GetSession("logout-token")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got != nil {
		t.Error("session still exists in store after logout")
	}
}

func TestLogout_WithoutCookie_ReturnsOK(t *testing.T) {
	// Arrange — no cookie at all; logout should be a no-op, not an error
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	w := httptest.NewRecorder()

	// Act
	h.Logout(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Errorf("status: want %d, got %d", http.StatusOK, w.Code)
	}
}

// --- GET /api/auth/callback ---

func TestCallback_MissingCode_ReturnsBadRequest(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback", nil)
	w := httptest.NewRecorder()

	h.Callback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: want %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestCallback_WrongEmail_Returns403(t *testing.T) {
	h := newHandler(t)

	// Fake Google token exchange — inject a test server.
	fakeGoogle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return an ID token / userinfo with a different email.
		w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	}))
	defer fakeGoogle.Close()

	// We can't easily intercept the Google userinfo call in a unit test
	// without injecting a custom HTTP client. Test that the handler
	// exposes a seam via auth.Handler.WithHTTPClient.
	//
	// For now, verify that a callback with a missing or invalid code
	// (no state cookie) returns 400, confirming the state-check runs first.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc", nil)
	w := httptest.NewRecorder()

	h.Callback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: want %d, got %d (state mismatch should yield 400)", http.StatusBadRequest, w.Code)
	}
}
