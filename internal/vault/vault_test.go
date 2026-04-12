package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/vault"
)

func newManager(t *testing.T) *vault.Manager {
	t.Helper()
	return vault.NewManager(t.TempDir())
}

// ReadFile

func writeFixture(t *testing.T, root, relPath, content string) {
	t.Helper()
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManager_ReadFile_ReturnEmptyStringWhenFileNotFound(t *testing.T) {
	m := newManager(t)

	got, err := m.ReadFile("missing.md")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("want empty string, got %q", got)
	}
}

func TestManager_ReadFile_ReturnsFileContents(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "notes/go.md", "# Go notes")

	got, err := m.ReadFile("notes/go.md")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "# Go notes" {
		t.Fatalf("want %q, got %q", "# Go notes", got)
	}
}

// WriteFile

func TestManager_WriteFile_CreatesFileWithContent(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)

	if err := m.WriteFile("deep/nested/page.md", "hello"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "deep/nested/page.md"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("want %q, got %q", "hello", string(got))
	}
}

func TestManager_WriteFile_OverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "page.md", "old content")

	if err := m.WriteFile("page.md", "new content"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "page.md"))
	if string(got) != "new content" {
		t.Fatalf("want %q, got %q", "new content", string(got))
	}
}

// AppendLog

func TestManager_AppendLog_CreatesLogMdWithEntry(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)

	if err := m.AppendLog("saved notes/go.md"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("log.md not created: %v", err)
	}
	if !strings.Contains(string(got), "saved notes/go.md") {
		t.Fatalf("entry not found in log: %q", string(got))
	}
}

func TestManager_AppendLog_AppendsMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)

	_ = m.AppendLog("first entry")
	_ = m.AppendLog("second entry")

	got, _ := os.ReadFile(filepath.Join(dir, "log.md"))
	if !strings.Contains(string(got), "first entry") || !strings.Contains(string(got), "second entry") {
		t.Fatalf("expected both entries in log, got: %q", string(got))
	}
}

// ReadIndex

func TestManager_ReadIndex_ReturnEmptyWhenMissing(t *testing.T) {
	m := newManager(t)

	got, err := m.ReadIndex()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestManager_ReadIndex_ReturnsIndexMdContents(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "index.md", "# Index")

	got, err := m.ReadIndex()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "# Index" {
		t.Fatalf("want %q, got %q", "# Index", got)
	}
}
