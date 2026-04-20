package drivesync

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// DriveClient wraps the Google Drive v3 API.
type DriveClient struct {
	svc *drive.Service
}

// NewDriveClient constructs a DriveClient using the given HTTP client.
// In production, pass an oauth2-authenticated client built from a
// PersistingTokenSource. In tests, pass a fake HTTP client.
func NewDriveClient(ctx context.Context, httpClient *http.Client) (*DriveClient, error) {
	svc, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("creating drive service: %w", err)
	}
	return &DriveClient{svc: svc}, nil
}

// EnsureFolder finds or creates a Drive folder with the given name under
// parentID (use "" for My Drive root). Returns the folder's Drive ID.
func (c *DriveClient) EnsureFolder(name, parentID string) (string, error) {
	parent := parentID
	if parent == "" {
		parent = "root"
	}

	q := fmt.Sprintf(
		"mimeType = 'application/vnd.google-apps.folder' and name = %q and %q in parents and trashed = false",
		name, parent,
	)
	list, err := c.svc.Files.List().Q(q).Fields("files(id,name)").Do()
	if err != nil {
		return "", fmt.Errorf("listing folders: %w", err)
	}
	if len(list.Files) > 0 {
		return list.Files[0].Id, nil
	}

	f := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parent},
	}
	created, err := c.svc.Files.Create(f).Fields("id").Do()
	if err != nil {
		return "", fmt.Errorf("creating folder: %w", err)
	}
	return created.Id, nil
}

// EnsureFolderPath resolves or creates every segment of relDir under rootFolderID.
// It checks State before making API calls and writes new IDs back to State.
// Returns the Drive ID of the deepest folder.
func (c *DriveClient) EnsureFolderPath(relDir, rootFolderID string, state *State) (string, error) {
	// Fast path: full relDir already cached.
	if id, err := state.GetFolder(relDir); err == nil && id != "" {
		return id, nil
	}

	segments := strings.Split(relDir, "/")
	currentParent := rootFolderID
	currentPath := ""
	for i, seg := range segments {
		if i > 0 {
			currentPath += "/"
		}
		currentPath += seg

		if id, err := state.GetFolder(currentPath); err == nil && id != "" {
			currentParent = id
			continue
		}

		id, err := c.EnsureFolder(seg, currentParent)
		if err != nil {
			return "", fmt.Errorf("ensuring folder %q: %w", currentPath, err)
		}
		if err := state.SetFolder(currentPath, id); err != nil {
			return "", fmt.Errorf("caching folder %q: %w", currentPath, err)
		}
		currentParent = id
	}
	return currentParent, nil
}

// Upload creates or updates a file in Drive under parentFolderID.
// relPath is used as the Drive file name. Returns the Drive file ID.
func (c *DriveClient) Upload(relPath, localPath, parentFolderID string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", localPath, err)
	}
	defer f.Close()

	name := filepath.Base(relPath)

	// Check for existing file to update instead of creating a duplicate.
	q := fmt.Sprintf("name = %q and %q in parents and trashed = false", name, parentFolderID)
	list, err := c.svc.Files.List().Q(q).Fields("files(id)").Do()
	if err != nil {
		return "", fmt.Errorf("listing files for upload: %w", err)
	}

	if len(list.Files) > 0 {
		updated, err := c.svc.Files.Update(list.Files[0].Id, &drive.File{}).
			Media(f).Fields("id").Do()
		if err != nil {
			return "", fmt.Errorf("updating file: %w", err)
		}
		return updated.Id, nil
	}

	meta := &drive.File{
		Name:    name,
		Parents: []string{parentFolderID},
	}
	created, err := c.svc.Files.Create(meta).Media(f).Fields("id").Do()
	if err != nil {
		return "", fmt.Errorf("uploading file: %w", err)
	}
	return created.Id, nil
}

// Trash moves the given Drive file to the trash.
func (c *DriveClient) Trash(driveFileID string) error {
	_, err := c.svc.Files.Update(driveFileID, &drive.File{Trashed: true}).Fields("id").Do()
	if err != nil {
		return fmt.Errorf("trashing file %s: %w", driveFileID, err)
	}
	return nil
}

// GetStartPageToken returns the current start page token for Drive changes,
// used to initialise polling in US-17.
func (c *DriveClient) GetStartPageToken() (string, error) {
	res, err := c.svc.Changes.GetStartPageToken().Do()
	if err != nil {
		return "", fmt.Errorf("getting start page token: %w", err)
	}
	return res.StartPageToken, nil
}
