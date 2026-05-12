package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// Key schema (TRD §4.6):
//
//	chat:sessions:list              → JSON array of session IDs, newest first
//	chat:sessions:{id}:meta         → JSON ChatSession
//	chat:sessions:{id}:messages     → JSON array of message IDs, in order
//	chat:messages:{id}              → JSON Message

const (
	chatSessionsListKey    = "chat:sessions:list"
	chatSessionMetaPrefix  = "chat:sessions:"
	chatSessionMetaSuffix  = ":meta"
	chatSessionMsgsSuffix  = ":messages"
	chatMessagePrefix      = "chat:messages:"
)

// PebbleChatStore is a Pebble-backed ChatStore.
type PebbleChatStore struct {
	db *pebble.DB
}

// NewPebbleChatStore returns a ChatStore backed by the given Pebble database.
func NewPebbleChatStore(db *pebble.DB) *PebbleChatStore {
	return &PebbleChatStore{db: db}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (p *PebbleChatStore) getJSON(key string, out interface{}) (bool, error) {
	data, closer, err := p.db.Get([]byte(key))
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pebble get %q: %w", key, err)
	}
	defer closer.Close()
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("unmarshal %q: %w", key, err)
	}
	return true, nil
}

func (p *PebbleChatStore) setJSON(key string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %q: %w", key, err)
	}
	return p.db.Set([]byte(key), data, pebble.Sync)
}

func sessionMetaKey(id string) string {
	return chatSessionMetaPrefix + id + chatSessionMetaSuffix
}

func sessionMsgsKey(id string) string {
	return chatSessionMetaPrefix + id + chatSessionMsgsSuffix
}

func messageKey(id string) string {
	return chatMessagePrefix + id
}

func newID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// ── ChatStore implementation ─────────────────────────────────────────────────

func (p *PebbleChatStore) loadSessionList() ([]string, error) {
	var ids []string
	if _, err := p.getJSON(chatSessionsListKey, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (p *PebbleChatStore) saveSessionList(ids []string) error {
	return p.setJSON(chatSessionsListKey, ids)
}

func (p *PebbleChatStore) ResolveSession() (ChatSession, error) {
	ids, err := p.loadSessionList()
	if err != nil {
		return ChatSession{}, err
	}

	if len(ids) > 0 {
		lastID := ids[0] // newest first
		var last ChatSession
		found, err := p.getJSON(sessionMetaKey(lastID), &last)
		if err != nil {
			return ChatSession{}, err
		}
		if found && time.Since(last.LastActiveAt) <= sessionTimeout {
			last.LastActiveAt = time.Now()
			if err := p.setJSON(sessionMetaKey(last.ID), last); err != nil {
				return ChatSession{}, err
			}
			return last, nil
		}
	}

	return p.createSession(ids)
}

func (p *PebbleChatStore) createSession(existingIDs []string) (ChatSession, error) {
	now := time.Now()
	s := ChatSession{
		ID:           newID(),
		CreatedAt:    now,
		LastActiveAt: now,
	}
	if err := p.setJSON(sessionMetaKey(s.ID), s); err != nil {
		return ChatSession{}, err
	}
	// Prepend new session to list (newest first).
	newList := append([]string{s.ID}, existingIDs...)
	if err := p.saveSessionList(newList); err != nil {
		return ChatSession{}, err
	}
	return s, nil
}

func (p *PebbleChatStore) UpdateSession(s ChatSession) error {
	return p.setJSON(sessionMetaKey(s.ID), s)
}

func (p *PebbleChatStore) AppendMessage(msg Message) error {
	if msg.ID == "" {
		msg.ID = newID()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

	// Persist the message itself.
	if err := p.setJSON(messageKey(msg.ID), msg); err != nil {
		return err
	}

	// Append its ID to the session's message list.
	msgsKey := sessionMsgsKey(msg.SessionID)
	var msgIDs []string
	if _, err := p.getJSON(msgsKey, &msgIDs); err != nil {
		return err
	}
	msgIDs = append(msgIDs, msg.ID)
	return p.setJSON(msgsKey, msgIDs)
}

func (p *PebbleChatStore) ListSessions(limit, offset int) ([]ChatSession, error) {
	ids, err := p.loadSessionList()
	if err != nil {
		return nil, err
	}
	// ids is newest-first; apply offset and limit.
	if offset >= len(ids) {
		return []ChatSession{}, nil
	}
	ids = ids[offset:]
	if limit < len(ids) {
		ids = ids[:limit]
	}
	out := make([]ChatSession, 0, len(ids))
	for _, id := range ids {
		var s ChatSession
		found, err := p.getJSON(sessionMetaKey(id), &s)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, s)
		}
	}
	return out, nil
}

func (p *PebbleChatStore) SearchMessages(query string, sessionOffset, sessionLimit int) ([]MessageSearchResult, error) {
	sessions, err := p.ListSessions(sessionLimit, sessionOffset)
	if err != nil {
		return nil, err
	}

	lower := strings.ToLower(query)
	var out []MessageSearchResult
	for _, sess := range sessions {
		msgs, err := p.ListMessages(sess.ID)
		if err != nil {
			return nil, err
		}
		for _, msg := range msgs {
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

func (p *PebbleChatStore) ListMessages(sessionID string) ([]Message, error) {
	var msgIDs []string
	found, err := p.getJSON(sessionMsgsKey(sessionID), &msgIDs)
	if err != nil {
		return nil, err
	}
	if !found || len(msgIDs) == 0 {
		return []Message{}, nil
	}

	msgs := make([]Message, 0, len(msgIDs))
	for _, id := range msgIDs {
		var m Message
		found, err := p.getJSON(messageKey(id), &m)
		if err != nil {
			return nil, err
		}
		if found {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

func (p *PebbleChatStore) GetRecentContext(currentSessionID string, minMessages int) ([]Message, error) {
	current, err := p.ListMessages(currentSessionID)
	if err != nil {
		return nil, err
	}

	if len(current) >= minMessages {
		return current, nil
	}

	ids, err := p.loadSessionList()
	if err != nil {
		return nil, err
	}

	need := minMessages - len(current)
	var prior []Message
	// ids is newest-first; skip the current session and walk older sessions.
	for _, id := range ids {
		if id == currentSessionID || need <= 0 {
			continue
		}
		sessmsgs, err := p.ListMessages(id)
		if err != nil {
			return nil, err
		}
		prior = append(sessmsgs, prior...)
		need -= len(sessmsgs)
	}

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

