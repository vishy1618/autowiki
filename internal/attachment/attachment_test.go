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

func TestHandler_Upload_NonImageReturnsEmptyDescription(t *testing.T) {
	// Arrange — describer should NOT be called for non-image files
	h := newTestHandler(t, &stubDescriber{desc: "should not be called"})
	body, ct := multipartBody(t, "notes.pdf", "application/pdf", []byte("pdfdata"))
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
		t.Errorf("expected empty description for non-image, got %q", resp["description"])
	}
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
