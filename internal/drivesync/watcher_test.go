package drivesync_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/drivesync"
)

// drainEvents collects all FileEvents from ch within the given window.
func drainEvents(ch <-chan drivesync.FileEvent, window time.Duration) []drivesync.FileEvent {
	var got []drivesync.FileEvent
	deadline := time.After(window)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, e)
		case <-deadline:
			return got
		}
	}
}

func TestWatcher_RapidWrites_EmitsSingleEvent(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "note.md")
	if err := os.WriteFile(f, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := drivesync.NewWatcherForTest(dir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcherForTest: %v", err)
	}
	defer w.Close()

	for i := 0; i < 5; i++ {
		if err := os.WriteFile(f, []byte(fmt.Sprintf("v%d", i)), 0644); err != nil {
			t.Fatalf("writing file: %v", err)
		}
	}

	got := drainEvents(w.Events(), 200*time.Millisecond)
	if len(got) != 1 {
		t.Errorf("want 1 event, got %d: %v", len(got), got)
	}
}

func TestWatcher_NewDirectory_EmitsSyntheticCreateForExistingFile(t *testing.T) {
	dir := t.TempDir()

	w, err := drivesync.NewWatcherForTest(dir, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcherForTest: %v", err)
	}
	defer w.Close()

	// Create a subdirectory with a file in it.
	// fsnotify fires a Create event for the dir; the watcher should then scan
	// the dir and emit a synthetic Create for any files already present.
	subdir := filepath.Join(dir, "notes")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "existing.md"), []byte("x"), 0644); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	got := drainEvents(w.Events(), 200*time.Millisecond)

	var found bool
	for _, e := range got {
		if e.RelPath == "notes/existing.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected synthetic Create event for notes/existing.md; got %v", got)
	}
}
