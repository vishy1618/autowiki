package drivesync

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"

	"github.com/cockroachdb/pebble"
	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/store"
)

type uploadJob struct {
	relPath   string
	localPath string
	parentID  string
	isTrash   bool
	driveID   string // set for trash jobs
}

// SyncManager orchestrates vault→Drive upload. Constructed via New().
type SyncManager struct {
	cfg             config.DriveSyncConfig
	oauthCfg        *oauth2.Config
	tokenStore      store.DriveTokenStore
	driveClient     *DriveClient // nil until Start() builds it (or NewWithHTTPClient sets it)
	state           *State
	vaultPath       string
	uploadCh        chan uploadJob
	wg              sync.WaitGroup
	workerOnce      sync.Once
	shutdownOnce    sync.Once
	watcher         *Watcher
	watcherDone     chan struct{}
	watcherDebounce time.Duration // 0 → use default (2s)
}

// New returns a SyncManager ready to Start. The DriveClient is built lazily
// inside Start() once the refresh token is confirmed present.
func New(cfg config.DriveSyncConfig, clientID, clientSecret string, db *pebble.DB, vaultPath string, tokenStore store.DriveTokenStore) *SyncManager {
	oauthCfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drive.DriveFileScope},
	}
	return &SyncManager{
		cfg:        cfg,
		oauthCfg:   oauthCfg,
		tokenStore: tokenStore,
		state:      NewState(db),
		vaultPath:  vaultPath,
		uploadCh:   make(chan uploadJob, 200),
	}
}

// NewWithHTTPClient constructs a SyncManager with a pre-built HTTP client,
// bypassing OAuth. The DriveClient is constructed immediately. Intended for tests.
func NewWithHTTPClient(cfg config.DriveSyncConfig, db *pebble.DB, vaultPath string, httpClient *http.Client) *SyncManager {
	dc, _ := NewDriveClient(context.Background(), httpClient)
	return &SyncManager{
		cfg:         cfg,
		driveClient: dc,
		state:       NewState(db),
		vaultPath:   vaultPath,
		uploadCh:    make(chan uploadJob, 200),
	}
}

// Start checks for a stored refresh token if no DriveClient is set yet; if
// absent it logs and returns. Otherwise it creates the vault Drive folder,
// stores sync metadata, runs initial reconciliation, and launches the upload worker.
func (sm *SyncManager) Start(ctx context.Context) {
	if sm.driveClient == nil {
		refreshToken, err := sm.tokenStore.GetDriveToken()
		if err != nil {
			slog.Error("drive sync: reading refresh token", "err", err)
			return
		}
		if refreshToken == "" {
			slog.Warn("drive sync: no refresh token — sign out and back in to grant Drive access")
			return
		}
		httpClient := sm.buildHTTPClient(ctx, refreshToken)
		sm.driveClient, err = NewDriveClient(ctx, httpClient)
		if err != nil {
			slog.Error("drive sync: creating drive client", "err", err)
			return
		}
	}

	sm.startWorkerOnce()

	rootID, err := sm.driveClient.EnsureFolder(sm.cfg.RootFolderName, "")
	if err != nil {
		slog.Error("drive sync: ensuring root folder", "err", err)
		return
	}
	slog.Info("drive sync: root folder ready", "folderID", rootID)

	vaultID, err := sm.driveClient.EnsureFolder(sm.cfg.VaultFolderName, rootID)
	if err != nil {
		slog.Error("drive sync: ensuring vault folder", "err", err)
		return
	}
	slog.Info("drive sync: vault folder ready", "folderID", vaultID)

	if err := sm.state.SetRootFolderID(vaultID); err != nil {
		slog.Error("drive sync: storing vault folder ID", "err", err)
		return
	}

	pageToken, err := sm.driveClient.GetStartPageToken()
	if err != nil {
		slog.Error("drive sync: getting start page token", "err", err)
		return
	}
	if err := sm.state.SetPageToken(pageToken); err != nil {
		slog.Error("drive sync: storing page token", "err", err)
		return
	}
	slog.Info("drive sync: start page token stored", "token", pageToken)

	sm.reconcileUpload(ctx)

	debounce := sm.watcherDebounce
	if debounce == 0 {
		debounce = 2 * time.Second
	}
	w, err := newWatcherWithDebounce(sm.vaultPath, debounce)
	if err != nil {
		slog.Error("drive sync: starting watcher", "err", err)
		return
	}
	sm.watcher = w
	sm.watcherDone = make(chan struct{})

	go func() {
		defer close(sm.watcherDone)
		for event := range w.Events() {
			sm.handleFileEvent(event)
		}
	}()
}

// ReconcileUpload walks the vault and uploads any files not yet tracked in State.
// It is a no-op if the DriveClient is not connected. Idempotent: safe to call again
// after a partial failure.
func (sm *SyncManager) ReconcileUpload(ctx context.Context) {
	if sm.driveClient == nil {
		slog.Warn("drive sync: not connected, skipping reconcile")
		return
	}
	sm.startWorkerOnce()
	sm.reconcileUpload(ctx)
}

