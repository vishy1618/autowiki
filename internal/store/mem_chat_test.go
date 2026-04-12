package store_test

import (
	"testing"

	"github.com/suvish/autowiki/internal/store"
)

func TestMemChatStore(t *testing.T) {
	runChatStoreTests(t, store.NewMemChatStore())
}
