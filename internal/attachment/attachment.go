package attachment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/suvish/autowiki/internal/vault"
)

// Describer is the subset of llm.Client used by the attachment handler.
// Defined here to avoid import cycles and enable stubbing in tests.
type Describer interface {
	DescribeImage(ctx context.Context, data []byte, mediaType string) (string, error)
}

// Handler handles attachment upload requests.
type Handler struct {
	vault     *vault.Manager
	describer Describer
}

// NewHandler returns an http.Handler for POST /api/attachments.
func NewHandler(vm *vault.Manager, d Describer) http.Handler {
	h := &Handler{vault: vm, describer: d}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/attachments", h.handleUpload)
	return mux
}

// uploadResponse is the JSON body returned on successful upload.
type uploadResponse struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	// 32 MB max in memory; larger spills to disk.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read the file bytes.
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "reading file", http.StatusInternalServerError)
		return
	}

	// Detect media type: prefer the part's Content-Type header if it is
	// specific (not the generic "application/octet-stream" fallback that
	// Go's multipart writer emits). Fall back to file extension, then sniff.
	mediaType := header.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = mt
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		if ext := extensionOf(header.Filename); ext != "" {
			if mt := mime.TypeByExtension(ext); mt != "" {
				if parsed, _, err := mime.ParseMediaType(mt); err == nil {
					mediaType = parsed
				}
			}
		}
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType = http.DetectContentType(data)
		if mt, _, err := mime.ParseMediaType(mediaType); err == nil {
			mediaType = mt
		}
	}

	// Save to vault.
	path, err := h.vault.SaveAttachment(header.Filename, data)
	if err != nil {
		http.Error(w, "saving attachment", http.StatusInternalServerError)
		return
	}

	// Generate description for images. PDFs are sent inline in the chat stream
	// rather than described at upload time, so they intentionally have no description.
	var description string
	if strings.HasPrefix(mediaType, "image/") {
		var err error
		description, err = h.describer.DescribeImage(r.Context(), data, mediaType)
		if err != nil {
			slog.Error("attachment: DescribeImage failed", "filename", header.Filename, "error", err)
		}
	}

	// Generate a short ID from the path.
	id := attachmentID(path)

	// Write sidecar metadata.
	_ = h.vault.WriteAttachmentMeta(path, vault.AttachmentMeta{
		ID:           id,
		OriginalName: header.Filename,
		MediaType:    mediaType,
		Description:  description,
		UploadedAt:   time.Now().UTC(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(uploadResponse{
		ID:          id,
		Path:        path,
		Description: description,
	})
}

// extensionOf returns the file extension (including the dot) for a filename,
// or an empty string if there is none.
func extensionOf(filename string) string {
	for i := len(filename) - 1; i >= 0; i-- {
		if filename[i] == '.' {
			return filename[i:]
		}
		if filename[i] == '/' || filename[i] == '\\' {
			break
		}
	}
	return ""
}

// attachmentID derives a short stable ID from the vault-relative path.
func attachmentID(path string) string {
	// Use the filename portion (without directory) as a human-readable prefix,
	// trimmed to 32 chars for safety.
	base := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			base = path[i+1:]
			break
		}
	}
	if len(base) > 32 {
		base = base[:32]
	}
	return fmt.Sprintf("att_%s", base)
}
