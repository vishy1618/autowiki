package vault_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// safePath / path-traversal protection

func TestManager_ReadFile_RejectsEscapingPath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"single parent", "../outside"},
		{"deep parent", "../../etc/passwd"},
		{"absolute outside", "/etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newManager(t)

			_, err := m.ReadFile(tt.path)

			if err == nil {
				t.Fatalf("expected error for escaping path %q, got nil", tt.path)
			}
		})
	}
}

func TestManager_WriteFile_RejectsEscapingPath(t *testing.T) {
	m := newManager(t)

	err := m.WriteFile("../../outside.md", "data")

	if err == nil {
		t.Fatal("expected error for escaping path, got nil")
	}
}


func TestManager_ReadFile_AcceptsValidRelativePaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"simple", "notes.md"},
		{"nested", "a/b/c.md"},
		{"redundant dot", "./notes.md"},
		{"non-escaping parent", "a/../notes.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			m := vault.NewManager(dir)
			writeFixture(t, dir, "notes.md", "content")

			_, err := m.ReadFile(tt.path)

			if err != nil {
				t.Fatalf("expected no error for %q, got %v", tt.path, err)
			}
		})
	}
}

// ListVault

func TestManager_ListVault_ListsFilesAtRoot(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "a.md", "content")
	writeFixture(t, dir, "b.md", "content")

	entries, err := m.ListVault("", false)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	paths := make(map[string]string)
	for _, e := range entries {
		paths[e.Path] = e.Type
	}
	if paths["a.md"] != "file" {
		t.Errorf("expected a.md as file, got %q", paths["a.md"])
	}
	if paths["b.md"] != "file" {
		t.Errorf("expected b.md as file, got %q", paths["b.md"])
	}
}

func TestManager_ListVault_RecursiveWalksSubtrees(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "a.md", "content")
	writeFixture(t, dir, "sub/b.md", "content")

	entries, err := m.ListVault("", true)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	paths := make(map[string]string)
	for _, e := range entries {
		paths[e.Path] = e.Type
	}
	if paths["a.md"] != "file" {
		t.Errorf("expected a.md as file")
	}
	if paths["sub/b.md"] != "file" {
		t.Errorf("expected sub/b.md as file")
	}
}

func TestManager_ListVault_NonRecursiveShowsDirEntry(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "sub/b.md", "content")

	entries, err := m.ListVault("", false)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	paths := make(map[string]string)
	for _, e := range entries {
		paths[e.Path] = e.Type
	}
	if paths["sub"] != "dir" {
		t.Errorf("expected sub as dir entry, got %q", paths["sub"])
	}
	if _, found := paths["sub/b.md"]; found {
		t.Error("non-recursive should not include nested file sub/b.md")
	}
}

func TestManager_ListVault_RejectsEscapingPath(t *testing.T) {
	m := newManager(t)

	_, err := m.ListVault("../../outside", false)

	if err == nil {
		t.Fatal("expected error for escaping path, got nil")
	}
}

// ReadFilePartial

func TestManager_ReadFilePartial_ReturnsAtMostMaxChars(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "big.md", "abcdefghij") // 10 bytes

	got, err := m.ReadFilePartial("big.md", 5)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "abcde" {
		t.Fatalf("want %q, got %q", "abcde", got)
	}
}

func TestManager_ReadFilePartial_ReturnsFullContentWhenSmallerThanLimit(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "small.md", "hi")

	got, err := m.ReadFilePartial("small.md", 1000)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hi" {
		t.Fatalf("want %q, got %q", "hi", got)
	}
}

func TestManager_ReadFilePartial_RejectsEscapingPath(t *testing.T) {
	m := newManager(t)

	_, err := m.ReadFilePartial("../../etc/passwd", 100)

	if err == nil {
		t.Fatal("expected error for escaping path, got nil")
	}
}

// MoveFile

func TestManager_MoveFile_MovesFileToNewPath(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "old.md", "content")

	err := m.MoveFile("old.md", "new/path/new.md")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// old should not exist
	if _, statErr := os.Stat(filepath.Join(dir, "old.md")); !os.IsNotExist(statErr) {
		t.Error("old.md should no longer exist")
	}
	// new should exist with same content
	data, readErr := os.ReadFile(filepath.Join(dir, "new/path/new.md"))
	if readErr != nil {
		t.Fatalf("new file not found: %v", readErr)
	}
	if string(data) != "content" {
		t.Errorf("want %q, got %q", "content", string(data))
	}
}

func TestManager_MoveFile_ErrorsWhenSourceDoesNotExist(t *testing.T) {
	m := newManager(t)

	err := m.MoveFile("nonexistent.md", "dest.md")

	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}

