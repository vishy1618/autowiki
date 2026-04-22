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

// --- Poller ---

func TestSyncManager_Poller_DownloadsNewDriveFileAndUpdatesPageToken(t *testing.T) {
	// Arrange — state has a page token; Drive returns one new-file change.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetPageToken("old-tok")
	_ = st.SetRootFolderID("vault-root-id") // so reconcile knows root

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"newStartPageToken": "new-tok",
				"changes": []map[string]any{
					{
						"fileId":  "drive-file-123",
						"removed": false,
						"file": map[string]any{
							"name":         "synced.md",
							"parents":      []string{"vault-root-id"},
							"modifiedTime": "2024-01-01T00:00:00Z",
						},
					},
				},
			})
		},
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				w.Write([]byte("synced content"))
				return
			}
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "folder-id"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	ctx, cancel := context.WithCancel(context.Background())
	sm.Reconcile(ctx) // start worker

	// Act — run one poll cycle.
	drivesync.PollOnceForTest(sm, ctx)
	cancel()
	sm.Shutdown()

	// Assert — file downloaded.
	content, err := os.ReadFile(filepath.Join(vaultDir, "synced.md"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(content) != "synced content" {
		t.Errorf("want %q, got %q", "synced content", string(content))
	}

	// Assert — page token updated.
	tok, err := st.GetPageToken()
	if err != nil {
		t.Fatalf("GetPageToken: %v", err)
	}
	if tok != "new-tok" {
		t.Errorf("page token: want %q, got %q", "new-tok", tok)
	}
}

func TestSyncManager_Download_DoesNotTriggerEchoUpload(t *testing.T) {
	// Arrange — watcher is running; a file is downloaded from Drive.
	// The resulting local write must NOT be re-uploaded (echo loop).
	//
	// The fake Drive returns "vault-folder-id" for the vault subfolder so that
	// sm.vaultFolderID matches the parentID in the change — ensuring the download
	// actually happens and we are genuinely testing echo suppression.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetPageToken("tok-1") // pre-seed so Start() doesn't fetch a fresh token

	uploadCount := 0
	folderCallCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"newStartPageToken": "tok-2",
				"changes": []map[string]any{{
					"fileId":  "drive-file-id",
					"removed": false,
					"file": map[string]any{
						"name":         "synced.md",
						"parents":      []string{"vault-folder-id"},
						"modifiedTime": "2024-06-01T10:00:00Z",
					},
				}},
			})
		},
		"/upload/drive/v3/files": func(w http.ResponseWriter, _ *http.Request) {
			uploadCount++
			jsonResponse(w, map[string]any{"id": "echo-id", "md5Checksum": "abc", "modifiedTime": "t"})
		},
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				w.Write([]byte("from drive"))
				return
			}
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			// Return distinct IDs so vault subfolder gets "vault-folder-id".
			folderCallCount++
			if folderCallCount == 1 {
				jsonResponse(w, map[string]any{"id": "root-folder-id"})
			} else {
				jsonResponse(w, map[string]any{"id": "vault-folder-id"})
			}
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetWatcherDebounce(sm, 50*time.Millisecond)
	sm.Start(context.Background())

	// Act — poll once; file downloads to vault; watcher fires after debounce.
	drivesync.PollOnceForTest(sm, context.Background())
	time.Sleep(300 * time.Millisecond) // debounce (50 ms) + upload worker
	sm.Shutdown()

	// Assert — no echo upload triggered by our own file write.
	if uploadCount > 0 {
		t.Errorf("expected 0 uploads (echo suppressed), got %d", uploadCount)
	}
}

