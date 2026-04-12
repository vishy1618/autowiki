package auth

import (
	"net/http"

	"github.com/suvish/autowiki/internal/store"
)

// Middleware validates session cookies on incoming requests.
type Middleware struct {
	sessions store.SessionStore
}

// NewMiddleware constructs a Middleware backed by the given session store.
func NewMiddleware(sessions store.SessionStore) *Middleware {
	return &Middleware{sessions: sessions}
}

// Require wraps an API handler: unauthenticated requests receive 401.
func (m *Middleware) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.isAuthenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireOrRedirect wraps a non-API handler: unauthenticated requests are
// redirected to /login.
func (m *Middleware) RequireOrRedirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.isAuthenticated(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	sess, err := m.sessions.GetSession(cookie.Value)
	return err == nil && sess != nil
}
