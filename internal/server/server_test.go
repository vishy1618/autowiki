package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func newTestServer(t *testing.T, baseURL string) http.Handler {
	t.Helper()
	cfg := &config.Config{
		ServerPort:      8080,
		BaseURL:         baseURL,
		AnthropicAPIKey: "key",
		VaultPath:       t.TempDir(),
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
	return server.New(cfg, sessions, chats, nil, vm, nil, false)
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
