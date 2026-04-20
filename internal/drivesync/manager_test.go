package drivesync_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/drivesync"
	"github.com/suvish/autowiki/internal/store"
)

var testCfg = config.DriveSyncConfig{RootFolderName: "autowiki", VaultFolderName: "vault"}

func TestSyncManager_Start_NoOpWhenNoRefreshToken(t *testing.T) {
	// Arrange — token store has no token; driveClient will be nil until token found.
	ts := store.NewMemStore()
	sm := drivesync.New(testCfg, "client-id", "client-secret", openTestPebble(t), t.TempDir(), ts)

	// Act — must return without error or panic.
	sm.Start(context.Background())
}

func TestSyncManager_Start_CreatesVaultFolderUnderRoot(t *testing.T) {
	// Arrange — pre-built client; fake Drive records folder creation order.
	var created []string
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
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

	sm := drivesync.NewWithHTTPClient(testCfg, openTestPebble(t), t.TempDir(), httpClient)

	// Act
	sm.Start(context.Background())
	sm.Shutdown()

	// Assert — root created first, then vault subfolder.
	if len(created) < 2 {
		t.Fatalf("expected at least 2 folder creations, got %d: %v", len(created), created)
	}
	if created[0] != "autowiki" {
		t.Errorf("first folder: want %q, got %q", "autowiki", created[0])
	}
	if created[1] != "vault" {
		t.Errorf("second folder: want %q, got %q", "vault", created[1])
	}
}

func TestSyncManager_ReconcileUpload_UploadsLocalOnlyFiles(t *testing.T) {
	// Arrange — vault has two files; state already tracks one.
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "notes.md"), []byte("hi"), 0644); err != nil {
		t.Fatalf("creating notes.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "todo.md"), []byte("do"), 0644); err != nil {
		t.Fatalf("creating todo.md: %v", err)
	}

	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFile("notes.md", drivesync.FileEntry{DriveID: "already-there"})
	_ = st.SetRootFolderID("vault-folder-id")

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "todo-drive-id"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)

	// Act
	sm.ReconcileUpload(context.Background())
	sm.Shutdown()

	// Assert — only todo.md was uploaded (notes.md was in state already).
	entry, err := st.GetFile("todo.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if entry == nil {
		t.Fatal("expected todo.md to be tracked in state after upload")
	}
	if entry.DriveID == "" {
		t.Error("expected non-empty DriveID for todo.md")
	}

	entry2, _ := st.GetFile("notes.md")
	if entry2 == nil || entry2.DriveID != "already-there" {
		t.Error("notes.md should remain unchanged in state")
	}
}

func TestSyncManager_UploadWorker_ProcessesTrashJobAndRemovesStateEntry(t *testing.T) {
	// Arrange — state has a file; worker should remove it after trashing.
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFile("old.md", drivesync.FileEntry{DriveID: "drive-id-old"})

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, _ *http.Request) {
			// Handles both list (GET) and trash (PATCH) — just return success.
			jsonResponse(w, map[string]any{"id": "drive-id-old"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, t.TempDir(), httpClient)
	sm.ReconcileUpload(context.Background()) // starts worker on empty vault
	drivesync.EnqueueTrashForTest(sm, "old.md", "drive-id-old")
	sm.Shutdown()

	// Assert — file entry removed from state.
	entry, err := st.GetFile("old.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if entry != nil {
		t.Errorf("expected old.md to be removed from state, got %+v", entry)
	}
}

func TestSyncManager_UploadWorker_RecordsErrorAndContinuesOnUploadFailure(t *testing.T) {
	// Arrange — two vault files; first upload fails, second succeeds.
	// WalkDir visits alphabetically, so "aaa.md" is processed before "zzz.md".
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "aaa.md"), []byte("a"), 0644); err != nil {
		t.Fatalf("creating aaa.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "zzz.md"), []byte("z"), 0644); err != nil {
		t.Fatalf("creating zzz.md: %v", err)
	}

	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetRootFolderID("vault-folder-id")

	postCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			postCount++
			if postCount == 1 {
				http.Error(w, "simulated upload failure", http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]any{"id": "zzz-drive-id"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)

	// Act
	sm.ReconcileUpload(context.Background())
	sm.Shutdown()

	// Assert — zzz.md is in state (worker continued after aaa.md failed).
	zzzEntry, err := st.GetFile("zzz.md")
	if err != nil {
		t.Fatalf("GetFile zzz.md: %v", err)
	}
	if zzzEntry == nil {
		t.Error("expected zzz.md to be in state (worker must continue after error)")
	}

	// Assert — aaa.md is NOT in state (its upload failed).
	aaaEntry, err := st.GetFile("aaa.md")
	if err != nil {
		t.Fatalf("GetFile aaa.md: %v", err)
	}
	if aaaEntry != nil {
		t.Error("expected aaa.md to be absent from state after upload failure")
	}
}

func TestSyncManager_UploadWorker_SetsLastErrorOnFailure(t *testing.T) {
	// Arrange — inject a trash job where the Drive call fails.
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFile("bad.md", drivesync.FileEntry{DriveID: "bad-drive-id"})

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "simulated trash failure", http.StatusInternalServerError)
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, t.TempDir(), httpClient)
	sm.ReconcileUpload(context.Background()) // starts worker on empty vault
	drivesync.EnqueueTrashForTest(sm, "bad.md", "bad-drive-id")
	sm.Shutdown()

	// Assert — last error is recorded.
	lastErr, err := st.GetLastError()
	if err != nil {
		t.Fatalf("GetLastError: %v", err)
	}
	if lastErr == "" {
		t.Error("expected SetLastError to be called on trash failure")
	}
}

