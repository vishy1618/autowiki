package drivesync_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/drivesync"
)

// driveHandler builds an httptest.Server that responds to Drive API calls.
// handlers maps URL path substrings to handler functions.
func newFakeDrive(t *testing.T, handlers map[string]http.HandlerFunc) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for pattern, h := range handlers {
			if strings.Contains(r.URL.Path, pattern) || strings.Contains(r.URL.RawQuery, pattern) {
				h(w, r)
				return
			}
		}
		http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL), http.StatusInternalServerError)
	}))
	client := &http.Client{
		Transport: rewriteHostTransport(srv),
	}
	return srv, client
}

// rewriteHostTransport redirects all requests to the given test server.
func rewriteHostTransport(srv *httptest.Server) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req2 := req.Clone(req.Context())
		req2.URL.Scheme = "http"
		req2.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req2)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func parseBody(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// --- GetStartPageToken ---

func TestDriveClient_GetStartPageToken_ReturnsToken(t *testing.T) {
	// Arrange
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes/startPageToken": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"startPageToken": "page-token-42"})
		},
	})
	defer srv.Close()

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	tok, err := client.GetStartPageToken()

	// Assert
	if err != nil {
		t.Fatalf("GetStartPageToken: %v", err)
	}
	if tok != "page-token-42" {
		t.Errorf("token: want %q, got %q", "page-token-42", tok)
	}
}

// --- EnsureFolder ---

func TestDriveClient_EnsureFolder_CreatesAndReturnsNewFolderID(t *testing.T) {
	// Arrange — list returns empty, create returns a new folder.
	listCalled := false
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				listCalled = true
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			// POST — create
			jsonResponse(w, map[string]any{"id": "folder-new"})
		},
	})
	defer srv.Close()

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	id, err := client.EnsureFolder("my-vault", "")

	// Assert
	if err != nil {
		t.Fatalf("EnsureFolder: %v", err)
	}
	if !listCalled {
		t.Error("expected list call before create")
	}
	if id != "folder-new" {
		t.Errorf("folderID: want %q, got %q", "folder-new", id)
	}
}

// --- EnsureFolderPath ---

func TestDriveClient_EnsureFolderPath_ResolvesFromStateWithoutAPICall(t *testing.T) {
	// Arrange — state already has the folder; API should never be called
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFolder("notes/work", "cached-folder-id")

	apiCalled := false
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			apiCalled = true
			jsonResponse(w, map[string]any{"files": []any{}})
		},
	})
	defer srv.Close()

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	id, err := client.EnsureFolderPath("notes/work", "root-id", st)

	// Assert
	if err != nil {
		t.Fatalf("EnsureFolderPath: %v", err)
	}
	if id != "cached-folder-id" {
		t.Errorf("want %q, got %q", "cached-folder-id", id)
	}
	if apiCalled {
		t.Error("expected no API call when folder is in state")
	}
}

func TestDriveClient_EnsureFolderPath_CreatesAndCachesMissingFolder(t *testing.T) {
	// Arrange — state is empty; API creates the folder
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "new-folder-id"})
		},
	})
	defer srv.Close()

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	id, err := client.EnsureFolderPath("notes/work", "root-id", st)

	// Assert — returned ID is correct
	if err != nil {
		t.Fatalf("EnsureFolderPath: %v", err)
	}
	if id != "new-folder-id" {
		t.Errorf("returned ID: want %q, got %q", "new-folder-id", id)
	}

	// Assert — ID is now cached in state
	cached, _ := st.GetFolder("notes/work")
	if cached != "new-folder-id" {
		t.Errorf("cached ID: want %q, got %q", "new-folder-id", cached)
	}
}

// --- Upload ---

func TestDriveClient_Upload_CreatesFileAndReturnsDriveID(t *testing.T) {
	// Arrange — list returns empty (no existing file), create returns new ID
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			// multipart upload create
			jsonResponse(w, map[string]any{"id": "uploaded-file-id"})
		},
		"/upload/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"id": "uploaded-file-id"})
		},
	})
	defer srv.Close()

	// Create a temp file to upload
	tmpDir := t.TempDir()
	localPath := tmpDir + "/todo.md"
	if err := os.WriteFile(localPath, []byte("# Hello"), 0644); err != nil {
		t.Fatalf("creating temp file: %v", err)
	}

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	driveID, err := client.Upload("todo.md", localPath, "parent-folder-id")

	// Assert
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if driveID == "" {
		t.Error("want non-empty driveID")
	}
}


func TestDriveClient_EnsureFolder_ReturnsExistingFolderID(t *testing.T) {
	// Arrange — Drive returns a file list with one matching folder.
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"files": []map[string]any{
					{"id": "folder-abc", "name": "my-vault"},
				},
			})
		},
	})
	defer srv.Close()

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	id, err := client.EnsureFolder("my-vault", "")

	// Assert
	if err != nil {
		t.Fatalf("EnsureFolder: %v", err)
	}
	if id != "folder-abc" {
		t.Errorf("folderID: want %q, got %q", "folder-abc", id)
	}
}
