package drivesync_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// --- ListChanges ---

func TestDriveClient_ListChanges_ReturnsSinglePageOfChanges(t *testing.T) {
	// Arrange
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"newStartPageToken": "next-tok",
				"changes": []map[string]any{
					{
						"fileId":  "f1",
						"removed": false,
						"file": map[string]any{
							"name":         "notes.md",
							"parents":      []string{"parent-id"},
							"modifiedTime": "2024-01-01T00:00:00Z",
							"md5Checksum":  "abc",
							"trashed":      false,
						},
					},
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
	changes, newToken, err := client.ListChanges("tok-1")

	// Assert
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if newToken != "next-tok" {
		t.Errorf("newStartPageToken: want %q, got %q", "next-tok", newToken)
	}
	if len(changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.FileID != "f1" {
		t.Errorf("FileID: want %q, got %q", "f1", c.FileID)
	}
	if c.Name != "notes.md" {
		t.Errorf("Name: want %q, got %q", "notes.md", c.Name)
	}
	if c.ParentID != "parent-id" {
		t.Errorf("ParentID: want %q, got %q", "parent-id", c.ParentID)
	}
	if c.Removed {
		t.Error("want Removed=false")
	}
}

func TestDriveClient_ListChanges_HandlesPaginationAndReturnsNewStartPageToken(t *testing.T) {
	// Arrange — two pages; second page has newStartPageToken.
	callCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if r.URL.Query().Get("pageToken") == "page2" {
				jsonResponse(w, map[string]any{
					"newStartPageToken": "final-tok",
					"changes": []map[string]any{
						{"fileId": "f2", "removed": false, "file": map[string]any{"name": "b.md", "parents": []string{"p"}}},
					},
				})
				return
			}
			jsonResponse(w, map[string]any{
				"nextPageToken": "page2",
				"changes": []map[string]any{
					{"fileId": "f1", "removed": false, "file": map[string]any{"name": "a.md", "parents": []string{"p"}}},
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
	changes, newToken, err := client.ListChanges("tok-1")

	// Assert
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("want 2 changes, got %d", len(changes))
	}
	if newToken != "final-tok" {
		t.Errorf("newStartPageToken: want %q, got %q", "final-tok", newToken)
	}
}

func TestDriveClient_ListChanges_ExcludesFolderChanges(t *testing.T) {
	// Arrange — one folder change and one file change; only the file should be returned.
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"newStartPageToken": "tok-2",
				"changes": []map[string]any{
					{
						"fileId":  "folder-id",
						"removed": false,
						"file": map[string]any{
							"name":     "notes",
							"parents":  []string{"vault-root"},
							"mimeType": "application/vnd.google-apps.folder",
						},
					},
					{
						"fileId":  "file-id",
						"removed": false,
						"file": map[string]any{
							"name":         "todo.md",
							"parents":      []string{"vault-root"},
							"mimeType":     "text/plain",
							"modifiedTime": "2024-01-01T00:00:00Z",
						},
					},
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
	changes, _, err := client.ListChanges("tok-1")

	// Assert — only the file is returned; folder is filtered out.
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("want 1 change (file only), got %d: %v", len(changes), changes)
	}
	if changes[0].FileID != "file-id" {
		t.Errorf("want file-id, got %q", changes[0].FileID)
	}
}

func TestDriveClient_ListChanges_RemovalHasNoFileMetadata(t *testing.T) {
	// Arrange — a permanent delete: Removed=true, no file metadata.
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"newStartPageToken": "tok-2",
				"changes": []map[string]any{
					{"fileId": "f-gone", "removed": true},
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
	changes, _, err := client.ListChanges("tok-1")

	// Assert
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(changes))
	}
	if !changes[0].Removed {
		t.Error("want Removed=true")
	}
	if changes[0].FileID != "f-gone" {
		t.Errorf("FileID: want %q, got %q", "f-gone", changes[0].FileID)
	}
	if changes[0].Name != "" {
		t.Errorf("want empty Name for permanent delete, got %q", changes[0].Name)
	}
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
	driveID, _, _, err := client.Upload("todo.md", localPath, "parent-folder-id")

	// Assert
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if driveID == "" {
		t.Error("want non-empty driveID")
	}
}


func TestDriveClient_Upload_ReturnsMD5AndModifiedTime(t *testing.T) {
	// Arrange — Drive returns id, md5Checksum, and modifiedTime on create.
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "new-id", "md5Checksum": "deadbeef", "modifiedTime": "2024-06-01T10:00:00Z"})
		},
		"/upload/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"id": "new-id", "md5Checksum": "deadbeef", "modifiedTime": "2024-06-01T10:00:00Z"})
		},
	})
	defer srv.Close()

	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "file.md")
	if err := os.WriteFile(localPath, []byte("content"), 0644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	driveID, md5, modTime, err := client.Upload("file.md", localPath, "parent-id")

	// Assert
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if driveID == "" {
		t.Error("want non-empty driveID")
	}
	if md5 == "" {
		t.Error("want non-empty md5")
	}
	if modTime == "" {
		t.Error("want non-empty modifiedTime")
	}
}

