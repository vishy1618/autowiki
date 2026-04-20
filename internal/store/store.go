package store

import "time"

// ChatSession represents a single conversation window. A new session is
// created after 30 minutes of inactivity.
type ChatSession struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// Message is a single turn in a chat session.
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"` // "user" | "assistant" | "tool_result"
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// MessageSearchResult is a single hit from SearchMessages.
type MessageSearchResult struct {
	SessionDate string // RFC3339 date of the session's CreatedAt
	Role        string // "user" or "assistant"
	Snippet     string // up to 300 characters of the matching message content
}

// ChatStore persists chat sessions and messages.
type ChatStore interface {
	// ResolveSession returns the current active session, or creates a new one
	// if none exists or the last session has been inactive for >30 minutes.
	ResolveSession() (ChatSession, error)

	// UpdateSession persists changes to an existing session (e.g. LastActiveAt).
	UpdateSession(ChatSession) error

	// AppendMessage stores a new message. It assigns ID and CreatedAt if not set.
	AppendMessage(Message) error

	// ListMessages returns all messages for a session in insertion order.
	ListMessages(sessionID string) ([]Message, error)

	// ListSessions returns sessions newest-first. limit caps the count;
	// offset skips the first N sessions (for pagination).
	ListSessions(limit, offset int) ([]ChatSession, error)

	// SearchMessages scans up to sessionLimit sessions starting at sessionOffset
	// (newest-first), does a case-insensitive substring match on message content,
	// skips tool_result messages, and returns snippets (≤300 chars) with session date.
	SearchMessages(query string, sessionOffset, sessionLimit int) ([]MessageSearchResult, error)
}

// DriveTokenStore persists the Google OAuth refresh token used for Drive sync.
type DriveTokenStore interface {
	// GetDriveToken returns the stored refresh token, or "" if none is set.
	GetDriveToken() (string, error)
	// SetDriveToken stores the refresh token, replacing any previous value.
	SetDriveToken(refreshToken string) error
}

// Session holds an authenticated user session.
type Session struct {
	Token             string    `json:"token"`
	Email             string    `json:"email"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	AbsoluteExpiresAt time.Time `json:"absolute_expires_at,omitempty"`
	// GraceOnly marks a short-lived tombstone left after token rotation so
	// in-flight requests carrying the superseded token still succeed for
	// ~30 seconds. Grace sessions are not eligible for further rotation.
	GraceOnly bool `json:"grace_only,omitempty"`
}

// SessionStore persists and retrieves auth sessions.
type SessionStore interface {
	// CreateSession persists a new session. Returns an error if the token
	// already exists or the underlying store fails.
	CreateSession(s Session) error

	// GetSession retrieves a session by token. Returns nil, nil when the
	// token does not exist or has expired (rolling or absolute).
	GetSession(token string) (*Session, error)

	// DeleteSession removes a session by token. A no-op if not found.
	DeleteSession(token string) error

	// RotateSession atomically creates newSess and replaces the record for
	// oldToken with a short-lived grace tombstone (GraceOnly=true, ExpiresAt
	// = now+gracePeriod). In-flight requests carrying the old token remain
	// valid during the grace window but will not trigger further rotation.
	RotateSession(oldToken string, newSess Session, gracePeriod time.Duration) error
}
