package drivesync

import (
	"golang.org/x/oauth2"

	"github.com/suvish/autowiki/internal/store"
)

// PersistingTokenSource wraps an oauth2.TokenSource and writes any new
// refresh token back to the DriveTokenStore so it survives server restarts.
//
// When newSource is non-nil and the base source fails, Token checks whether a
// newer refresh token has appeared in the store (e.g. written by a subsequent
// sign-in) and transparently rebuilds from it — no server restart needed.
type PersistingTokenSource struct {
	base             oauth2.TokenSource
	store            store.DriveTokenStore
	newSource        func(refreshToken string) oauth2.TokenSource // nil disables recovery
	lastRefreshToken string
}

// NewPersistingTokenSource constructs a PersistingTokenSource. newSource, when
// non-nil, is called with a fresh refresh token to rebuild the base source
// after an auth failure (e.g. invalid_grant after the user re-signs-in).
func NewPersistingTokenSource(base oauth2.TokenSource, s store.DriveTokenStore, newSource func(string) oauth2.TokenSource) *PersistingTokenSource {
	last, _ := s.GetDriveToken()
	return &PersistingTokenSource{base: base, store: s, newSource: newSource, lastRefreshToken: last}
}

// Token returns the current token from the base source, persisting the refresh
// token if it has changed. On failure it attempts recovery by reading the
// latest token from the store and rebuilding the base source before giving up.
func (p *PersistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil && p.newSource != nil {
		fresh, storeErr := p.store.GetDriveToken()
		if storeErr == nil && fresh != "" && fresh != p.lastRefreshToken {
			p.base = p.newSource(fresh)
			p.lastRefreshToken = fresh
			tok, err = p.base.Token()
		}
	}
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken != "" && tok.RefreshToken != p.lastRefreshToken {
		if err := p.store.SetDriveToken(tok.RefreshToken); err != nil {
			return nil, err
		}
		p.lastRefreshToken = tok.RefreshToken
	}
	return tok, nil
}
