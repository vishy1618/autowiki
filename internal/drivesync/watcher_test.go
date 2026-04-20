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

// TestWatcher_HandleNewDir_EmitsSyntheticCreateForExistingFiles tests the synthetic
// event logic directly, without relying on fsnotify to fire the directory Create event.
// The directory is pre-populated before the watcher starts, so there is no race.
func TestWatcher_HandleNewDir_EmitsSyntheticCreateForExistingFiles(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "notes")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "existing.md"), []byte("x"), 0644); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	w, err := drivesync.NewWatcherForTest(dir, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcherForTest: %v", err)
	}
	defer w.Close()

	// Act — trigger handleNewDir directly; file is guaranteed to be present.
	drivesync.TriggerNewDirForTest(w, subdir)

	got := drainEvents(w.Events(), 200*time.Millisecond)

	var found bool
	for _, e := range got {
		if e.RelPath == "notes/existing.md" && e.Op == drivesync.OpCreate {
			found = true
		}
	}
	if !found {
		t.Errorf("expected synthetic Create for notes/existing.md; got %v", got)
	}
}

func TestWatcher_HandleNewDir_LargeDirectory_EmitsEventsWithoutDeadlock(t *testing.T) {
	const fileCount = 150 // exceeds the synthetic channel capacity of 100

	dir := t.TempDir()
	subdir := filepath.Join(dir, "big")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("file%03d.md", i)
		if err := os.WriteFile(filepath.Join(subdir, name), []byte("x"), 0644); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}

	w, err := drivesync.NewWatcherForTest(dir, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcherForTest: %v", err)
	}
	defer w.Close()

	drivesync.TriggerNewDirForTest(w, subdir)

	got := drainEvents(w.Events(), 500*time.Millisecond)
	if len(got) != fileCount {
		t.Errorf("want %d events, got %d", fileCount, len(got))
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
