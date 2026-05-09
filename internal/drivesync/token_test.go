package drivesync_test

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/suvish/autowiki/internal/drivesync"
	"github.com/suvish/autowiki/internal/store"
)

// staticTokenSource returns the same token on every call.
type staticTokenSource struct{ tok *oauth2.Token }

func (s *staticTokenSource) Token() (*oauth2.Token, error) { return s.tok, nil }

// failingTokenSource always returns an error.
type failingTokenSource struct{ err error }

func (f *failingTokenSource) Token() (*oauth2.Token, error) { return nil, f.err }

func TestPersistingTokenSource_PersistsNewRefreshToken(t *testing.T) {
	ts := store.NewMemStore()
	base := &staticTokenSource{tok: &oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-new",
		Expiry:       time.Now().Add(time.Hour),
	}}
	pts := drivesync.NewPersistingTokenSource(base, ts, nil)

	if _, err := pts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	got, err := ts.GetDriveToken()
	if err != nil {
		t.Fatalf("GetDriveToken: %v", err)
	}
	if got != "refresh-new" {
		t.Errorf("DriveToken: want %q, got %q", "refresh-new", got)
	}
}

func TestPersistingTokenSource_NoOpWhenRefreshTokenUnchanged(t *testing.T) {
	ts := store.NewMemStore()
	_ = ts.SetDriveToken("refresh-same")
	writeCount := 0
	countingStore := &countingTokenStore{inner: ts, onSet: func() { writeCount++ }}
	base := &staticTokenSource{tok: &oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-same",
		Expiry:       time.Now().Add(time.Hour),
	}}
	pts := drivesync.NewPersistingTokenSource(base, countingStore, nil)

	if _, err := pts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}

	if writeCount != 0 {
		t.Errorf("SetDriveToken called %d time(s), want 0", writeCount)
	}
}

func TestPersistingTokenSource_RecoversAfterReSignIn(t *testing.T) {
	// Arrange: store has the expired token; SyncManager was already started with it.
	ts := store.NewMemStore()
	_ = ts.SetDriveToken("refresh-expired")

	base := &failingTokenSource{err: errors.New(`oauth2: "invalid_grant" "Token has been expired or revoked."`)}

	recoveredSource := &staticTokenSource{tok: &oauth2.Token{
		AccessToken:  "access-recovered",
		RefreshToken: "refresh-recovered",
		Expiry:       time.Now().Add(time.Hour),
	}}
	factory := func(rt string) oauth2.TokenSource {
		if rt == "refresh-recovered" {
			return recoveredSource
		}
		return &failingTokenSource{err: errors.New("unexpected refresh token in factory")}
	}

	// Construct the source while the store still holds the expired token,
	// mirroring the real startup path where SyncManager builds its HTTP client
	// from the token that was in Pebble at boot time.
	pts := drivesync.NewPersistingTokenSource(base, ts, factory)

	// Simulate the user signing in again: auth callback writes the new token.
	_ = ts.SetDriveToken("refresh-recovered")

	// Act
	tok, err := pts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "access-recovered" {
		t.Errorf("AccessToken: want %q, got %q", "access-recovered", tok.AccessToken)
	}
}

func TestPersistingTokenSource_DoesNotRecoverWhenNoNewToken(t *testing.T) {
	// Arrange: store has the same (expired) token the base was built with.
	ts := store.NewMemStore()
	_ = ts.SetDriveToken("refresh-expired")

	authErr := errors.New(`oauth2: "invalid_grant"`)
	base := &failingTokenSource{err: authErr}

	factoryCalled := false
	factory := func(rt string) oauth2.TokenSource {
		factoryCalled = true
		return base
	}
	pts := drivesync.NewPersistingTokenSource(base, ts, factory)

	// Act: base still fails and store has no new token.
	_, err := pts.Token()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if factoryCalled {
		t.Error("factory should not be called when store has no new token")
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