// --- ListFolder ---

func TestDriveClient_ListFolder_ReturnsFlatFileList(t *testing.T) {
	// Arrange — folder contains two files, no subfolders.
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"files": []map[string]any{
					{"id": "f1", "name": "alpha.md", "mimeType": "text/plain", "modifiedTime": "2024-01-01T00:00:00Z", "md5Checksum": "aaa"},
					{"id": "f2", "name": "beta.md", "mimeType": "text/plain", "modifiedTime": "2024-01-02T00:00:00Z", "md5Checksum": "bbb"},
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
	files, err := client.ListFolder("root-id")

	// Assert
	if err != nil {
		t.Fatalf("ListFolder: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d: %v", len(files), files)
	}
	if files[0].RelPath != "alpha.md" || files[0].ID != "f1" || files[0].MD5 != "aaa" {
		t.Errorf("unexpected first file: %+v", files[0])
	}
	if files[1].RelPath != "beta.md" || files[1].ID != "f2" {
		t.Errorf("unexpected second file: %+v", files[1])
	}
}

func TestDriveClient_ListFolder_RecursesIntoSubfolders(t *testing.T) {
	// Arrange — root contains one subfolder; subfolder contains one file.
	callCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			callCount++
			switch callCount {
			case 1: // root listing → one subfolder
				jsonResponse(w, map[string]any{
					"files": []map[string]any{
						{"id": "sub1", "name": "notes", "mimeType": "application/vnd.google-apps.folder"},
					},
				})
			case 2: // subfolder listing → one file
				jsonResponse(w, map[string]any{
					"files": []map[string]any{
						{"id": "f1", "name": "note.md", "mimeType": "text/plain", "modifiedTime": "2024-01-01T00:00:00Z", "md5Checksum": "ccc"},
					},
				})
			default:
				jsonResponse(w, map[string]any{"files": []any{}})
			}
		},
	})
	defer srv.Close()

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}

	// Act
	files, err := client.ListFolder("root-id")

	// Assert
	if err != nil {
		t.Fatalf("ListFolder: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d: %v", len(files), files)
	}
	if files[0].RelPath != "notes/note.md" {
		t.Errorf("want RelPath %q, got %q", "notes/note.md", files[0].RelPath)
	}
}

func TestDriveClient_ListFolder_HandlesPagination(t *testing.T) {
	// Arrange — two pages of results.
	callCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if r.URL.Query().Get("pageToken") == "page2" {
				jsonResponse(w, map[string]any{
					"files": []map[string]any{
						{"id": "f2", "name": "beta.md", "mimeType": "text/plain", "modifiedTime": "2024-01-02T00:00:00Z", "md5Checksum": "bbb"},
					},
				})
				return
			}
			jsonResponse(w, map[string]any{
				"nextPageToken": "page2",
				"files": []map[string]any{
					{"id": "f1", "name": "alpha.md", "mimeType": "text/plain", "modifiedTime": "2024-01-01T00:00:00Z", "md5Checksum": "aaa"},
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
	files, err := client.ListFolder("root-id")

	// Assert
	if err != nil {
		t.Fatalf("ListFolder: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files from 2 pages, got %d: %v", len(files), files)
	}
}

// --- Download ---

func TestDriveClient_Download_WritesFileContentsToLocalPath(t *testing.T) {
	// Arrange — fake Drive serves file content on alt=media request.
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"alt=media": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("# Hello World"))
		},
	})
	defer srv.Close()

	client, err := drivesync.NewDriveClient(context.Background(), httpClient)
	if err != nil {
		t.Fatalf("NewDriveClient: %v", err)
	}
	localPath := filepath.Join(t.TempDir(), "subdir", "file.md")

	// Act
	err = client.Download("file-id-123", localPath)

	// Assert
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != "# Hello World" {
		t.Errorf("want %q, got %q", "# Hello World", string(got))
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
