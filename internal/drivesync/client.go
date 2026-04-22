package drivesync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
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
// Returns the Drive file ID, MD5 checksum, and modifiedTime from Drive's response.
func (c *DriveClient) Upload(relPath, localPath, parentFolderID string) (driveID, md5, modifiedTime string, err error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", "", "", fmt.Errorf("opening %s: %w", localPath, err)
	}
	defer f.Close()

	name := filepath.Base(relPath)
	const fields googleapi.Field = "id,md5Checksum,modifiedTime"

	q := fmt.Sprintf("name = %q and %q in parents and trashed = false", name, parentFolderID)
	list, err := c.svc.Files.List().Q(q).Fields("files(id)").Do()
	if err != nil {
		return "", "", "", fmt.Errorf("listing files for upload: %w", err)
	}

	if len(list.Files) > 0 {
		updated, err := c.svc.Files.Update(list.Files[0].Id, &drive.File{}).
			Media(f).Fields(fields).Do()
		if err != nil {
			return "", "", "", fmt.Errorf("updating file: %w", err)
		}
		return updated.Id, updated.Md5Checksum, updated.ModifiedTime, nil
	}

	meta := &drive.File{Name: name, Parents: []string{parentFolderID}}
	created, err := c.svc.Files.Create(meta).Media(f).Fields(fields).Do()
	if err != nil {
		return "", "", "", fmt.Errorf("uploading file: %w", err)
	}
	return created.Id, created.Md5Checksum, created.ModifiedTime, nil
}

// Download retrieves a Drive file by ID and writes it to localPath,
// creating parent directories as needed.
func (c *DriveClient) Download(driveFileID, localPath string) error {
	resp, err := c.svc.Files.Get(driveFileID).Download()
	if err != nil {
		return fmt.Errorf("downloading file %s: %w", driveFileID, err)
	}
	defer resp.Body.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("creating directories for %s: %w", localPath, err)
	}
	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("creating local file %s: %w", localPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("writing file %s: %w", localPath, err)
	}
	return nil
}

// Trash moves the given Drive file to the trash.
func (c *DriveClient) Trash(driveFileID string) error {
	_, err := c.svc.Files.Update(driveFileID, &drive.File{Trashed: true}).Fields("id").Do()
	if err != nil {
		return fmt.Errorf("trashing file %s: %w", driveFileID, err)
	}
	return nil
}

// DriveFile holds the Drive metadata for a single file returned by ListFolder.
type DriveFile struct {
	ID           string
	RelPath      string
	ModifiedTime string
	MD5          string
}

// ListFolder lists all non-trashed files recursively under folderID.
// RelPath is relative to folderID (e.g. "notes/meeting.md").
// Used by US-17 download reconciliation.
func (c *DriveClient) ListFolder(folderID string) ([]DriveFile, error) {
	return c.listFolderRecursive(folderID, "")
}

func (c *DriveClient) listFolderRecursive(folderID, prefix string) ([]DriveFile, error) {
	q := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	var result []DriveFile
	var pageToken string

	for {
		call := c.svc.Files.List().
			Q(q).
			Fields("nextPageToken, files(id,name,mimeType,modifiedTime,md5Checksum)")
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		list, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("listing folder %s: %w", folderID, err)
		}

		for _, f := range list.Files {
			relPath := f.Name
			if prefix != "" {
				relPath = prefix + "/" + f.Name
			}
			if f.MimeType == "application/vnd.google-apps.folder" {
				children, err := c.listFolderRecursive(f.Id, relPath)
				if err != nil {
					return nil, err
				}
				result = append(result, children...)
			} else {
				result = append(result, DriveFile{
					ID:           f.Id,
					RelPath:      relPath,
					ModifiedTime: f.ModifiedTime,
					MD5:          f.Md5Checksum,
				})
			}
		}

		pageToken = list.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return result, nil
}

// GetStartPageToken returns the current start page token for Drive changes.
func (c *DriveClient) GetStartPageToken() (string, error) {
	res, err := c.svc.Changes.GetStartPageToken().Do()
	if err != nil {
		return "", fmt.Errorf("getting start page token: %w", err)
	}
	return res.StartPageToken, nil
}

// DriveChange describes a single change returned by the Drive changes API.
// Name and ParentID are empty for permanently deleted files (Removed=true with no metadata).
type DriveChange struct {
	FileID       string
	Removed      bool
	Name         string
	ParentID     string
	ModifiedTime string
	MD5          string
}

// ListChanges fetches all changes since pageToken, handling pagination internally.
// Returns the full list of changes and the newStartPageToken to use on the next poll.
func (c *DriveClient) ListChanges(pageToken string) ([]DriveChange, string, error) {
	var result []DriveChange
	var newStartToken string
	curToken := pageToken

	for {
		list, err := c.svc.Changes.List(curToken).
			Fields("nextPageToken, newStartPageToken, changes(fileId,removed,file(name,parents,mimeType,modifiedTime,md5Checksum,trashed))").
			Do()
		if err != nil {
			return nil, "", fmt.Errorf("listing changes: %w", err)
		}

		for _, ch := range list.Changes {
			if ch.File != nil && ch.File.MimeType == "application/vnd.google-apps.folder" {
				continue // folder changes are handled imperatively via EnsureFolderPath
			}
			dc := DriveChange{FileID: ch.FileId, Removed: ch.Removed}
			if ch.File != nil && !ch.File.Trashed {
				dc.Name = ch.File.Name
				dc.ModifiedTime = ch.File.ModifiedTime
				dc.MD5 = ch.File.Md5Checksum
				if len(ch.File.Parents) > 0 {
					dc.ParentID = ch.File.Parents[0]
				}
			}
			result = append(result, dc)
		}

		if list.NewStartPageToken != "" {
			newStartToken = list.NewStartPageToken
		}
		if list.NextPageToken == "" {
			break
		}
		curToken = list.NextPageToken
	}
	return result, newStartToken, nil
}
