package drivesync

import "time"

// EnqueueTrashForTest injects a trash job directly into the upload worker channel.
// Only valid after the worker has been started (e.g. via ReconcileUpload or Start).
func EnqueueTrashForTest(sm *SyncManager, relPath, driveID string) {
	sm.uploadCh <- uploadJob{isTrash: true, relPath: relPath, driveID: driveID}
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
