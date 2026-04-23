package drivesync

// DriveStatus is the snapshot returned by SyncManager.Status().
type DriveStatus struct {
	Enabled          bool
	Connected        bool
	LastVaultSync    *string
	LastPebbleBackup *string
	LastError        *string
}

// StatusProvider is implemented by SyncManager and consumed by the HTTP handler.
type StatusProvider interface {
	Status() DriveStatus
}

// Status returns a point-in-time snapshot of the sync state.
func (sm *SyncManager) Status() DriveStatus {
	s := DriveStatus{Enabled: true}
	if sm.tokenStore != nil {
		token, _ := sm.tokenStore.GetDriveToken()
		s.Connected = token != ""
	}
	if v, _ := sm.state.GetLastVaultSync(); v != "" {
		s.LastVaultSync = &v
	}
	if v, _ := sm.state.GetLastError(); v != "" {
		s.LastError = &v
	}
	return s
}
