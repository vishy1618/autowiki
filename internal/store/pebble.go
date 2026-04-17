package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
)

// OpenPebble opens (or creates) a Pebble database at the given path.
// The caller owns the returned *pebble.DB and must call Close when done.
func OpenPebble(path string) (*pebble.DB, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("opening pebble at %s: %w", path, err)
	}
	return db, nil
}

// ── Auth session store ──────────────────────────────────────────────────────

const sessionKeyPrefix = "auth:sessions:"

// PebbleStore is a Pebble-backed SessionStore.
type PebbleStore struct {
	db *pebble.DB
}

// NewPebbleStore returns a SessionStore backed by the given Pebble database.
func NewPebbleStore(db *pebble.DB) *PebbleStore {
	return &PebbleStore{db: db}
}

func (p *PebbleStore) CreateSession(s Session) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshalling session: %w", err)
	}
	key := []byte(sessionKeyPrefix + s.Token)
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetSession(token string) (*Session, error) {
	key := []byte(sessionKeyPrefix + token)
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pebble get: %w", err)
	}
	defer closer.Close()

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshalling session: %w", err)
	}

	now := time.Now()
	if now.After(s.ExpiresAt) {
		return nil, nil
	}
	if !s.AbsoluteExpiresAt.IsZero() && now.After(s.AbsoluteExpiresAt) {
		return nil, nil
	}

	return &s, nil
}

func (p *PebbleStore) DeleteSession(token string) error {
	key := []byte(sessionKeyPrefix + token)
	return p.db.Delete(key, pebble.Sync)
}

func (p *PebbleStore) RotateSession(oldToken string, newSess Session, gracePeriod time.Duration) error {
	// Write new session.
	if err := p.CreateSession(newSess); err != nil {
		return fmt.Errorf("creating new session: %w", err)
	}

	// Overwrite old token with a grace tombstone.
	key := []byte(sessionKeyPrefix + oldToken)
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil // old token already gone — nothing to tombstone
	}
	if err != nil {
		return fmt.Errorf("fetching old session: %w", err)
	}
	var old Session
	unmarshalErr := json.Unmarshal(data, &old)
	closer.Close()
	if unmarshalErr != nil {
		return fmt.Errorf("unmarshalling old session: %w", unmarshalErr)
	}

	old.ExpiresAt = time.Now().Add(gracePeriod)
	old.GraceOnly = true
	return p.CreateSession(old) // overwrites the key (same token)
}
