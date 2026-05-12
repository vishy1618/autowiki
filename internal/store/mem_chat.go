package store

import (
	"fmt"
	"strings"
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
			last.LastActiveAt = time.Now()
			m.sessions[len(m.sessions)-1] = last
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

func (m *MemChatStore) ListSessions(limit, offset int) ([]ChatSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out, err := m.listSessionsLocked(limit, offset)
	if out == nil {
		out = []ChatSession{}
	}
	return out, err
}

func (m *MemChatStore) SearchMessages(query string, sessionOffset, sessionLimit int) ([]MessageSearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lower := strings.ToLower(query)
	sessions, _ := m.listSessionsLocked(sessionLimit, sessionOffset)

	var out []MessageSearchResult
	for _, sess := range sessions {
		for _, msg := range m.messages {
			if msg.SessionID != sess.ID {
				continue
			}
			if msg.Role == "tool_result" {
				continue
			}
			if !strings.Contains(strings.ToLower(msg.Content), lower) {
				continue
			}
			snippet := msg.Content
			if len(snippet) > 300 {
				snippet = snippet[:300]
			}
			out = append(out, MessageSearchResult{
				SessionDate: sess.CreatedAt.Format(time.RFC3339),
				Role:        msg.Role,
				Snippet:     snippet,
			})
		}
	}
	if out == nil {
		out = []MessageSearchResult{}
	}
	return out, nil
}

// listSessionsLocked is ListSessions without the lock (caller must hold m.mu).
func (m *MemChatStore) listSessionsLocked(limit, offset int) ([]ChatSession, error) {
	n := len(m.sessions)
	var out []ChatSession
	for i := n - 1 - offset; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.sessions[i])
	}
	return out, nil
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

func (m *MemChatStore) GetRecentContext(currentSessionID string, minMessages int) ([]Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var current []Message
	for _, msg := range m.messages {
		if msg.SessionID == currentSessionID {
			current = append(current, msg)
		}
	}

	if len(current) >= minMessages {
		if current == nil {
			current = []Message{}
		}
		return current, nil
	}

	need := minMessages - len(current)
	// Walk sessions newest-first, skipping the current one.
	var prior []Message
	for i := len(m.sessions) - 1; i >= 0 && need > 0; i-- {
		s := m.sessions[i]
		if s.ID == currentSessionID {
			continue
		}
		var sessmsgs []Message
		for _, msg := range m.messages {
			if msg.SessionID == s.ID {
				sessmsgs = append(sessmsgs, msg)
			}
		}
		prior = append(sessmsgs, prior...)
		need -= len(sessmsgs)
	}

	// Trim prior to at most (minMessages - len(current)) messages, taking the newest.
	cap := minMessages - len(current)
	if len(prior) > cap {
		prior = prior[len(prior)-cap:]
	}

	result := append(prior, current...)
	if result == nil {
		result = []Message{}
	}
	return result, nil
}