func (sm *SyncManager) reconcileUpload(ctx context.Context) {
	vaultFolderID, err := sm.state.GetRootFolderID()
	if err != nil {
		slog.Error("drive sync: reading vault folder ID", "err", err)
		return
	}

	slog.Info("drive sync: reconcile started")
	err = filepath.WalkDir(sm.vaultPath, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(sm.vaultPath, path)
		if err != nil {
			return nil
		}

		existing, err := sm.state.GetFile(relPath)
		if err != nil || existing != nil {
			return nil // skip files already tracked or unreadable
		}

		dir := filepath.Dir(relPath)
		parentID := vaultFolderID
		if dir != "." && vaultFolderID != "" {
			parentID, err = sm.driveClient.EnsureFolderPath(filepath.ToSlash(dir), vaultFolderID, sm.state)
			if err != nil {
				slog.Error("drive sync: ensuring folder path", "path", dir, "err", err)
				return nil
			}
		}

		relPathSlash := filepath.ToSlash(relPath)
		slog.Info("drive sync: queuing reconcile upload", "path", relPathSlash)
		sm.uploadCh <- uploadJob{
			relPath:   relPathSlash,
			localPath: path,
			parentID:  parentID,
		}
		return nil
	})
	if err != nil {
		slog.Error("drive sync: walking vault", "err", err)
	}
}

// Shutdown stops the watcher (if running), drains the upload worker, and returns.
// Safe to call multiple times.
func (sm *SyncManager) Shutdown() {
	if sm.watcher != nil {
		sm.watcher.Close()
		<-sm.watcherDone
	}
	sm.shutdownOnce.Do(func() { close(sm.uploadCh) })
	sm.wg.Wait()
}

func (sm *SyncManager) handleFileEvent(event FileEvent) {
	slog.Info("drive sync: watcher event", "path", event.RelPath, "op", event.Op)

	if event.Op == OpRemove || event.Op == OpRename {
		existing, err := sm.state.GetFile(event.RelPath)
		if err != nil || existing == nil {
			return // not tracked — nothing to trash
		}
		sm.uploadCh <- uploadJob{isTrash: true, relPath: event.RelPath, driveID: existing.DriveID}
		return
	}

	vaultFolderID, err := sm.state.GetRootFolderID()
	if err != nil {
		slog.Error("drive sync: reading vault folder ID for watcher event", "err", err)
		return
	}

	localPath := filepath.Join(sm.vaultPath, filepath.FromSlash(event.RelPath))
	dir := filepath.Dir(event.RelPath)
	parentID := vaultFolderID
	if dir != "." && vaultFolderID != "" {
		parentID, err = sm.driveClient.EnsureFolderPath(dir, vaultFolderID, sm.state)
		if err != nil {
			slog.Error("drive sync: ensuring folder path for watcher event", "path", dir, "err", err)
			return
		}
	}

	sm.uploadCh <- uploadJob{
		relPath:   event.RelPath,
		localPath: localPath,
		parentID:  parentID,
	}
}

func (sm *SyncManager) startWorkerOnce() {
	sm.workerOnce.Do(func() {
		sm.wg.Add(1)
		go func() {
			defer sm.wg.Done()
			for job := range sm.uploadCh {
				sm.processJob(job)
			}
		}()
	})
}

func (sm *SyncManager) processJob(job uploadJob) {
	if job.isTrash {
		if err := sm.driveClient.Trash(job.driveID); err != nil {
			slog.Error("drive sync: trashing file", "driveID", job.driveID, "err", err)
			_ = sm.state.SetLastError(err.Error())
			return
		}
		if err := sm.state.DeleteFile(job.relPath); err != nil {
			slog.Error("drive sync: removing file state", "path", job.relPath, "err", err)
		}
		slog.Info("drive sync: file trashed", "path", job.relPath, "driveID", job.driveID)
		return
	}

	driveID, err := sm.driveClient.Upload(job.relPath, job.localPath, job.parentID)
	if err != nil {
		slog.Error("drive sync: uploading file", "path", job.relPath, "err", err)
		_ = sm.state.SetLastError(err.Error())
		return
	}
	entry := FileEntry{DriveID: driveID, LocalModTime: time.Now().UTC().Format(time.RFC3339)}
	if err := sm.state.SetFile(job.relPath, entry); err != nil {
		slog.Error("drive sync: storing file state", "path", job.relPath, "err", err)
	}
	_ = sm.state.SetLastVaultSync(time.Now().UTC().Format(time.RFC3339))
	_ = sm.state.SetLastError("")
	slog.Info("drive sync: file uploaded", "path", job.relPath, "driveID", driveID)
}

func (sm *SyncManager) buildHTTPClient(ctx context.Context, refreshToken string) *http.Client {
	tok := &oauth2.Token{RefreshToken: refreshToken}
	base := sm.oauthCfg.TokenSource(ctx, tok)
	pts := NewPersistingTokenSource(base, sm.tokenStore)
	return oauth2.NewClient(ctx, pts)
}
