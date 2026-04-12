package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
)

const sessionKeyPrefix = "auth:sessions:"

// PebbleStore is a Pebble-backed SessionStore.
type PebbleStore struct {
	db *pebble.DB
}

// NewPebbleStore opens (or creates) a Pebble database at the given path.
func NewPebbleStore(path string) (*PebbleStore, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("opening pebble at %s: %w", path, err)
	}
	return &PebbleStore{db: db}, nil
}

// Close releases the underlying Pebble handle. Always call this when done.
func (p *PebbleStore) Close() error {
	return p.db.Close()
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

	if time.Now().After(s.ExpiresAt) {
		return nil, nil
	}

	return &s, nil
}

func (p *PebbleStore) DeleteSession(token string) error {
	key := []byte(sessionKeyPrefix + token)
	return p.db.Delete(key, pebble.Sync)
}
