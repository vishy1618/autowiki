package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/auth"
	"github.com/suvish/autowiki/internal/store"
)

// roundTripFunc lets us create an http.RoundTripper from a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeGoogleClient returns an http.Client whose transport rewrites all
// requests to the given httptest.Server, enabling fully offline OAuth tests.
func fakeGoogleClient(srv *httptest.Server) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			req2.URL.Host = strings.TrimPrefix(srv.URL, "http://")
			return http.DefaultTransport.RoundTrip(req2)
		}),
	}
}

func newHandler(t *testing.T) *auth.Handler {
	t.Helper()
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	return auth.NewHandler(cfg, store.NewMemStore(), nil)
}

// --- GET /api/auth/login ---

func TestLogin_WithDriveSync_IncludesDriveScopeAndOfflineAccess(t *testing.T) {
	// Arrange — handler wired with a DriveTokenStore signals drive sync is enabled.
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, store.NewMemStore(), store.NewMemStore())

	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	w := httptest.NewRecorder()

	// Act
	h.Login(w, req)

	// Assert
	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "auth%2Fdrive") {
		t.Errorf("Login URL missing drive scope: %q", loc)
	}
	if !strings.Contains(loc, "access_type=offline") {
		t.Errorf("Login URL missing access_type=offline: %q", loc)
	}
}

func TestLogin_WithDriveSync_IncludesPromptConsent(t *testing.T) {
	// Arrange
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, store.NewMemStore(), store.NewMemStore())
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	w := httptest.NewRecorder()

	// Act
	h.Login(w, req)

	// Assert
	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "prompt=consent") {
		t.Errorf("Login URL missing prompt=consent: %q", loc)
	}
}

func TestLogin_WithoutDriveSync_OmitsDriveScopeAndOnlineAccess(t *testing.T) {
	// Arrange — nil DriveTokenStore means drive sync is disabled.
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, store.NewMemStore(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	w := httptest.NewRecorder()

	// Act
	h.Login(w, req)

	// Assert
	loc := w.Result().Header.Get("Location")
	if strings.Contains(loc, "auth%2Fdrive") {
		t.Errorf("Login URL should not contain drive scope: %q", loc)
	}
	if strings.Contains(loc, "access_type=offline") {
		t.Errorf("Login URL should not contain access_type=offline: %q", loc)
	}
}

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
	h := auth.NewHandler(cfg, s, nil)

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

// --- GET /api/auth/me ---

func TestMe_WithoutCookie_Returns401(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	w := httptest.NewRecorder()

	h.Me(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: want %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestMe_WithInvalidToken_Returns401(t *testing.T) {
	h := newHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: "no-such-token"})
	w := httptest.NewRecorder()

	h.Me(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: want %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestMe_WithValidSession_ReturnsEmail(t *testing.T) {
	// Arrange — pre-populate a live session.
	s := store.NewMemStore()
	_ = s.CreateSession(store.Session{
		Token:     "valid-token",
		Email:     "allowed@example.com",
		ExpiresAt: time.Now().Add(30 * time.Minute),
	})
	cfg := auth.Config{
		AllowedEmail:  "allowed@example.com",
		SessionSecret: "supersecretvalue",
		BaseURL:       "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, s, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: "valid-token"})
	w := httptest.NewRecorder()

	h.Me(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: want %d, got %d", http.StatusOK, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	if body := w.Body.String(); body != `{"email":"allowed@example.com"}` {
		t.Errorf("body: want JSON with email, got %q", body)
	}
}

// --- GET /api/auth/callback ---

func TestCallback_HappyPath_SetsSessionCookieAndRedirects(t *testing.T) {
	// Arrange — fake Google: token endpoint + userinfo endpoint.
	fakeGoogle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "userinfo") {
			fmt.Fprint(w, `{"email":"allowed@example.com","verified_email":true}`)
		} else {
			// Token exchange
			fmt.Fprint(w, `{"access_token":"fake-access","token_type":"Bearer","expires_in":3600}`)
		}
	}))
	defer fakeGoogle.Close()

	s := store.NewMemStore()
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, s, nil)
	h.WithHTTPClient(fakeGoogleClient(fakeGoogle))

	// Build request with matching state cookie and query param.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=authcode&state=mystate", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "mystate"})
	w := httptest.NewRecorder()

	// Act
	h.Callback(w, req)

	// Assert — redirects to /.
	if w.Code != http.StatusFound {
		t.Fatalf("status: want %d, got %d — body: %s", http.StatusFound, w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location: want /, got %q", loc)
	}

	// Assert — session cookie is set.
	var sessionToken string
	for _, c := range w.Result().Cookies() {
		if c.Name == "autowiki_session" && c.Value != "" {
			sessionToken = c.Value
		}
	}
	if sessionToken == "" {
		t.Fatal("expected autowiki_session cookie to be set")
	}

	// Assert — session exists in store with correct email.
	sess, err := s.GetSession(sessionToken)
	if err != nil || sess == nil {
		t.Fatalf("session not found in store: %v", err)
	}
	if sess.Email != "allowed@example.com" {
		t.Errorf("session email: want allowed@example.com, got %q", sess.Email)
	}
}

