package store

import (
	"sync"
	"time"
)

// MemStore is an in-memory SessionStore. Intended for tests.
type MemStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
}

func NewMemStore() *MemStore {
	return &MemStore{sessions: make(map[string]Session)}
}

func (m *MemStore) CreateSession(s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.Token] = s
	return nil
}

func (m *MemStore) GetSession(token string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[token]
	if !ok {
		return nil, nil
	}
	if time.Now().After(s.ExpiresAt) {
		return nil, nil
	}
	return &s, nil
}

func (m *MemStore) DeleteSession(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
	return nil
}
