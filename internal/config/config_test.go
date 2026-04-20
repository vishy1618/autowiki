package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suvish/autowiki/internal/config"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

const minimalConfig = `
vault_path: ${VAULT_PATH}
server_port: ${PORT}
anthropic_api_key: ${ANTHROPIC_API_KEY}
pebble_path: ${PEBBLE_PATH}
chat_model: haiku
dream_model: sonnet
base_url: ${BASE_URL}
auth:
  google_client_id: ${GOOGLE_CLIENT_ID}
  google_client_secret: ${GOOGLE_CLIENT_SECRET}
  allowed_email: ${ALLOWED_EMAIL}
  session_secret: ${SESSION_SECRET}
dream:
  enabled: true
  start_hour_ist: 1
  end_hour_ist: 5
`

func setMinimalEnv(t *testing.T) {
	t.Helper()
	vars := map[string]string{
		"VAULT_PATH":           "/tmp/vault",
		"PORT":                 "8080",
		"ANTHROPIC_API_KEY":    "sk-ant-test",
		"PEBBLE_PATH":          "/tmp/pebble",
		"GOOGLE_CLIENT_ID":     "gid",
		"GOOGLE_CLIENT_SECRET": "gsecret",
		"ALLOWED_EMAIL":        "test@example.com",
		"SESSION_SECRET":       "supersecret",
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func TestConfig_Load_ReadsBaseURLFromEnv(t *testing.T) {
	// Arrange
	setMinimalEnv(t)
	t.Setenv("BASE_URL", "https://wiki.example.com")
	path := writeConfig(t, minimalConfig)

	// Act
	cfg, err := config.Load(path)

	// Assert
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseURL != "https://wiki.example.com" {
		t.Errorf("expected BaseURL %q, got %q", "https://wiki.example.com", cfg.BaseURL)
	}
}

func TestConfig_Load_DefaultsBaseURLToLocalhost(t *testing.T) {
	// When BASE_URL is not set the server should fall back to localhost so
	// local development continues to work without extra config.
	setMinimalEnv(t)
	t.Setenv("BASE_URL", "") // explicitly unset
	t.Setenv("PORT", "9090")
	path := writeConfig(t, minimalConfig)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseURL != "http://localhost:9090" {
		t.Errorf("expected default BaseURL %q, got %q", "http://localhost:9090", cfg.BaseURL)
	}
}

func TestConfig_DriveSync_DefaultsApplied(t *testing.T) {
	// When drive_sync is absent from config, defaults must be populated so
	// drivesync.New() can rely on them without nil-checking every field.
	setMinimalEnv(t)
	path := writeConfig(t, minimalConfig)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DriveSync.VaultFolderName != "autowiki-vault" {
		t.Errorf("VaultFolderName: want %q, got %q", "autowiki-vault", cfg.DriveSync.VaultFolderName)
	}
	if cfg.DriveSync.PollIntervalSecs != 60 {
		t.Errorf("PollIntervalSecs: want 60, got %d", cfg.DriveSync.PollIntervalSecs)
	}
	if cfg.DriveSync.ConflictStrategy != "last_write_wins" {
		t.Errorf("ConflictStrategy: want %q, got %q", "last_write_wins", cfg.DriveSync.ConflictStrategy)
	}
}
