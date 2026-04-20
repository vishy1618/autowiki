package drivesync

// EnqueueTrashForTest injects a trash job directly into the upload worker channel.
// Only valid after the worker has been started (e.g. via ReconcileUpload or Start).
func EnqueueTrashForTest(sm *SyncManager, relPath, driveID string) {
	sm.uploadCh <- uploadJob{isTrash: true, relPath: relPath, driveID: driveID}
}
