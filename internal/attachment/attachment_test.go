package attachment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/attachment"
	"github.com/suvish/autowiki/internal/vault"
)

// stubDescriber is a fake llm.Describer that returns a fixed description.
type stubDescriber struct {
	desc string
	err  error
}

func (s *stubDescriber) DescribeImage(_ context.Context, _ []byte, _ string) (string, error) {
	return s.desc, s.err
}

func (s *stubDescriber) DescribeDocument(_ context.Context, _ []byte, _ string) (string, error) {
	return s.desc, s.err
}

func newTestHandler(t *testing.T, describer attachment.Describer) http.Handler {
	t.Helper()
	vm := vault.NewManager(t.TempDir())
	return attachment.NewHandler(vm, describer)
}

// multipartBody builds a multipart/form-data body with a single "file" field.
func multipartBody(t *testing.T, filename, contentType string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(data)
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestHandler_Upload_ImageReturnsDescription(t *testing.T) {
	// Arrange
	h := newTestHandler(t, &stubDescriber{desc: "a blue square"})
	body, ct := multipartBody(t, "photo.png", "image/png", []byte("fakeimg"))
	req := httptest.NewRequest(http.MethodPost, "/api/attachments", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["description"] != "a blue square" {
		t.Errorf("want description %q, got %q", "a blue square", resp["description"])
	}
	if resp["id"] == "" {
		t.Error("expected non-empty id")
	}
	path, _ := resp["path"].(string)
	if !strings.HasPrefix(path, "_attachments/") {
		t.Errorf("expected path under _attachments/, got %q", path)
	}
}

func TestHandler_Upload_NonImageNonPdfReturnsEmptyDescription(t *testing.T) {
	// Arrange — describer should NOT be called for files that are neither images nor PDFs.
	h := newTestHandler(t, &stubDescriber{desc: "should not be called"})
	body, ct := multipartBody(t, "notes.txt", "text/plain", []byte("some text"))
	req := httptest.NewRequest(http.MethodPost, "/api/attachments", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["description"] != "" {
		t.Errorf("expected empty description for non-image non-PDF, got %q", resp["description"])
	}
}

func TestHandler_Upload_WritesSidecarMetadata(t *testing.T) {
	// Arrange — use an explicit vault so we can inspect it afterwards.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	h := attachment.NewHandler(vm, &stubDescriber{desc: "a blue square"})

	body, ct := multipartBody(t, "photo.png", "image/png", []byte("fakeimg"))
	req := httptest.NewRequest(http.MethodPost, "/api/attachments", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// Assert — sidecar exists and contains correct fields.
	meta, err := vm.ReadAttachmentMeta(resp.Path)
	if err != nil {
		t.Fatalf("expected sidecar to be written at %q: %v", resp.Path, err)
	}
	if meta.Description != "a blue square" {
		t.Errorf("want description %q, got %q", "a blue square", meta.Description)
	}
	if meta.OriginalName != "photo.png" {
		t.Errorf("want original_name %q, got %q", "photo.png", meta.OriginalName)
	}
	if meta.MediaType != "image/png" {
		t.Errorf("want media_type %q, got %q", "image/png", meta.MediaType)
	}
}

func TestHandler_Upload_DescribesPdfUnderSizeLimit(t *testing.T) {
	// Arrange — small PDF (well under 5 MB) with a describer that returns a summary.
	h := newTestHandler(t, &stubDescriber{desc: "a guide to Go interfaces"})
	fakePDF := []byte("%PDF-1.4 fake content")
	body, ct := multipartBody(t, "guide.pdf", "application/pdf", fakePDF)
	req := httptest.NewRequest(http.MethodPost, "/api/attachments", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["description"] != "a guide to Go interfaces" {
		t.Errorf("want description %q, got %q", "a guide to Go interfaces", resp["description"])
	}
}

func TestHandler_Upload_SkipsDescriptionForOversizedPdf(t *testing.T) {
	// Arrange — PDF over 5 MB; describer should not be called.
	called := false
	describer := &callTrackingDescriber{onDescribeDocument: func() { called = true }}
	h := newTestHandler(t, describer)
	// 5 MB + 1 byte
	largePDF := make([]byte, 5_000_001)
	copy(largePDF, []byte("%PDF-1.4"))
	body, ct := multipartBody(t, "large.pdf", "application/pdf", largePDF)
	req := httptest.NewRequest(http.MethodPost, "/api/attachments", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if called {
		t.Error("DescribeDocument should not be called for PDFs over 5 MB")
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// Description should indicate the file was too large, not be empty.
	desc, _ := resp["description"].(string)
	if !strings.Contains(desc, "too large") {
		t.Errorf("want description to mention 'too large', got %q", desc)
	}
}

func TestHandler_Upload_SkipsDescriptionForNonPdf(t *testing.T) {
	// Arrange — plain text file; DescribeDocument should not be called.
	called := false
	describer := &callTrackingDescriber{onDescribeDocument: func() { called = true }}
	h := newTestHandler(t, describer)
	body, ct := multipartBody(t, "notes.txt", "text/plain", []byte("some text"))
	req := httptest.NewRequest(http.MethodPost, "/api/attachments", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if called {
		t.Error("DescribeDocument should not be called for non-PDF files")
	}
}

// callTrackingDescriber is a Describer that tracks whether DescribeDocument was called.
type callTrackingDescriber struct {
	onDescribeDocument func()
}

func (c *callTrackingDescriber) DescribeImage(_ context.Context, _ []byte, _ string) (string, error) {
	return "", nil
}

func (c *callTrackingDescriber) DescribeDocument(_ context.Context, _ []byte, _ string) (string, error) {
	if c.onDescribeDocument != nil {
		c.onDescribeDocument()
	}
	return "", nil
}

func TestHandler_Upload_MissingFileReturns400(t *testing.T) {
	// Arrange — multipart body with no file field
	h := newTestHandler(t, &stubDescriber{})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
