package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const sessionTimeout = 30 * time.Minute

// MemChatStore is an in-memory ChatStore. Intended for tests.
type MemChatStore struct {
	mu       sync.RWMutex
	sessions []ChatSession // ordered oldest→newest
	messages []Message
	counter  atomic.Int64
}

func NewMemChatStore() *MemChatStore {
	return &MemChatStore{}
}

func (m *MemChatStore) nextID() string {
	return fmt.Sprintf("%d", m.counter.Add(1))
}

func (m *MemChatStore) ResolveSession() (ChatSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sessions) > 0 {
		last := m.sessions[len(m.sessions)-1]
		if time.Since(last.LastActiveAt) <= sessionTimeout {
			return last, nil
		}
	}

	now := time.Now()
	s := ChatSession{
		ID:           m.nextID(),
		CreatedAt:    now,
		LastActiveAt: now,
	}
	m.sessions = append(m.sessions, s)
	return s, nil
}

func (m *MemChatStore) UpdateSession(s ChatSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, existing := range m.sessions {
		if existing.ID == s.ID {
			m.sessions[i] = s
			return nil
		}
	}
	return fmt.Errorf("session %q not found", s.ID)
}

func (m *MemChatStore) AppendMessage(msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if msg.ID == "" {
		msg.ID = m.nextID()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	m.messages = append(m.messages, msg)
	return nil
}

func (m *MemChatStore) ListMessages(sessionID string) ([]Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []Message
	for _, msg := range m.messages {
		if msg.SessionID == sessionID {
			out = append(out, msg)
		}
	}
	if out == nil {
		out = []Message{}
	}
	return out, nil
}