func TestSyncManager_Poller_RetriesOnceAfterTransientError(t *testing.T) {
	// Arrange — first ListChanges call fails; second succeeds with one file change.
	// The download should still happen, proving the retry fired.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetPageToken("old-tok")
	_ = st.SetRootFolderID("vault-root-id")

	callCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 1 {
				// Simulate transient error (e.g. EOF / 500).
				http.Error(w, "transient error", http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]any{
				"newStartPageToken": "new-tok",
				"changes": []map[string]any{
					{
						"fileId":  "drive-file-retry",
						"removed": false,
						"file": map[string]any{
							"name":         "retry.md",
							"parents":      []string{"vault-root-id"},
							"modifiedTime": "2024-01-01T00:00:00Z",
						},
					},
				},
			})
		},
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				w.Write([]byte("retry content"))
				return
			}
			jsonResponse(w, map[string]any{"files": []any{}})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	ctx, cancel := context.WithCancel(context.Background())
	sm.Reconcile(ctx)

	// Act
	drivesync.PollOnceForTest(sm, ctx)
	cancel()
	sm.Shutdown()

	// Assert — file downloaded despite first call failing.
	content, err := os.ReadFile(filepath.Join(vaultDir, "retry.md"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(content) != "retry content" {
		t.Errorf("want %q, got %q", "retry content", string(content))
	}

	// Assert — two calls were made to /drive/v3/changes.
	if callCount != 2 {
		t.Errorf("expected 2 ListChanges calls (1 failure + 1 retry), got %d", callCount)
	}
}

func TestSyncManager_Poller_DoesNotReDownloadAfterSuccessfulDownload(t *testing.T) {
	// Arrange — first poll downloads a file; second poll reports the same modifiedTime.
	// Drive modtime is far in the future so conflict resolution always favours Drive
	// (local can never be "newer"). Only the DriveModTime stored in state after the
	// first download can prevent the second download.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetPageToken("tok-1")
	_ = st.SetRootFolderID("vault-root-id")

	const driveModTime = "2099-01-01T00:00:00Z" // far future — local is always older
	downloadCount := 0
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			tok := r.URL.Query().Get("pageToken")
			nextTok := map[string]string{"tok-1": "tok-2", "tok-2": "tok-3"}[tok]
			if nextTok == "" {
				nextTok = tok
			}
			jsonResponse(w, map[string]any{
				"newStartPageToken": nextTok,
				"changes": []map[string]any{{
					"fileId":  "drive-file-id",
					"removed": false,
					"file": map[string]any{
						"name":         "notes.md",
						"parents":      []string{"vault-root-id"},
						"modifiedTime": driveModTime,
					},
				}},
			})
		},
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				downloadCount++
				w.Write([]byte("content"))
				return
			}
			jsonResponse(w, map[string]any{"files": []any{}})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	ctx, cancel := context.WithCancel(context.Background())
	sm.Reconcile(ctx)

	// Act — two poll cycles with the same Drive change.
	drivesync.PollOnceForTest(sm, ctx)
	drivesync.PollOnceForTest(sm, ctx)
	cancel()
	sm.Shutdown()

	// Assert — file downloaded exactly once; state records the DriveModTime.
	if downloadCount != 1 {
		t.Errorf("expected 1 download, got %d", downloadCount)
	}
	entry, err := st.GetFile("notes.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if entry == nil {
		t.Fatal("expected notes.md in state")
	}
	if entry.DriveModTime != driveModTime {
		t.Errorf("DriveModTime: want %q, got %q", driveModTime, entry.DriveModTime)
	}
}

func TestSyncManager_Poller_SkipsDownloadWhenDriveModTimeMatchesState(t *testing.T) {
	// Arrange — file is in State with a matching DriveModTime; Drive reports the same modifiedTime.
	// No download should occur.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetPageToken("tok-1")
	_ = st.SetFile("notes.md", drivesync.FileEntry{DriveID: "drive-id-1", DriveModTime: "2024-01-01T00:00:00Z"})

	downloadCalled := false
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"newStartPageToken": "tok-2",
				"changes": []map[string]any{
					{
						"fileId":  "drive-id-1",
						"removed": false,
						"file": map[string]any{
							"name":         "notes.md",
							"parents":      []string{"vault-root-id"},
							"modifiedTime": "2024-01-01T00:00:00Z",
						},
					},
				},
			})
		},
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				downloadCalled = true
				w.Write([]byte("should not be downloaded"))
				return
			}
			jsonResponse(w, map[string]any{"files": []any{}})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	ctx, cancel := context.WithCancel(context.Background())
	sm.Reconcile(ctx)
	drivesync.PollOnceForTest(sm, ctx)
	cancel()
	sm.Shutdown()

	// Assert — no download was attempted.
	if downloadCalled {
		t.Error("expected no download when DriveModTime matches state")
	}
}