func TestManager_MoveFile_RejectsEscapingFromPath(t *testing.T) {
	m := newManager(t)

	err := m.MoveFile("../../outside.md", "dest.md")

	if err == nil {
		t.Fatal("expected error for escaping from path, got nil")
	}
}

func TestManager_MoveFile_RejectsEscapingToPath(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "src.md", "x")

	err := m.MoveFile("src.md", "../../outside.md")

	if err == nil {
		t.Fatal("expected error for escaping to path, got nil")
	}
}

// DeleteItem

func TestManager_DeleteItem_DeletesAFile(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "page.md", "content")

	err := m.DeleteItem("page.md", false)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "page.md")); !os.IsNotExist(statErr) {
		t.Error("page.md should no longer exist")
	}
}

func TestManager_DeleteItem_ErrorsOnNonEmptyDirWithoutRecursive(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "sub/page.md", "content")

	err := m.DeleteItem("sub", false)

	if err == nil {
		t.Fatal("expected error for non-empty dir without recursive, got nil")
	}
}

func TestManager_DeleteItem_RecursiveDeletesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "sub/page.md", "content")

	err := m.DeleteItem("sub", true)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(statErr) {
		t.Error("sub/ should no longer exist")
	}
}

func TestManager_DeleteItem_RejectsEscapingPath(t *testing.T) {
	m := newManager(t)

	err := m.DeleteItem("../../outside", false)

	if err == nil {
		t.Fatal("expected error for escaping path, got nil")
	}
}

func TestManager_DeleteItem_RejectsEmptyPath(t *testing.T) {
	m := newManager(t)

	err := m.DeleteItem("", false)

	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

// EnsureSchema

func TestManager_SearchPages_FindsMatchInAttachmentSidecar(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "_attachments/photo-20260416-abc123.png.meta.json",
		`{"id":"att1","original_name":"photo.png","media_type":"image/png","description":"a sunset over mountains","uploaded_at":"2026-04-16T00:00:00Z"}`)

	results, err := m.SearchPages("sunset", 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Path != "_attachments/photo-20260416-abc123.png" {
		t.Errorf("want attachment path, got %q", results[0].Path)
	}
	if !strings.Contains(results[0].Snippet, "sunset") {
		t.Errorf("want snippet containing match, got %q", results[0].Snippet)
	}
}

func TestManager_SearchPages_FindsAttachmentByOriginalName(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "_attachments/report-20260416-abc123.pdf.meta.json",
		`{"id":"att2","original_name":"q1-report.pdf","media_type":"application/pdf","description":"Q1 financial summary","uploaded_at":"2026-04-16T00:00:00Z"}`)

	results, err := m.SearchPages("q1-report", 10)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Path != "_attachments/report-20260416-abc123.pdf" {
		t.Errorf("want attachment path, got %q", results[0].Path)
	}
}

func TestManager_SearchPages_SidecarAndMdResultsRespectMaxResults(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	writeFixture(t, dir, "notes.md", "go programming notes")
	writeFixture(t, dir, "_attachments/go-20260416-abc.png.meta.json",
		`{"description":"go gopher mascot image"}`)

	results, err := m.SearchPages("go", 1)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want exactly 1 result (maxResults=1), got %d", len(results))
	}
}

func TestManager_EnsureSchema_CreatesFileWhenAbsent(t *testing.T) {
	m := newManager(t)

	content, err := m.EnsureSchema()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content == "" {
		t.Fatal("expected non-empty schema content")
	}
}

func TestManager_EnsureSchema_WritesFileToDisk(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)

	_, _ = m.EnsureSchema()

	data, err := os.ReadFile(filepath.Join(dir, "schema.md"))
	if err != nil {
		t.Fatalf("schema.md not created on disk: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("schema.md is empty")
	}
}

func TestManager_EnsureSchema_ReturnsExistingContentUnchanged(t *testing.T) {
	dir := t.TempDir()
	m := vault.NewManager(dir)
	custom := "# My Custom Schema\n\nmy rules here\n"
	writeFixture(t, dir, "schema.md", custom)

	content, err := m.EnsureSchema()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != custom {
		t.Fatalf("want existing content %q, got %q", custom, content)
	}
}

func TestManager_EnsureSchema_DefaultTemplateContainsExpectedSections(t *testing.T) {
	m := newManager(t)

	content, _ := m.EnsureSchema()

	for _, section := range []string{"## Folders", "## Files", "## Links", "## Headings", "## Style"} {
		if !strings.Contains(content, section) {
			t.Errorf("default schema missing section %q", section)
		}
	}
}

