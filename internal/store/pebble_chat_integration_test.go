package store_test

import (
	"testing"

	"github.com/suvish/autowiki/internal/store"
)

func TestPebbleChatStore(t *testing.T) {
	db, err := store.OpenPebble(t.TempDir())
	if err != nil {
		t.Fatalf("OpenPebble: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	runChatStoreTests(t, store.NewPebbleChatStore(db))
}
