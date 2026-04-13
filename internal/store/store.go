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
}

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