func TestSyncManager_WatcherConsumer_TracksNewFileCreatedAfterStart(t *testing.T) {
	// Arrange — empty vault; fake Drive handles folder creation, page token, and upload.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "folder-or-file-id"})
		},
		"/upload/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{"id": "new-drive-id"})
		},
		"/drive/v3/changes/startPageToken": func(w http.ResponseWriter, _ *http.Request) {
			jsonResponse(w, map[string]any{"startPageToken": "tok-1"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetWatcherDebounce(sm, 50*time.Millisecond)
	sm.Start(context.Background()) // reconcile runs (empty vault), watcher starts

	// Act — create a new file after Start returns.
	if err := os.WriteFile(filepath.Join(vaultDir, "new.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("creating file: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // debounce + processing
	sm.Shutdown()

	// Assert — new.md is tracked in state.
	entry, err := st.GetFile("new.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if entry == nil {
		t.Fatal("expected new.md to be tracked in state after watcher upload")
	}
	if entry.DriveID == "" {
		t.Error("expected non-empty DriveID for new.md")
	}
}

func TestSyncManager_WatcherConsumer_TrashesDeletedFile(t *testing.T) {
	// Arrange — vault has a file that's already tracked in state.
	vaultDir := t.TempDir()
	filePath := filepath.Join(vaultDir, "old.md")
	if err := os.WriteFile(filePath, []byte("bye"), 0644); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFile("old.md", drivesync.FileEntry{DriveID: "old-drive-id"})

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			// POST (create folder) or PATCH (trash)
			jsonResponse(w, map[string]any{"id": "old-drive-id"})
		},
		"/drive/v3/changes/startPageToken": func(w http.ResponseWriter, _ *http.Request) {
			jsonResponse(w, map[string]any{"startPageToken": "tok-1"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetWatcherDebounce(sm, 50*time.Millisecond)
	sm.Start(context.Background()) // reconcile skips old.md (already in state)

	// Act — delete the file.
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("removing file: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // debounce + processing
	sm.Shutdown()

	// Assert — old.md removed from state.
	entry, err := st.GetFile("old.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if entry != nil {
		t.Errorf("expected old.md to be removed from state, got %+v", entry)
	}
}

func TestSyncManager_ReconcileUpload_SkipsFilesAlreadyInState(t *testing.T) {
	// Arrange — vault has one file and it's already in state.
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "existing.md"), []byte("x"), 0644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFile("existing.md", drivesync.FileEntry{DriveID: "drive-id-existing"})

	apiCallCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, _ *http.Request) {
			apiCallCount++
			jsonResponse(w, map[string]any{"files": []any{}})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)

	// Act
	sm.ReconcileUpload(context.Background())
	sm.Shutdown()

	// Assert — no upload API calls made.
	if apiCallCount > 0 {
		t.Errorf("expected 0 API calls, got %d", apiCallCount)
	}
}
