package drivesync

import (
	"context"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/suvish/autowiki/internal/store"
)

// PollOnceForTest triggers one synchronous poll cycle.
func PollOnceForTest(sm *SyncManager, ctx context.Context) {
	sm.pollOnce(ctx)
}

// SetVaultFolderIDForTest sets the cached vault folder ID on a SyncManager.
// Used to test resolveRelPath without a full Start() cycle.
func SetVaultFolderIDForTest(sm *SyncManager, id string) {
	sm.vaultFolderID = id
}

// ResolveRelPathForTest calls the private resolveRelPath method.
func ResolveRelPathForTest(sm *SyncManager, change DriveChange) (string, bool) {
	return sm.resolveRelPath(change)
}

// EnqueueDownloadForTest injects a download job directly into the sync worker channel.
// driveModTime is the RFC3339 modifiedTime from Drive (used for conflict resolution).
func EnqueueDownloadForTest(sm *SyncManager, relPath, driveFileID, vaultPath, driveModTime string) {
	localPath := filepath.Join(vaultPath, relPath)
	sm.syncCh <- syncJob{download: true, relPath: relPath, driveFileID: driveFileID, localPath: localPath, driveModTime: driveModTime}
}

// EnqueueTrashForTest injects a trash job directly into the upload worker channel.
// Only valid after the worker has been started (e.g. via Reconcile or Start).
func EnqueueTrashForTest(sm *SyncManager, relPath, driveID string) {
	sm.syncCh <- syncJob{isTrash: true, relPath: relPath, driveID: driveID}
}

// NewWatcherForTest creates a Watcher with a configurable debounce duration.
func NewWatcherForTest(root string, debounce time.Duration) (*Watcher, error) {
	return newWatcherWithDebounce(root, debounce)
}

// SetWatcherDebounce overrides the debounce duration used when SyncManager.Start
// creates its internal Watcher. Call before Start().
func SetWatcherDebounce(sm *SyncManager, d time.Duration) {
	sm.watcherDebounce = d
}

// TriggerNewDirForTest calls handleNewDir directly, bypassing fsnotify.
// Use to test synthetic-event logic with a fully populated directory.
func TriggerNewDirForTest(w *Watcher, dirPath string) {
	w.handleNewDir(dirPath)
}

// NewWithTokenStore creates a minimal SyncManager for testing Status().
func NewWithTokenStore(db *pebble.DB, tokenStore store.DriveTokenStore) *SyncManager {
	return &SyncManager{
		state:      NewState(db),
		tokenStore: tokenStore,
		syncCh:     make(chan syncJob, 1),
	}
}
