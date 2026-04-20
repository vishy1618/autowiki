package drivesync_test

import (
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/suvish/autowiki/internal/drivesync"
	"github.com/suvish/autowiki/internal/store"
)

// staticTokenSource returns the same token on every call.
type staticTokenSource struct{ tok *oauth2.Token }

func (s *staticTokenSource) Token() (*oauth2.Token, error) { return s.tok, nil }

func TestPersistingTokenSource_PersistsNewRefreshToken(t *testing.T) {
	// Arrange
	ts := store.NewMemStore()
	base := &staticTokenSource{tok: &oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-new",
		Expiry:       time.Now().Add(time.Hour),
	}}
	pts := drivesync.NewPersistingTokenSource(base, ts)

	// Act
	if _, err := pts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	// Assert
	got, err := ts.GetDriveToken()
	if err != nil {
		t.Fatalf("GetDriveToken: %v", err)
	}
	if got != "refresh-new" {
		t.Errorf("DriveToken: want %q, got %q", "refresh-new", got)
	}
}

func TestPersistingTokenSource_NoOpWhenRefreshTokenUnchanged(t *testing.T) {
	// Arrange — store already has the same token the source will return.
	ts := store.NewMemStore()
	_ = ts.SetDriveToken("refresh-same")
	writeCount := 0
	countingStore := &countingTokenStore{inner: ts, onSet: func() { writeCount++ }}
	base := &staticTokenSource{tok: &oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-same",
		Expiry:       time.Now().Add(time.Hour),
	}}
	pts := drivesync.NewPersistingTokenSource(base, countingStore)

	// Act
	if _, err := pts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	// Assert — SetDriveToken was not called.
	if writeCount != 0 {
		t.Errorf("SetDriveToken called %d time(s), want 0", writeCount)
	}
}

// countingTokenStore wraps a DriveTokenStore and counts SetDriveToken calls.
type countingTokenStore struct {
	inner store.DriveTokenStore
	onSet func()
}

func (c *countingTokenStore) GetDriveToken() (string, error) { return c.inner.GetDriveToken() }
func (c *countingTokenStore) SetDriveToken(t string) error {
	c.onSet()
	return c.inner.SetDriveToken(t)
}