func TestManager_UpdateAttachmentDescription_WritesDescriptionToSidecar(t *testing.T) {
	// Arrange — save an attachment and write an initial sidecar with no description.
	m := newManager(t)
	path, _ := m.SaveAttachment("doc.pdf", []byte("%PDF"))
	_ = m.WriteAttachmentMeta(path, vault.AttachmentMeta{
		ID:           "att1",
		OriginalName: "doc.pdf",
		MediaType:    "application/pdf",
	})

	// Act
	err := m.UpdateAttachmentDescription(path, "A report on Q1 results.")

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	meta, err := m.ReadAttachmentMeta(path)
	if err != nil {
		t.Fatalf("ReadAttachmentMeta: %v", err)
	}
	if meta.Description != "A report on Q1 results." {
		t.Errorf("want description %q, got %q", "A report on Q1 results.", meta.Description)
	}
}

func TestManager_UpdateAttachmentDescription_PreservesOtherFields(t *testing.T) {
	// Arrange
	m := newManager(t)
	path, _ := m.SaveAttachment("doc.pdf", []byte("%PDF"))
	original := vault.AttachmentMeta{
		ID:           "att2",
		OriginalName: "quarterly.pdf",
		MediaType:    "application/pdf",
	}
	_ = m.WriteAttachmentMeta(path, original)

	// Act
	_ = m.UpdateAttachmentDescription(path, "summary text")

	// Assert — other fields must survive the update.
	meta, _ := m.ReadAttachmentMeta(path)
	if meta.ID != "att2" {
		t.Errorf("ID changed: want %q, got %q", "att2", meta.ID)
	}
	if meta.OriginalName != "quarterly.pdf" {
		t.Errorf("OriginalName changed: want %q, got %q", "quarterly.pdf", meta.OriginalName)
	}
	if meta.MediaType != "application/pdf" {
		t.Errorf("MediaType changed: want %q, got %q", "application/pdf", meta.MediaType)
	}
}

func TestManager_UpdateAttachmentDescription_ErrorWhenNoSidecar(t *testing.T) {
	// Arrange — path exists in vault but has no sidecar.
	m := newManager(t)
	path, _ := m.SaveAttachment("doc.pdf", []byte("%PDF"))

	// Act
	err := m.UpdateAttachmentDescription(path, "notes")

	// Assert
	if err == nil {
		t.Fatal("expected error when sidecar does not exist, got nil")
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

// ServeFile — Content-Type

func TestManager_ServeFile_ContentType(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantCT   string
	}{
		{
			name:     "pdf extension gets application/pdf",
			filename: "report.pdf",
			wantCT:   "application/pdf",
		},
		{
			name:     "unknown extension falls back to octet-stream",
			filename: "binary.bin",
			wantCT:   "application/octet-stream",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			m := vault.NewManager(dir)
			writeFixture(t, dir, tt.filename, "data")

			req := httptest.NewRequest(http.MethodGet, "/serve", nil)
			w := httptest.NewRecorder()
			m.ServeFile(w, req, tt.filename)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			ct := w.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, tt.wantCT) {
				t.Errorf("Content-Type: want prefix %q, got %q", tt.wantCT, ct)
			}
		})
	}
}

// ServeFile — error cases

func TestManager_ServeFile_PathTraversalReturns400(t *testing.T) {
	m := newManager(t)
	req := httptest.NewRequest(http.MethodGet, "/serve", nil)
	w := httptest.NewRecorder()

	m.ServeFile(w, req, "../../etc/passwd")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path traversal, got %d", w.Code)
	}
}

func TestManager_ServeFile_MissingFileReturns404(t *testing.T) {
	m := newManager(t)
	req := httptest.NewRequest(http.MethodGet, "/serve", nil)
	w := httptest.NewRecorder()

	m.ServeFile(w, req, "nonexistent.md")

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing file, got %d", w.Code)
	}
}

func TestManager_ServeFile_DispositionHeader(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantDisp string
	}{
		{
			name:     "ascii filename uses plain form",
			filename: "notes.md",
			wantDisp: `attachment; filename="notes.md"`,
		},
		{
			name:     "unicode filename uses RFC 5987 form",
			filename: "日本語.md",
			wantDisp: "attachment; filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E.md",
		},
		{
			name:     "filename with spaces uses RFC 5987 form",
			filename: "my notes.md",
			wantDisp: "attachment; filename*=UTF-8''my%20notes.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			m := vault.NewManager(dir)
			writeFixture(t, dir, tt.filename, "content")

			req := httptest.NewRequest(http.MethodGet, "/serve", nil)
			w := httptest.NewRecorder()
			m.ServeFile(w, req, tt.filename)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			if got := w.Header().Get("Content-Disposition"); got != tt.wantDisp {
				t.Errorf("Content-Disposition: want %q, got %q", tt.wantDisp, got)
			}
		})
	}
}

