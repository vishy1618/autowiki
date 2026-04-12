package store_test

import (
	"testing"

	"github.com/suvish/autowiki/internal/store"
)

func TestMemStore(t *testing.T) {
	runSessionStoreTests(t, store.NewMemStore())
}
