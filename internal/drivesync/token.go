package drivesync

import (
	"golang.org/x/oauth2"

	"github.com/suvish/autowiki/internal/store"
)

// PersistingTokenSource wraps an oauth2.TokenSource and writes any new
// refresh token back to the DriveTokenStore so it survives server restarts.
type PersistingTokenSource struct {
	base             oauth2.TokenSource
	store            store.DriveTokenStore
	lastRefreshToken string
}

// NewPersistingTokenSource constructs a PersistingTokenSource. The current
// stored token is read at construction time to seed the comparison baseline.
func NewPersistingTokenSource(base oauth2.TokenSource, s store.DriveTokenStore) *PersistingTokenSource {
	last, _ := s.GetDriveToken()
	return &PersistingTokenSource{base: base, store: s, lastRefreshToken: last}
}

// Token returns the current token from the base source, persisting the
// refresh token if it has changed.
func (p *PersistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
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
