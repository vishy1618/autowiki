package drivesync_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
