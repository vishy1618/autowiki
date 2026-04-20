package drivesync

import (
	"context"
	"fmt"
	"net/http"

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

// GetStartPageToken returns the current start page token for Drive changes,
// used to initialise polling in US-17.
func (c *DriveClient) GetStartPageToken() (string, error) {
	res, err := c.svc.Changes.GetStartPageToken().Do()
	if err != nil {
		return "", fmt.Errorf("getting start page token: %w", err)
	}
	return res.StartPageToken, nil
}
