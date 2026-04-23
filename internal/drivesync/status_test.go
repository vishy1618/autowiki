package drivesync_test

import (
	"testing"

	"github.com/suvish/autowiki/internal/drivesync"
	"github.com/suvish/autowiki/internal/store"
)

func TestSyncManager_Status_EnabledAlwaysTrue(t *testing.T) {
	sm := drivesync.NewWithTokenStore(openTestPebble(t), store.NewMemStore())
	if !sm.Status().Enabled {
		t.Error("want Enabled=true; SyncManager only exists when drive sync is enabled")
	}
}

func TestSyncManager_Status_NotConnectedWhenNoToken(t *testing.T) {
	// Arrange — empty token store
	sm := drivesync.NewWithTokenStore(openTestPebble(t), store.NewMemStore())

	// Act
	status := sm.Status()

	// Assert
	if status.Connected {
		t.Error("want Connected=false when token store is empty")
	}
}

func TestSyncManager_Status_LastVaultSyncNonNilWhenSet(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetLastVaultSync("2026-04-22T10:00:00Z")
	sm := drivesync.NewWithTokenStore(db, store.NewMemStore())

	// Act
	status := sm.Status()

	// Assert
	if status.LastVaultSync == nil {
		t.Fatal("want non-nil LastVaultSync when state has a value")
	}
	if *status.LastVaultSync != "2026-04-22T10:00:00Z" {
		t.Errorf("want %q, got %q", "2026-04-22T10:00:00Z", *status.LastVaultSync)
	}
}

func TestSyncManager_Status_LastVaultSyncNilWhenNotSet(t *testing.T) {
	// Arrange — state has no LastVaultSync
	sm := drivesync.NewWithTokenStore(openTestPebble(t), store.NewMemStore())

	// Act
	status := sm.Status()

	// Assert
	if status.LastVaultSync != nil {
		t.Errorf("want nil LastVaultSync when state is empty, got %q", *status.LastVaultSync)
	}
}

func TestSyncManager_Status_LastErrorNonNilWhenSet(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetLastError("something went wrong")
	sm := drivesync.NewWithTokenStore(db, store.NewMemStore())

	// Act
	status := sm.Status()

	// Assert
	if status.LastError == nil {
		t.Fatal("want non-nil LastError when state has an error")
	}
	if *status.LastError != "something went wrong" {
		t.Errorf("want %q, got %q", "something went wrong", *status.LastError)
	}
}

func TestSyncManager_Status_LastErrorNilWhenEmpty(t *testing.T) {
	// Arrange — no error stored
	sm := drivesync.NewWithTokenStore(openTestPebble(t), store.NewMemStore())

	// Act
	status := sm.Status()

	// Assert
	if status.LastError != nil {
		t.Errorf("want nil LastError when no error stored, got %q", *status.LastError)
	}
}

func TestSyncManager_Status_ConnectedWhenTokenPresent(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	ts := store.NewMemStore()
	_ = ts.SetDriveToken("refresh-token")

	sm := drivesync.NewWithTokenStore(db, ts)

	// Act
	status := sm.Status()

	// Assert
	if !status.Connected {
		t.Error("want Connected=true when token store has a token")
	}
}
