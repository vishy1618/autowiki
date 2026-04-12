package store

import (
	"encoding/json"
	"fmt"
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

