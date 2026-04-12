package store_test

import (
	"os"
	"testing"

	"github.com/suvish/autowiki/internal/store"
)

func TestPebbleStore(t *testing.T) {
	dir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s, err := store.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	runSessionStoreTests(t, s)
}