func TestSyncManager_Poller_DeletesLocalFileWhenDriveChangeIsRemoval(t *testing.T) {
	// Arrange — local file exists and is tracked; Drive reports it removed.
	vaultDir := t.TempDir()
	localFile := filepath.Join(vaultDir, "gone.md")
	if err := os.WriteFile(localFile, []byte("bye"), 0644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetPageToken("tok-1")
	_ = st.SetFile("gone.md", drivesync.FileEntry{DriveID: "drive-gone-id"})

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/changes": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, map[string]any{
				"newStartPageToken": "tok-2",
				"changes": []map[string]any{
					{"fileId": "drive-gone-id", "removed": true},
				},
			})
		},
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "folder-id"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	ctx, cancel := context.WithCancel(context.Background())
	sm.Reconcile(ctx)

	// Act
	drivesync.PollOnceForTest(sm, ctx)
	cancel()
	sm.Shutdown()

	// Assert — local file deleted.
	if _, err := os.Stat(localFile); !os.IsNotExist(err) {
		t.Error("expected gone.md to be deleted locally")
	}

	// Assert — state entry removed.
	entry, err := st.GetFile("gone.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if entry != nil {
		t.Errorf("expected gone.md to be removed from state, got %+v", entry)
	}
}

// --- Download job ---

func TestSyncManager_DownloadJob_WritesFileAndUpdatesState(t *testing.T) {
	// Arrange — fake Drive serves file content on download request.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				w.Write([]byte("# Downloaded"))
				return
			}
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "folder-id"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	sm.Reconcile(context.Background()) // starts worker on empty vault
	drivesync.EnqueueDownloadForTest(sm, "notes.md", "drive-file-id", vaultDir, "")
	sm.Shutdown()

	// Assert — file written to disk.
	content, err := os.ReadFile(filepath.Join(vaultDir, "notes.md"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(content) != "# Downloaded" {
		t.Errorf("want %q, got %q", "# Downloaded", string(content))
	}

	// Assert — state updated.
	entry, err := st.GetFile("notes.md")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if entry == nil {
		t.Fatal("expected notes.md to be tracked in state after download")
	}
	if entry.DriveID != "drive-file-id" {
		t.Errorf("DriveID: want %q, got %q", "drive-file-id", entry.DriveID)
	}
}

// --- resolveRelPath ---

func TestSyncManager_ResolveRelPath_WhenParentIsVaultRoot(t *testing.T) {
	// Arrange
	sm := drivesync.NewWithHTTPClient(testCfg, openTestPebble(t), t.TempDir(), &http.Client{})
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	change := drivesync.DriveChange{FileID: "f1", Name: "notes.md", ParentID: "vault-root-id"}

	// Act
	relPath, ok := drivesync.ResolveRelPathForTest(sm, change)

	// Assert
	if !ok {
		t.Fatal("want ok=true")
	}
	if relPath != "notes.md" {
		t.Errorf("want %q, got %q", "notes.md", relPath)
	}
}

func TestSyncManager_ResolveRelPath_WhenParentIsKnownSubfolder(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFolder("notes/work", "subfolder-drive-id")

	sm := drivesync.NewWithHTTPClient(testCfg, db, t.TempDir(), &http.Client{})
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	change := drivesync.DriveChange{FileID: "f1", Name: "meeting.md", ParentID: "subfolder-drive-id"}

	// Act
	relPath, ok := drivesync.ResolveRelPathForTest(sm, change)

	// Assert
	if !ok {
		t.Fatal("want ok=true")
	}
	if relPath != "notes/work/meeting.md" {
		t.Errorf("want %q, got %q", "notes/work/meeting.md", relPath)
	}
}

func TestSyncManager_ResolveRelPath_ReturnsFalseWhenParentUnknown(t *testing.T) {
	// Arrange
	sm := drivesync.NewWithHTTPClient(testCfg, openTestPebble(t), t.TempDir(), &http.Client{})
	drivesync.SetVaultFolderIDForTest(sm, "vault-root-id")

	change := drivesync.DriveChange{FileID: "f1", Name: "file.md", ParentID: "unknown-parent-id"}

	// Act
	_, ok := drivesync.ResolveRelPathForTest(sm, change)

	// Assert
	if ok {
		t.Error("want ok=false for unknown parent")
	}
}

func TestSyncManager_Start_NoOpWhenNoRefreshToken(t *testing.T) {
	// Arrange — token store has no token; driveClient will be nil until token found.
	ts := store.NewMemStore()
	sm := drivesync.New(testCfg, "client-id", "client-secret", openTestPebble(t), t.TempDir(), ts)

	// Act — must return without error or panic.
	sm.Start(context.Background())
}

func TestSyncManager_Start_ReturnsWithoutPanicWhenDriveErrors(t *testing.T) {
	// Arrange — fake Drive returns 500 for every request.
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "simulated drive failure", http.StatusInternalServerError)
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, openTestPebble(t), t.TempDir(), httpClient)

	// Act — must log the error and return cleanly (no panic, no hang).
	sm.Start(context.Background())
	sm.Shutdown()
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

