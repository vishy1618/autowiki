package drivesync_test

import (
	"testing"

	"github.com/suvish/autowiki/internal/drivesync"
)

// --- PageToken ---

func TestState_PageToken_SetAndGet(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	if err := st.SetPageToken("tok-99"); err != nil {
		t.Fatalf("SetPageToken: %v", err)
	}
	got, err := st.GetPageToken()

	// Assert
	if err != nil {
		t.Fatalf("GetPageToken: %v", err)
	}
	if got != "tok-99" {
		t.Errorf("want %q, got %q", "tok-99", got)
	}
}

func TestState_PageToken_GetWhenNotSet_ReturnsEmpty(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	got, err := st.GetPageToken()

	// Assert
	if err != nil {
		t.Fatalf("GetPageToken: %v", err)
	}
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

// --- Folders ---

func TestState_Folder_SetGetDelete(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act — set
	if err := st.SetFolder("notes/work", "folder-id-abc"); err != nil {
		t.Fatalf("SetFolder: %v", err)
	}
	got, err := st.GetFolder("notes/work")

	// Assert — set
	if err != nil {
		t.Fatalf("GetFolder: %v", err)
	}
	if got != "folder-id-abc" {
		t.Errorf("want %q, got %q", "folder-id-abc", got)
	}

	// Act — delete
	if err := st.DeleteFolder("notes/work"); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	got, err = st.GetFolder("notes/work")

	// Assert — deleted
	if err != nil {
		t.Fatalf("GetFolder after delete: %v", err)
	}
	if got != "" {
		t.Errorf("want empty after delete, got %q", got)
	}
}

func TestState_Folder_GetWhenNotSet_ReturnsEmpty(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	got, err := st.GetFolder("missing/path")

	// Assert
	if err != nil {
		t.Fatalf("GetFolder: %v", err)
	}
	if got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

// --- Files ---

func TestState_File_SetGetDelete(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	entry := drivesync.FileEntry{
		DriveID:       "file-id-123",
		MD5:           "abc123",
		DriveModTime:  "2024-01-01T00:00:00Z",
		LocalModTime:  "2024-01-01T01:00:00Z",
	}

	// Act — set
	if err := st.SetFile("notes/todo.md", entry); err != nil {
		t.Fatalf("SetFile: %v", err)
	}
	got, err := st.GetFile("notes/todo.md")

	// Assert — set
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got == nil {
		t.Fatal("want entry, got nil")
	}
	if got.DriveID != entry.DriveID {
		t.Errorf("DriveID: want %q, got %q", entry.DriveID, got.DriveID)
	}

	// Act — delete
	if err := st.DeleteFile("notes/todo.md"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	got, err = st.GetFile("notes/todo.md")

	// Assert — deleted
	if err != nil {
		t.Fatalf("GetFile after delete: %v", err)
	}
	if got != nil {
		t.Errorf("want nil after delete, got %+v", got)
	}
}

func TestState_File_GetWhenNotSet_ReturnsNil(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	got, err := st.GetFile("missing.md")

	// Assert
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

// --- Reverse lookups ---

func TestState_GetFolderByDriveID_ReturnsRelPathAfterSetFolder(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFolder("notes/work", "folder-drive-id")

	// Act
	relPath, err := st.GetFolderByDriveID("folder-drive-id")

	// Assert
	if err != nil {
		t.Fatalf("GetFolderByDriveID: %v", err)
	}
	if relPath != "notes/work" {
		t.Errorf("want %q, got %q", "notes/work", relPath)
	}
}

func TestState_GetFolderByDriveID_ReturnsEmptyWhenNotFound(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	relPath, err := st.GetFolderByDriveID("unknown-id")

	// Assert
	if err != nil {
		t.Fatalf("GetFolderByDriveID: %v", err)
	}
	if relPath != "" {
		t.Errorf("want empty, got %q", relPath)
	}
}

func TestState_GetFileByDriveID_ReturnsRelPathAfterSetFile(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)
	_ = st.SetFile("notes/todo.md", drivesync.FileEntry{DriveID: "file-drive-id"})

	// Act
	relPath, err := st.GetFileByDriveID("file-drive-id")

	// Assert
	if err != nil {
		t.Fatalf("GetFileByDriveID: %v", err)
	}
	if relPath != "notes/todo.md" {
		t.Errorf("want %q, got %q", "notes/todo.md", relPath)
	}
}

func TestState_GetFileByDriveID_ReturnsEmptyWhenNotFound(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	relPath, err := st.GetFileByDriveID("unknown-id")

	// Assert
	if err != nil {
		t.Fatalf("GetFileByDriveID: %v", err)
	}
	if relPath != "" {
		t.Errorf("want empty, got %q", relPath)
	}
}

// --- LastVaultSync / LastError ---

func TestState_SetLastVaultSync_CanBeRetrieved(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	if err := st.SetLastVaultSync("2024-06-01T12:00:00Z"); err != nil {
		t.Fatalf("SetLastVaultSync: %v", err)
	}
	got, err := st.GetLastVaultSync()

	// Assert
	if err != nil {
		t.Fatalf("GetLastVaultSync: %v", err)
	}
	if got != "2024-06-01T12:00:00Z" {
		t.Errorf("want %q, got %q", "2024-06-01T12:00:00Z", got)
	}
}

func TestState_SetLastError_CanBeRetrieved(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	if err := st.SetLastError("something went wrong"); err != nil {
		t.Fatalf("SetLastError: %v", err)
	}
	got, err := st.GetLastError()

	// Assert
	if err != nil {
		t.Fatalf("GetLastError: %v", err)
	}
	if got != "something went wrong" {
		t.Errorf("want %q, got %q", "something went wrong", got)
	}
}

// --- RootFolderID ---

func TestState_RootFolderID_SetAndGet(t *testing.T) {
	// Arrange
	db := openTestPebble(t)
	st := drivesync.NewState(db)

	// Act
	if err := st.SetRootFolderID("root-folder-xyz"); err != nil {
		t.Fatalf("SetRootFolderID: %v", err)
	}
	got, err := st.GetRootFolderID()

	// Assert
	if err != nil {
		t.Fatalf("GetRootFolderID: %v", err)
	}
	if got != "root-folder-xyz" {
		t.Errorf("want %q, got %q", "root-folder-xyz", got)
	}
}
