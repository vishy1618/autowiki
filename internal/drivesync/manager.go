package drivesync

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
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

type syncJob struct {
	relPath      string
	localPath    string
	parentID     string
	isTrash      bool
	driveID      string // set for trash jobs
	download     bool   // set for download jobs
	driveFileID  string // set for download jobs
	driveModTime string // RFC3339; set for download jobs (conflict resolution)
}

// SyncManager orchestrates vault→Drive upload. Constructed via New().
type SyncManager struct {
	cfg                config.DriveSyncConfig
	oauthCfg           *oauth2.Config
	tokenStore         store.DriveTokenStore
	driveClient        *DriveClient // nil until Start() builds it (or NewWithHTTPClient sets it)
	state              *State
	vaultPath          string
	syncCh             chan syncJob
	wg                 sync.WaitGroup
	workerOnce         sync.Once
	shutdownOnce       sync.Once
	watcher            *Watcher
	watcherDone        chan struct{}
	watcherDebounce    time.Duration // 0 → use default (2s)
	tokenRetryInterval time.Duration // 0 → use default (30s)
	httpClientFactory  func(context.Context, string) *http.Client // nil → use buildHTTPClient
	vaultFolderID      string        // cached after Start(); never changes during process lifetime
	bgCancel           context.CancelFunc
	pendingDownloads   sync.Map // relPath → struct{}; suppresses fsnotify echo after Drive download
}

// New returns a SyncManager ready to Start. The DriveClient is built lazily
// inside Start() once the refresh token is confirmed present.
func New(cfg config.DriveSyncConfig, clientID, clientSecret string, db *pebble.DB, vaultPath string, tokenStore store.DriveTokenStore) *SyncManager {
	oauthCfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drive.DriveScope},
	}
	return &SyncManager{
		cfg:        cfg,
		oauthCfg:   oauthCfg,
		tokenStore: tokenStore,
		state:      NewState(db),
		vaultPath:  vaultPath,
		syncCh:   make(chan syncJob, 200),
	}
}

// NewWithHTTPClient constructs a SyncManager with a pre-built HTTP client,
// bypassing OAuth. The DriveClient is constructed immediately. Intended for tests.
func NewWithHTTPClient(cfg config.DriveSyncConfig, db *pebble.DB, vaultPath string, httpClient *http.Client) *SyncManager {
	dc, err := NewDriveClient(context.Background(), httpClient)
	if err != nil {
		panic("drivesync.NewWithHTTPClient: " + err.Error())
	}
	return &SyncManager{
		cfg:         cfg,
		driveClient: dc,
		state:       NewState(db),
		vaultPath:   vaultPath,
		syncCh:    make(chan syncJob, 200),
	}
}

// Start checks for a stored refresh token if no DriveClient is set yet. If
// absent, it spawns a background goroutine that retries until the token
// appears (e.g. after first sign-in). Otherwise it starts the full sync
// pipeline: folder setup, initial reconcile, poller, and watcher.
func (sm *SyncManager) Start(ctx context.Context) {
	if sm.driveClient == nil {
		refreshToken, err := sm.tokenStore.GetDriveToken()
		if err != nil {
			slog.Error("drive sync: reading refresh token", "err", err)
			return
		}
		if refreshToken == "" {
			slog.Warn("drive sync: no refresh token — will retry after sign-in")
			go sm.waitForToken(ctx)
			return
		}
		factory := sm.httpClientFactory
		if factory == nil {
			factory = sm.buildHTTPClient
		}
		httpClient := factory(ctx, refreshToken)
		sm.driveClient, err = NewDriveClient(ctx, httpClient)
		if err != nil {
			slog.Error("drive sync: creating drive client", "err", err)
			return
		}
	}
	sm.startWithClient(ctx)
}

