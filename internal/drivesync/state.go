package drivesync

import (
	"encoding/json"
	"errors"

	"github.com/cockroachdb/pebble"
)

// FileEntry holds the Drive sync metadata for a single vault file.
type FileEntry struct {
	DriveID      string `json:"driveID"`
	MD5          string `json:"md5"`
	DriveModTime string `json:"driveModTime"`
	LocalModTime string `json:"localModTime"`
}

const (
	keyPageToken    = "drive_sync:page_token"
	keyRootFolderID = "drive_sync:root_folder_id"
	keyLastSync     = "drive_sync:last_vault_sync"
	keyLastError    = "drive_sync:last_error"
	prefixFolders   = "drive_sync:folders:"
	prefixFiles     = "drive_sync:files:"
)

// State persists Drive sync metadata in Pebble.
type State struct {
	db *pebble.DB
}

// NewState constructs a State using an existing Pebble handle.
func NewState(db *pebble.DB) *State {
	return &State{db: db}
}

func (s *State) get(key string) (string, error) {
	val, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer closer.Close()
	return string(val), nil
}

func (s *State) set(key, value string) error {
	return s.db.Set([]byte(key), []byte(value), pebble.Sync)
}

func (s *State) delete(key string) error {
	return s.db.Delete([]byte(key), pebble.Sync)
}

// GetPageToken returns the stored Drive changes page token, or "" if unset.
func (s *State) GetPageToken() (string, error) { return s.get(keyPageToken) }

// SetPageToken stores the Drive changes page token.
func (s *State) SetPageToken(tok string) error { return s.set(keyPageToken, tok) }

// GetRootFolderID returns the Drive ID of the root vault folder, or "" if unset.
func (s *State) GetRootFolderID() (string, error) { return s.get(keyRootFolderID) }

// SetRootFolderID stores the Drive ID of the root vault folder.
func (s *State) SetRootFolderID(id string) error { return s.set(keyRootFolderID, id) }

// GetFolder returns the Drive folder ID for the given vault-relative path, or "" if unset.
func (s *State) GetFolder(relPath string) (string, error) {
	return s.get(prefixFolders + relPath)
}

// SetFolder stores the Drive folder ID for the given vault-relative path.
func (s *State) SetFolder(relPath, folderID string) error {
	return s.set(prefixFolders+relPath, folderID)
}

// DeleteFolder removes the folder entry for the given vault-relative path.
func (s *State) DeleteFolder(relPath string) error {
	return s.delete(prefixFolders + relPath)
}

// GetFile returns the FileEntry for the given vault-relative path, or nil if unset.
func (s *State) GetFile(relPath string) (*FileEntry, error) {
	raw, err := s.get(prefixFiles + relPath)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var entry FileEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

// SetFile stores the FileEntry for the given vault-relative path.
func (s *State) SetFile(relPath string, entry FileEntry) error {
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return s.set(prefixFiles+relPath, string(b))
}

// DeleteFile removes the file entry for the given vault-relative path.
func (s *State) DeleteFile(relPath string) error {
	return s.delete(prefixFiles + relPath)
}

// SetLastVaultSync stores an RFC3339 timestamp of the last successful sync operation.
func (s *State) SetLastVaultSync(ts string) error { return s.set(keyLastSync, ts) }

// GetLastVaultSync returns the stored timestamp, or "" if unset.
func (s *State) GetLastVaultSync() (string, error) { return s.get(keyLastSync) }

// SetLastError stores the last sync error string; pass "" to clear.
func (s *State) SetLastError(msg string) error { return s.set(keyLastError, msg) }

// GetLastError returns the last sync error string, or "" if none.
func (s *State) GetLastError() (string, error) { return s.get(keyLastError) }