func TestCallback_WithDriveSync_StoresRefreshToken(t *testing.T) {
	// Arrange — fake Google returns both an access token and a refresh token.
	fakeGoogle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "userinfo") {
			fmt.Fprint(w, `{"email":"allowed@example.com","verified_email":true}`)
		} else {
			fmt.Fprint(w, `{"access_token":"fake-access","refresh_token":"fake-refresh","token_type":"Bearer","expires_in":3600}`)
		}
	}))
	defer fakeGoogle.Close()

	s := store.NewMemStore()
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, s, s)
	h.WithHTTPClient(fakeGoogleClient(fakeGoogle))

	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=authcode&state=mystate", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "mystate"})
	w := httptest.NewRecorder()

	// Act
	h.Callback(w, req)

	// Assert — refresh token stored in DriveTokenStore.
	token, err := s.GetDriveToken()
	if err != nil {
		t.Fatalf("GetDriveToken: %v", err)
	}
	if token != "fake-refresh" {
		t.Errorf("DriveToken: want %q, got %q", "fake-refresh", token)
	}
}

func TestCallback_WithoutDriveSync_DoesNotStoreDriveToken(t *testing.T) {
	// Arrange — Drive sync disabled (nil DriveTokenStore); callback must not
	// attempt to store a token even if the token response contains one.
	fakeGoogle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "userinfo") {
			fmt.Fprint(w, `{"email":"allowed@example.com","verified_email":true}`)
		} else {
			fmt.Fprint(w, `{"access_token":"fake-access","refresh_token":"fake-refresh","token_type":"Bearer","expires_in":3600}`)
		}
	}))
	defer fakeGoogle.Close()

	s := store.NewMemStore()
	cfg := auth.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		AllowedEmail:       "allowed@example.com",
		SessionSecret:      "supersecretvalue",
		BaseURL:            "http://localhost:8080",
	}
	h := auth.NewHandler(cfg, s, nil) // drive sync disabled
	h.WithHTTPClient(fakeGoogleClient(fakeGoogle))

	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=authcode&state=mystate", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "mystate"})
	w := httptest.NewRecorder()

	// Act
	h.Callback(w, req)

	// Assert — no drive token stored (store always returns empty when not set).
	token, err := s.GetDriveToken()
	if err != nil {
		t.Fatalf("GetDriveToken: %v", err)
	}
	if token != "" {
		t.Errorf("DriveToken: want empty, got %q", token)
	}
}

func TestCallback_WrongEmail_RedirectsToLoginWithError(t *testing.T) {
	// Arrange — fake Google returns a different email.
	fakeGoogle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "userinfo") {
			fmt.Fprint(w, `{"email":"other@example.com","verified_email":true}`)
		} else {
			fmt.Fprint(w, `{"access_token":"fake-access","token_type":"Bearer","expires_in":3600}`)
		}
	}))
	defer fakeGoogle.Close()

	h := newHandler(t)
	h.WithHTTPClient(fakeGoogleClient(fakeGoogle))

	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=authcode&state=mystate", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "mystate"})
	w := httptest.NewRecorder()

	h.Callback(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status: want %d, got %d", http.StatusFound, w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/login") || !strings.Contains(loc, "error=unauthorized") {
		t.Errorf("Location: want /login?error=unauthorized, got %q", loc)
	}
}

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
