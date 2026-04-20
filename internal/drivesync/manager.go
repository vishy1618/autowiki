package drivesync

import (
	"context"
	"log/slog"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"

	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/store"
)

// SyncManager orchestrates vault→Drive upload. Constructed via New().
type SyncManager struct {
	cfg            config.DriveSyncConfig
	oauthCfg       *oauth2.Config
	tokenStore     store.DriveTokenStore
	overrideClient *http.Client // non-nil in tests to bypass OAuth
}

// New returns a SyncManager ready to Start. clientID and clientSecret are the
// same Google credentials used for sign-in.
func New(cfg config.DriveSyncConfig, clientID, clientSecret string, tokenStore store.DriveTokenStore) *SyncManager {
	oauthCfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drive.DriveFileScope},
	}
	return &SyncManager{cfg: cfg, oauthCfg: oauthCfg, tokenStore: tokenStore}
}

// NewWithHTTPClient constructs a SyncManager that uses the given HTTP client
// instead of building one from the stored refresh token. Intended for tests.
func NewWithHTTPClient(cfg config.DriveSyncConfig, tokenStore store.DriveTokenStore, httpClient *http.Client) *SyncManager {
	return &SyncManager{cfg: cfg, tokenStore: tokenStore, overrideClient: httpClient}
}

// Start checks for a stored refresh token; if absent it logs and returns.
// Otherwise it creates the vault Drive folder and fetches the start page token.
// Errors are logged but never propagated — sync failure must not affect chat.
func (sm *SyncManager) Start(ctx context.Context) {
	refreshToken, err := sm.tokenStore.GetDriveToken()
	if err != nil {
		slog.Error("drive sync: reading refresh token", "err", err)
		return
	}
	if refreshToken == "" {
		slog.Warn("drive sync: no refresh token — sign out and back in to grant Drive access")
		return
	}

	httpClient := sm.overrideClient
	if httpClient == nil {
		httpClient = sm.buildHTTPClient(ctx, refreshToken)
	}

	driveClient, err := NewDriveClient(ctx, httpClient)
	if err != nil {
		slog.Error("drive sync: creating drive client", "err", err)
		return
	}

	rootID, err := driveClient.EnsureFolder(sm.cfg.RootFolderName, "")
	if err != nil {
		slog.Error("drive sync: ensuring root folder", "err", err)
		return
	}
	slog.Info("drive sync: root folder ready", "folderID", rootID)

	vaultID, err := driveClient.EnsureFolder(sm.cfg.VaultFolderName, rootID)
	if err != nil {
		slog.Error("drive sync: ensuring vault folder", "err", err)
		return
	}
	slog.Info("drive sync: vault folder ready", "folderID", vaultID)

	pageToken, err := driveClient.GetStartPageToken()
	if err != nil {
		slog.Error("drive sync: getting start page token", "err", err)
		return
	}
	slog.Info("drive sync: start page token", "token", pageToken)
}

func (sm *SyncManager) buildHTTPClient(ctx context.Context, refreshToken string) *http.Client {
	tok := &oauth2.Token{RefreshToken: refreshToken}
	base := sm.oauthCfg.TokenSource(ctx, tok)
	pts := NewPersistingTokenSource(base, sm.tokenStore)
	return oauth2.NewClient(ctx, pts)
}
