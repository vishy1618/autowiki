package store

import "time"

// Session holds an authenticated user session.
type Session struct {
	Token     string    `json:"token"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionStore persists and retrieves auth sessions.
type SessionStore interface {
	// CreateSession persists a new session. Returns an error if the token
	// already exists or the underlying store fails.
	CreateSession(s Session) error

	// GetSession retrieves a session by token. Returns nil, nil when the
	// token does not exist or has expired.
	GetSession(token string) (*Session, error)

	// DeleteSession removes a session by token. A no-op if not found.
	DeleteSession(token string) error
}