// waitForToken polls the token store until a refresh token appears, then
// starts the full sync pipeline. Exits when ctx is cancelled.
func (sm *SyncManager) waitForToken(ctx context.Context) {
	interval := sm.tokenRetryInterval
	if interval == 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			token, err := sm.tokenStore.GetDriveToken()
			if err != nil {
				slog.Error("drive sync: reading refresh token in retry loop", "err", err)
				continue
			}
			if token == "" {
				continue
			}
			slog.Info("drive sync: refresh token found, starting sync")
			factory := sm.httpClientFactory
			if factory == nil {
				factory = sm.buildHTTPClient
			}
			httpClient := factory(ctx, token)
			dc, err := NewDriveClient(ctx, httpClient)
			if err != nil {
				slog.Error("drive sync: creating drive client after token found", "err", err)
				continue
			}
			sm.driveClient = dc
			sm.startWithClient(ctx)
			return
		case <-ctx.Done():
			return
		}
	}
}

// startWithClient runs the full startup sequence: folder setup, page token,
// initial reconcile, poller, and watcher. Assumes sm.driveClient is set.
func (sm *SyncManager) startWithClient(ctx context.Context) {
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
	sm.vaultFolderID = vaultID

	existingToken, err := sm.state.GetPageToken()
	if err != nil {
		slog.Error("drive sync: reading stored page token", "err", err)
		return
	}
	if existingToken == "" {
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
	}

	bgCtx, bgCancel := context.WithCancel(ctx)
	sm.bgCancel = bgCancel

	sm.reconcile(bgCtx)

	sm.startPoller(bgCtx)

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

// Reconcile walks the vault, uploads local-only files, and downloads Drive-only
// files. It is a no-op if the DriveClient is not connected. Idempotent: safe to
// call again after a partial failure.
func (sm *SyncManager) Reconcile(ctx context.Context) {
	if sm.driveClient == nil {
		slog.Warn("drive sync: not connected, skipping reconcile")
		return
	}
	sm.startWorkerOnce()
	sm.reconcile(ctx)
}

func (sm *SyncManager) reconcile(ctx context.Context) {
	vaultFolderID := sm.vaultFolderID
	if vaultFolderID == "" {
		var err error
		vaultFolderID, err = sm.state.GetRootFolderID()
		if err != nil {
			slog.Error("drive sync: reading vault folder ID", "err", err)
			return
		}
	}

	slog.Info("drive sync: reconcile started")
	err := filepath.WalkDir(sm.vaultPath, func(path string, d fs.DirEntry, werr error) error {
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
		sm.syncCh <- syncJob{
			relPath:   relPathSlash,
			localPath: path,
			parentID:  parentID,
		}
		return nil
	})
	if err != nil {
		slog.Error("drive sync: walking vault", "err", err)
	}

	// Download Drive-only files: present in Drive but absent from State.
	if vaultFolderID != "" {
		driveFiles, err := sm.driveClient.ListFolder(vaultFolderID)
		if err != nil {
			slog.Error("drive sync: listing Drive folder for reconcile", "err", err)
		} else {
			for _, df := range driveFiles {
				if ctx.Err() != nil {
					break
				}
				existing, err := sm.state.GetFile(df.RelPath)
				if err != nil || existing != nil {
					continue // already tracked — skip
				}
				localPath := filepath.Join(sm.vaultPath, filepath.FromSlash(df.RelPath))
				slog.Info("drive sync: queuing reconcile download", "path", df.RelPath, "driveID", df.ID)
				sm.syncCh <- syncJob{download: true, relPath: df.RelPath, driveFileID: df.ID, localPath: localPath, driveModTime: df.ModifiedTime}
			}
		}
	}

	slog.Info("drive sync: reconcile complete")
}

// Shutdown stops all background goroutines and drains the sync worker.
// Safe to call multiple times.
func (sm *SyncManager) Shutdown() {
	if sm.bgCancel != nil {
		sm.bgCancel()
	}
	if sm.watcher != nil {
		sm.watcher.Close()
		<-sm.watcherDone
	}
	sm.shutdownOnce.Do(func() { close(sm.syncCh) })
	sm.wg.Wait()
}

func (sm *SyncManager) handleFileEvent(event FileEvent) {
	// Suppress echo: if we wrote this file ourselves (download from Drive), skip it.
	if _, downloaded := sm.pendingDownloads.LoadAndDelete(event.RelPath); downloaded {
		slog.Info("drive sync: suppressing echo upload after download", "path", event.RelPath)
		return
	}
	slog.Info("drive sync: watcher event", "path", event.RelPath, "op", event.Op)

	if event.Op == OpRemove || event.Op == OpRename {
		existing, err := sm.state.GetFile(event.RelPath)
		if err != nil || existing == nil {
			slog.Debug("drive sync: ignoring remove for untracked file", "path", event.RelPath)
			return
		}
		sm.syncCh <- syncJob{isTrash: true, relPath: event.RelPath, driveID: existing.DriveID}
		return
	}

	localPath := filepath.Join(sm.vaultPath, filepath.FromSlash(event.RelPath))
	vaultFolderID := sm.vaultFolderID
	dir := filepath.Dir(event.RelPath)
	parentID := vaultFolderID
	if dir != "." && vaultFolderID != "" {
		var err error
		parentID, err = sm.driveClient.EnsureFolderPath(dir, vaultFolderID, sm.state)
		if err != nil {
			slog.Error("drive sync: ensuring folder path for watcher event", "path", dir, "err", err)
			return
		}
	}

	sm.syncCh <- syncJob{
		relPath:   event.RelPath,
		localPath: localPath,
		parentID:  parentID,
	}
}

// startPoller launches a goroutine that polls Drive for changes on a ticker.
func (sm *SyncManager) startPoller(ctx context.Context) {
	interval := time.Duration(sm.cfg.PollIntervalSecs) * time.Second
	if interval == 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	sm.wg.Add(1)
	go func() {
		defer sm.wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sm.pollOnce(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// pollOnce fetches one batch of Drive changes and processes them.
func (sm *SyncManager) pollOnce(ctx context.Context) {
	pageToken, err := sm.state.GetPageToken()
	if err != nil || pageToken == "" {
		slog.Debug("drive sync: poll skipped — no page token stored")
		return
	}

	slog.Info("drive sync: polling for changes", "pageToken", pageToken)
	changes, newToken, err := sm.driveClient.ListChanges(pageToken)
	if err != nil {
		slog.Warn("drive sync: listing changes failed, retrying once", "err", err)
		time.Sleep(2 * time.Second)
		changes, newToken, err = sm.driveClient.ListChanges(pageToken)
		if err != nil {
			slog.Error("drive sync: listing changes", "err", err)
			_ = sm.state.SetLastError(err.Error())
			return
		}
	}

	slog.Info("drive sync: poll complete", "changes", len(changes), "newToken", newToken)

	for _, change := range changes {
		sm.processChange(ctx, change)
	}

	if newToken != "" && newToken != pageToken {
		if err := sm.state.SetPageToken(newToken); err != nil {
			slog.Error("drive sync: storing page token after poll", "err", err)
		}
	}
}

func (sm *SyncManager) processChange(ctx context.Context, change DriveChange) {
	slog.Debug("drive sync: processing change", "fileID", change.FileID, "name", change.Name, "parentID", change.ParentID, "removed", change.Removed, "modifiedTime", change.ModifiedTime)

	if change.Removed || change.Name == "" {
		// Permanent delete or trashed: resolve via reverse file lookup.
		relPath, err := sm.state.GetFileByDriveID(change.FileID)
		if err != nil || relPath == "" {
			slog.Debug("drive sync: removal change skipped — file not tracked locally", "fileID", change.FileID)
			return
		}
		localPath := filepath.Join(sm.vaultPath, filepath.FromSlash(relPath))
		if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
			slog.Error("drive sync: deleting local file", "path", relPath, "err", err)
		}
		if err := sm.state.DeleteFile(relPath); err != nil {
			slog.Error("drive sync: removing file state after Drive deletion", "path", relPath, "err", err)
		}
		slog.Info("drive sync: local file deleted (Drive removal)", "path", relPath)
		return
	}

	relPath, ok := sm.resolveRelPath(change)
	if !ok {
		slog.Warn("drive sync: change skipped — parent folder not in state", "fileID", change.FileID, "name", change.Name, "parentID", change.ParentID)
		return
	}

	// Skip if we already have this exact version (DriveModTime match means no change since last sync).
	if existing, err := sm.state.GetFile(relPath); err == nil && existing != nil {
		if existing.DriveModTime == change.ModifiedTime {
			slog.Debug("drive sync: change skipped — already up to date", "path", relPath, "driveModTime", change.ModifiedTime)
			return
		}
		slog.Debug("drive sync: change has newer version", "path", relPath, "stored", existing.DriveModTime, "incoming", change.ModifiedTime)
	}

	localPath := filepath.Join(sm.vaultPath, filepath.FromSlash(relPath))
	slog.Info("drive sync: queuing download from Drive", "path", relPath, "driveID", change.FileID)
	sm.syncCh <- syncJob{download: true, relPath: relPath, driveFileID: change.FileID, localPath: localPath, driveModTime: change.ModifiedTime}
}

// resolveRelPath derives the vault-relative path for a Drive change.
// Returns (relPath, true) when resolved; ("", false) when the change's parent
// is outside the vault or unknown.
func (sm *SyncManager) resolveRelPath(change DriveChange) (string, bool) {
	if change.ParentID == sm.vaultFolderID {
		return change.Name, true
	}
	folderRelPath, err := sm.state.GetFolderByDriveID(change.ParentID)
	if err != nil || folderRelPath == "" {
		return "", false
	}
	return folderRelPath + "/" + change.Name, true
}

func (sm *SyncManager) startWorkerOnce() {
	sm.workerOnce.Do(func() {
		sm.wg.Add(1)
		go func() {
			defer sm.wg.Done()
			for job := range sm.syncCh {
				sm.processJob(job)
			}
		}()
	})
}

func (sm *SyncManager) processJob(job syncJob) {
	if job.download {
		// Conflict resolution: if local file exists, decide which side wins.
		if info, err := os.Stat(job.localPath); err == nil {
			var driveModTime time.Time
			if job.driveModTime != "" {
				driveModTime, _ = time.Parse(time.RFC3339, job.driveModTime)
			}
			decision := Resolve(info.ModTime(), driveModTime, sm.cfg.ConflictStrategy)
			if decision.Winner == "local" {
				slog.Info("drive sync: download skipped — local file is newer", "path", job.relPath)
				return
			}
			if decision.ConflictPath != "" {
				// Rename local to conflict path before overwriting.
				ext := filepath.Ext(job.localPath)
				stem := job.localPath[:len(job.localPath)-len(ext)]
				conflictLocal := stem + "." + decision.ConflictPath + ext
				if err := os.Rename(job.localPath, conflictLocal); err != nil {
					slog.Error("drive sync: renaming local file to conflict path", "path", job.relPath, "err", err)
				} else {
					slog.Info("drive sync: conflict file created", "path", filepath.Base(conflictLocal))
				}
			}
		}

		// Mark as pending before writing so handleFileEvent can suppress the echo.
		// Only needed when the watcher is running; during reconcile there is no watcher yet.
		if sm.watcher != nil {
			sm.pendingDownloads.Store(job.relPath, struct{}{})
		}
		if err := sm.driveClient.Download(job.driveFileID, job.localPath); err != nil {
			sm.pendingDownloads.Delete(job.relPath) // no file written, no event will fire
			slog.Error("drive sync: downloading file", "path", job.relPath, "err", err)
			_ = sm.state.SetLastError(err.Error())
			return
		}
		entry := FileEntry{DriveID: job.driveFileID, DriveModTime: job.driveModTime, LocalModTime: time.Now().UTC().Format(time.RFC3339)}
		if err := sm.state.SetFile(job.relPath, entry); err != nil {
			slog.Error("drive sync: storing file state after download", "path", job.relPath, "err", err)
		}
		_ = sm.state.SetLastVaultSync(time.Now().UTC().Format(time.RFC3339))
		_ = sm.state.SetLastError("")
		slog.Info("drive sync: file downloaded", "path", job.relPath, "driveID", job.driveFileID)
		return
	}

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

	driveID, md5, modTime, err := sm.driveClient.Upload(job.relPath, job.localPath, job.parentID)
	if err != nil {
		slog.Error("drive sync: uploading file", "path", job.relPath, "err", err)
		_ = sm.state.SetLastError(err.Error())
		return
	}
	entry := FileEntry{
		DriveID:      driveID,
		MD5:          md5,
		DriveModTime: modTime,
		LocalModTime: time.Now().UTC().Format(time.RFC3339),
	}
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