func TestSyncManager_Reconcile_UploadsLocalOnlyFiles(t *testing.T) {
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
	sm.Reconcile(context.Background())
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

func TestSyncManager_Reconcile_DownloadsDriveOnlyFiles(t *testing.T) {
	// Arrange — Drive has a file; no local copy and no State entry.
	// Reconcile should download it.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	_ = drivesync.NewState(db)

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Query().Get("alt") == "media":
				w.Write([]byte("from drive"))
			case r.Method == http.MethodGet && r.URL.Query().Get("q") != "":
				// ListFolder — return one file under the vault folder.
				jsonResponse(w, map[string]any{
					"files": []map[string]any{
						{
							"id":           "drive-only-id",
							"name":         "drive-only.md",
							"mimeType":     "text/plain",
							"modifiedTime": "2024-06-01T10:00:00Z",
							"md5Checksum":  "abc123",
						},
					},
				})
			default:
				// Folder creation / other GETs.
				jsonResponse(w, map[string]any{"id": "vault-folder-id", "files": []any{}})
			}
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetVaultFolderIDForTest(sm, "vault-folder-id")

	// Act
	sm.Reconcile(context.Background())
	sm.Shutdown()

	// Assert — Drive-only file downloaded to local vault.
	content, err := os.ReadFile(filepath.Join(vaultDir, "drive-only.md"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(content) != "from drive" {
		t.Errorf("want %q, got %q", "from drive", string(content))
	}
}

func TestSyncManager_Reconcile_SkipsDownloadForFilesAlreadyInState(t *testing.T) {
	// Arrange — Drive has a file that is already tracked in State.
	// Reconcile should neither re-upload nor re-download it.
	vaultDir := t.TempDir()
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFile("already-synced.md", drivesync.FileEntry{DriveID: "drive-synced-id"})

	downloadCalled := false
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				downloadCalled = true
				w.Write([]byte("should not download"))
				return
			}
			if r.Method == http.MethodGet && r.URL.Query().Get("q") != "" {
				jsonResponse(w, map[string]any{
					"files": []map[string]any{
						{
							"id":           "drive-synced-id",
							"name":         "already-synced.md",
							"mimeType":     "text/plain",
							"modifiedTime": "2024-06-01T10:00:00Z",
							"md5Checksum":  "abc",
						},
					},
				})
				return
			}
			jsonResponse(w, map[string]any{"id": "vault-folder-id", "files": []any{}})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, vaultDir, httpClient)
	drivesync.SetVaultFolderIDForTest(sm, "vault-folder-id")

	// Act
	sm.Reconcile(context.Background())
	sm.Shutdown()

	// Assert — no download attempted.
	if downloadCalled {
		t.Error("expected no download for file already tracked in state")
	}
}

func TestSyncManager_DownloadJob_SkipsWhenLocalWins(t *testing.T) {
	// Arrange — local file is newer; last_write_wins should keep local and skip download.
	vaultDir := t.TempDir()
	localFile := filepath.Join(vaultDir, "notes.md")
	if err := os.WriteFile(localFile, []byte("local content"), 0644); err != nil {
		t.Fatalf("writing local file: %v", err)
	}
	// Set local mod time to be 10 s in the future relative to Drive.
	driveTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	localTime := driveTime.Add(10 * time.Second)
	if err := os.Chtimes(localFile, localTime, localTime); err != nil {
		t.Fatalf("setting local mod time: %v", err)
	}

	downloadCalled := false
	db := openTestPebble(t)
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				downloadCalled = true
				w.Write([]byte("drive content"))
				return
			}
			jsonResponse(w, map[string]any{"files": []any{}})
		},
	})
	defer srv.Close()

	cfg := config.DriveSyncConfig{
		RootFolderName:   "autowiki",
		VaultFolderName:  "vault",
		ConflictStrategy: config.ConflictLastWriteWins,
	}
	sm := drivesync.NewWithHTTPClient(cfg, db, vaultDir, httpClient)
	sm.Reconcile(context.Background()) // start worker
	drivesync.EnqueueDownloadForTest(sm, "notes.md", "drive-file-id", vaultDir,
		driveTime.Format(time.RFC3339))
	sm.Shutdown()

	// Assert — local file untouched; download not called.
	if downloadCalled {
		t.Error("expected download to be skipped when local is newer")
	}
	content, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("reading local file: %v", err)
	}
	if string(content) != "local content" {
		t.Errorf("local file should be unchanged, got %q", string(content))
	}
}

