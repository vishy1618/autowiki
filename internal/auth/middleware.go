package auth

import (
	"net/http"
	"time"

	"github.com/suvish/autowiki/internal/store"
)

const (
	rotateThreshold = 12 * time.Hour
	gracePeriod     = 30 * time.Second
)

// Middleware validates session cookies on incoming requests.
type Middleware struct {
	sessions store.SessionStore
	newToken func() (string, error)
}

// NewMiddleware constructs a Middleware backed by the given session store.
func NewMiddleware(sessions store.SessionStore) *Middleware {
	return &Middleware{sessions: sessions, newToken: func() (string, error) { return randomToken(32) }}
}

// NewMiddlewareWithTokenGen constructs a Middleware with an injectable token
// generator, for use in tests.
func NewMiddlewareWithTokenGen(sessions store.SessionStore, newToken func() (string, error)) *Middleware {
	return &Middleware{sessions: sessions, newToken: newToken}
}

// Require wraps an API handler: unauthenticated requests receive 401.
// For authenticated sessions in the second half of their window, it rotates
// the token and issues a new cookie before passing through.
func (m *Middleware) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sess, err := m.sessions.GetSession(cookie.Value)
		if err != nil || sess == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Rotate if in the second half of the window (and not a grace token).
		if !sess.GraceOnly && time.Until(sess.ExpiresAt) < rotateThreshold {
			m.rotate(w, sess)
		}

		next.ServeHTTP(w, r)
	})
}

// rotate issues a new token, tombstones the old one, and sets a new cookie.
func (m *Middleware) rotate(w http.ResponseWriter, old *store.Session) {
	tok, err := m.newToken()
	if err != nil {
		return // best-effort; request still proceeds with old token
	}

	newSess := store.Session{
		Token:             tok,
		Email:             old.Email,
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(sessionDuration),
		AbsoluteExpiresAt: old.AbsoluteExpiresAt,
	}

	if err := m.sessions.RotateSession(old.Token, newSess, gracePeriod); err != nil {
		return // best-effort
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
