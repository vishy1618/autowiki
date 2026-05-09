package server_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/server"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// stubStreamer satisfies chat.Streamer without doing anything.
type stubStreamer struct{}

func (s *stubStreamer) Stream(_ interface{}, _ interface{}, _ string, _ string, _ interface{}) (interface{}, error) {
	return nil, nil
}

type testEnv struct {
	handler   http.Handler
	sessions  store.SessionStore
	vaultPath string
}

func newTestEnv(t *testing.T, baseURL string) *testEnv {
	t.Helper()
	vaultDir := t.TempDir()
	cfg := &config.Config{
		ServerPort:      8080,
		BaseURL:         baseURL,
		AnthropicAPIKey: "key",
		VaultPath:       vaultDir,
		PebblePath:      t.TempDir(),
		Auth: config.AuthConfig{
			GoogleClientID:     "gid",
			GoogleClientSecret: "gsecret",
			AllowedEmail:       "test@example.com",
			SessionSecret:      "secret",
		},
	}
	db, err := store.OpenPebble(t.TempDir())
	if err != nil {
		t.Fatalf("OpenPebble: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	sessions := store.NewPebbleStore(db)
	chats := store.NewPebbleChatStore(db)
	vm := vault.NewManager(cfg.VaultPath)
	return &testEnv{
		handler:   server.New(cfg, sessions, chats, nil, vm, nil, nil, nil, nil, false),
		sessions:  sessions,
		vaultPath: vaultDir,
	}
}

func newTestServer(t *testing.T, baseURL string) http.Handler {
	t.Helper()
	return newTestEnv(t, baseURL).handler
}

// createSession writes a valid session to the store and returns the cookie value.
func createSession(t *testing.T, sessions store.SessionStore) string {
	t.Helper()
	tok := "test-token-" + t.Name()
	err := sessions.CreateSession(store.Session{
		Token:             tok,
		Email:             "test@example.com",
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(24 * time.Hour),
		AbsoluteExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return tok
}

// writeVaultFixture creates a file inside the vault directory.
func writeVaultFixture(t *testing.T, vaultPath, relPath, content string) {
	t.Helper()
	full := filepath.Join(vaultPath, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// GET /api/vault/files/*

func TestServer_VaultFile_UnauthenticatedReturns401(t *testing.T) {
	env := newTestEnv(t, "http://localhost")
	writeVaultFixture(t, env.vaultPath, "notes.md", "# Notes")

	req := httptest.NewRequest(http.MethodGet, "/api/vault/files/notes.md", nil)
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestServer_VaultFile_AuthenticatedExistingFileReturns200WithHeaders(t *testing.T) {
	env := newTestEnv(t, "http://localhost")
	writeVaultFixture(t, env.vaultPath, "notes.md", "# Notes")
	tok := createSession(t, env.sessions)

	req := httptest.NewRequest(http.MethodGet, "/api/vault/files/notes.md", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: tok})
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if disp := w.Header().Get("Content-Disposition"); !strings.Contains(disp, "notes.md") {
		t.Errorf("Content-Disposition missing filename, got %q", disp)
	}
}

func TestServer_VaultFile_MissingFileReturns404(t *testing.T) {
	env := newTestEnv(t, "http://localhost")
	tok := createSession(t, env.sessions)

	req := httptest.NewRequest(http.MethodGet, "/api/vault/files/nonexistent.md", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: tok})
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestServer_VaultFile_EmptyPathReturns400(t *testing.T) {
	env := newTestEnv(t, "http://localhost")
	tok := createSession(t, env.sessions)

	// Trailing slash only — no file path.
	req := httptest.NewRequest(http.MethodGet, "/api/vault/files/", nil)
	req.AddCookie(&http.Cookie{Name: "autowiki_session", Value: tok})
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty path, got %d", w.Code)
	}
}

func TestServer_Login_RedirectsToGoogleWithConfiguredBaseURL(t *testing.T) {
	// Arrange — server configured with a production base URL.
	h := newTestServer(t, "https://wiki.example.com")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — redirect goes to Google, and the redirect_uri in the OAuth URL
	// points to the configured base URL, not localhost.
	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "accounts.google.com") {
		t.Errorf("expected redirect to Google, got %q", location)
	}
	if !strings.Contains(location, "wiki.example.com") {
		t.Errorf("expected redirect_uri to contain configured base URL, got %q", location)
	}
	if strings.Contains(location, "localhost") {
		t.Errorf("redirect_uri must not fall back to localhost, got %q", location)
	}
}
