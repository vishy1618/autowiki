package vault_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// SaveAttachment

func TestManager_SaveAttachment_WritesFileUnderAttachmentsDir(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)

	path, err := m.SaveAttachment("photo.png", []byte("imgdata"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(path, "_attachments/") {
		t.Fatalf("expected path under _attachments/, got %q", path)
	}
	if !strings.HasSuffix(path, ".png") {
		t.Fatalf("expected .png extension, got %q", path)
	}
	full := filepath.Join(dir, path)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("file not found at %q: %v", full, err)
	}
	if string(data) != "imgdata" {
		t.Fatalf("want %q, got %q", "imgdata", string(data))
	}
}

func TestManager_SaveAttachment_GeneratesUniqueNamesForSameOriginal(t *testing.T) {
	m := newManager(t)

	path1, _ := m.SaveAttachment("note.png", []byte("a"))
	path2, _ := m.SaveAttachment("note.png", []byte("b"))

	if path1 == path2 {
		t.Fatalf("expected unique paths, both got %q", path1)
	}
}

func TestManager_SaveAttachment_PreservesOriginalStemInFilename(t *testing.T) {
	m := newManager(t)

	path, _ := m.SaveAttachment("my-diagram.png", []byte("x"))

	base := filepath.Base(path)
	if !strings.HasPrefix(base, "my-diagram-") {
		t.Fatalf("expected filename to start with original stem, got %q", base)
	}
}

// AttachmentMeta sidecar

func TestManager_WriteAndReadAttachmentMeta_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	path, _ := m.SaveAttachment("img.png", []byte("x"))

	meta := vault.AttachmentMeta{
		ID:           "abc123",
		OriginalName: "img.png",
		MediaType:    "image/png",
		Description:  "a red circle",
		UploadedAt:   time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC),
	}

	if err := m.WriteAttachmentMeta(path, meta); err != nil {
		t.Fatalf("WriteAttachmentMeta: %v", err)
	}

	got, err := m.ReadAttachmentMeta(path)
	if err != nil {
		t.Fatalf("ReadAttachmentMeta: %v", err)
	}
	if got.ID != meta.ID {
		t.Errorf("ID: want %q, got %q", meta.ID, got.ID)
	}
	if got.Description != meta.Description {
		t.Errorf("Description: want %q, got %q", meta.Description, got.Description)
	}
	if got.OriginalName != meta.OriginalName {
		t.Errorf("OriginalName: want %q, got %q", meta.OriginalName, got.OriginalName)
	}
}

func TestManager_ReadAttachmentData_ReturnsRawBytes(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	content := []byte("%PDF-1.4 fake content")
	path, err := m.SaveAttachment("doc.pdf", content)
	if err != nil {
		t.Fatalf("SaveAttachment: %v", err)
	}

	got, err := m.ReadAttachmentData(path)
	if err != nil {
		t.Fatalf("ReadAttachmentData: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("want %q, got %q", content, got)
	}
}

// SearchPages

func TestManager_SearchPages_ReturnsMatchingPagesWithSnippets(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "programming/go.md", "line1\nGo interfaces are cool\nline3")

	results, err := m.SearchPages("Go interfaces", 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Path != "programming/go.md" {
		t.Errorf("want path %q, got %q", "programming/go.md", results[0].Path)
	}
	if !strings.Contains(results[0].Snippet, "Go interfaces are cool") {
		t.Errorf("snippet does not contain matching line: %q", results[0].Snippet)
	}
}

func TestManager_SearchPages_IsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "notes.md", "Go Interfaces Are Cool")

	results, err := m.SearchPages("go interfaces", 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
}

func TestManager_SearchPages_RespectsMaxResults(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "a.md", "match here")
	writeFixture(t, dir, "b.md", "match here")
	writeFixture(t, dir, "c.md", "match here")

	results, err := m.SearchPages("match", 2)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
}

func TestManager_SearchPages_ReturnsEmptyWhenNoMatch(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "notes.md", "nothing relevant here")

	results, err := m.SearchPages("xyzzy", 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("want 0 results, got %d", len(results))
	}
}

func TestManager_ReadAttachmentMeta_SidecarStoredNextToFile(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	path, _ := m.SaveAttachment("img.png", []byte("x"))

	meta := vault.AttachmentMeta{ID: "x1", OriginalName: "img.png", MediaType: "image/png"}
	_ = m.WriteAttachmentMeta(path, meta)

	// Sidecar must be readable as JSON directly from disk.
	sidecarPath := filepath.Join(dir, path+".meta.json")
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("sidecar file not found at %q: %v", sidecarPath, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v", err)
	}
}