func TestSyncManager_DownloadJob_RenamesLocalAndDownloadsOnConflict(t *testing.T) {
	// Arrange — both sides modified within 5 s (keep_both); Drive content should win
	// and the local file should be renamed to a conflict path.
	vaultDir := t.TempDir()
	localFile := filepath.Join(vaultDir, "notes.md")
	if err := os.WriteFile(localFile, []byte("local content"), 0644); err != nil {
		t.Fatalf("writing local file: %v", err)
	}
	driveTime := time.Date(2024, 1, 1, 12, 0, 3, 0, time.UTC) // 3 s after local
	localTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(localFile, localTime, localTime); err != nil {
		t.Fatalf("setting local mod time: %v", err)
	}

	db := openTestPebble(t)
	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "media" {
				w.Write([]byte("drive content"))
				return
			}
			jsonResponse(w, map[string]any{"files": []any{}})
		},
	})
	defer srv.Close()

	cfg := config.DriveSyncConfig{
		RootFolderName:   "autowiki",
		VaultFolderName:  "vault",
		ConflictStrategy: config.ConflictKeepBoth,
	}
	sm := drivesync.NewWithHTTPClient(cfg, db, vaultDir, httpClient)
	sm.Reconcile(context.Background())
	drivesync.EnqueueDownloadForTest(sm, "notes.md", "drive-file-id", vaultDir,
		driveTime.Format(time.RFC3339))
	sm.Shutdown()

	// Assert — Drive content written to notes.md.
	content, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatalf("reading notes.md: %v", err)
	}
	if string(content) != "drive content" {
		t.Errorf("want drive content in notes.md, got %q", string(content))
	}

	// Assert — conflict file created with local content.
	entries, err := os.ReadDir(vaultDir)
	if err != nil {
		t.Fatalf("reading vaultDir: %v", err)
	}
	var conflictFile string
	for _, e := range entries {
		if e.Name() != "notes.md" {
			conflictFile = e.Name()
		}
	}
	if conflictFile == "" {
		t.Fatal("expected a conflict file to be created")
	}
	conflictContent, err := os.ReadFile(filepath.Join(vaultDir, conflictFile))
	if err != nil {
		t.Fatalf("reading conflict file: %v", err)
	}
	if string(conflictContent) != "local content" {
		t.Errorf("conflict file should contain local content, got %q", string(conflictContent))
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
	sm.Reconcile(context.Background()) // starts worker on empty vault
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
	sm.Reconcile(context.Background())
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
	sm.Reconcile(context.Background()) // starts worker on empty vault
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

func TestSyncManager_Start_PreservesExistingPageToken(t *testing.T) {
	// Arrange — state already has a page token from a previous run.
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetPageToken("existing-token")

	srv, httpClient := newFakeDrive(t, map[string]http.HandlerFunc{
		"/drive/v3/files": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				jsonResponse(w, map[string]any{"files": []any{}})
				return
			}
			jsonResponse(w, map[string]any{"id": "folder-id"})
		},
		"/drive/v3/changes/startPageToken": func(w http.ResponseWriter, _ *http.Request) {
			jsonResponse(w, map[string]any{"startPageToken": "fresh-token"})
		},
	})
	defer srv.Close()

	sm := drivesync.NewWithHTTPClient(testCfg, db, t.TempDir(), httpClient)
	drivesync.SetWatcherDebounce(sm, 20*time.Millisecond)
	sm.Start(context.Background())
	sm.Shutdown()

	// Assert — existing token is preserved; the fresh one from Drive is ignored.
	tok, err := st.GetPageToken()
	if err != nil {
		t.Fatalf("GetPageToken: %v", err)
	}
	if tok != "existing-token" {
		t.Errorf("want existing-token, got %q", tok)
	}
}

func TestSyncManager_Reconcile_SkipsUploadForFilesAlreadyInState(t *testing.T) {
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
	sm.Reconcile(context.Background())
	sm.Shutdown()

	// Assert — no upload API calls made.
	if apiCallCount > 0 {
		t.Errorf("expected 0 API calls, got %d", apiCallCount)
	}
}
