package drivesync_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/drivesync"
	"github.com/suvish/autowiki/internal/store"
)

func TestSyncManager_Start_NoOpWhenNoRefreshToken(t *testing.T) {
	// Arrange — token store has no token.
	ts := store.NewMemStore()
	cfg := config.DriveSyncConfig{RootFolderName: "autowiki", VaultFolderName: "vault"}
	sm := drivesync.New(cfg, "client-id", "client-secret", ts)

	// Act — must return without error or panic.
	sm.Start(context.Background())
}

func TestSyncManager_Start_CreatesVaultFolderUnderRoot(t *testing.T) {
	// Arrange — token present; fake Drive records folder creation order.
	ts := store.NewMemStore()
	_ = ts.SetDriveToken("fake-refresh-token")

	var created []string
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			// Parse the name from the request to track creation order.
			var body struct {
				Name string `json:"name"`
			}
			_ = parseBody(r, &body)
			created = append(created, body.Name)
			jsonResponse(w, map[string]any{"id": "id-" + body.Name})
		},
		"/drive/v3/changes/startPageToken": func(w http.ResponseWriter, _ *http.Request) {
			jsonResponse(w, map[string]any{"startPageToken": "tok-1"})
		},
	})
	defer srv.Close()

	cfg := config.DriveSyncConfig{RootFolderName: "autowiki", VaultFolderName: "vault"}
	sm := drivesync.NewWithHTTPClient(cfg, ts, httpClient)

	// Act
	sm.Start(context.Background())

	// Assert — root created first, then vault subfolder.
	if len(created) != 2 {
		t.Fatalf("expected 2 folder creations, got %d: %v", len(created), created)
	}
	if created[0] != "autowiki" {
		t.Errorf("first folder: want %q, got %q", "autowiki", created[0])
	}
	if created[1] != "vault" {
		t.Errorf("second folder: want %q, got %q", "vault", created[1])
	}
}
