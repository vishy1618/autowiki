package drivesync

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileOp describes the type of file-system change.
type FileOp string

const (
	OpWrite  FileOp = "Write"
	OpCreate FileOp = "Create"
	OpRename FileOp = "Rename"
	OpRemove FileOp = "Remove"
)

// FileEvent carries a single debounced file-system event.
type FileEvent struct {
	RelPath string
	Op      FileOp
}

// Watcher watches a directory tree for file changes and emits debounced
// FileEvents on its channel.
type Watcher struct {
	root      string
	debounce  time.Duration
	fw        *fsnotify.Watcher
	events    chan FileEvent
	synthetic chan FileEvent // immediate events for new-dir scans, bypasses debounce
	done      chan struct{}
}

// NewWatcher creates a Watcher for the given root directory using a 2-second debounce.
func NewWatcher(root string) (*Watcher, error) {
	return newWatcherWithDebounce(root, 2*time.Second)
}

func newWatcherWithDebounce(root string, d time.Duration) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		root:      root,
		debounce:  d,
		fw:        fw,
		events:    make(chan FileEvent, 50),
		synthetic: make(chan FileEvent, 100),
		done:      make(chan struct{}),
	}

	// Recursively add watches for all existing subdirectories.
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		return fw.Add(path)
	}); err != nil {
		fw.Close()
		return nil, err
	}

	go w.run()
	return w, nil
}

// Events returns the channel on which debounced FileEvents are delivered.
func (w *Watcher) Events() <-chan FileEvent { return w.events }

// Close shuts down the watcher and closes the events channel.
func (w *Watcher) Close() error {
	err := w.fw.Close()
	<-w.done
	return err
}

func (w *Watcher) run() {
	defer close(w.events)
	defer close(w.done)

	type pending struct {
		op    FileOp
		timer *time.Timer
	}
	byPath := map[string]*pending{}
	fire := make(chan string, 100)

	for {
		select {
		case e := <-w.synthetic:
			w.events <- e

		case relPath := <-fire:
			if p, ok := byPath[relPath]; ok {
				delete(byPath, relPath)
				w.events <- FileEvent{RelPath: relPath, Op: p.op}
			}

		case event, ok := <-w.fw.Events:
			if !ok {
				for _, p := range byPath {
					p.timer.Stop()
				}
				return
			}

			path := event.Name
			info, statErr := os.Stat(path)

			if statErr == nil && info.IsDir() {
				if event.Has(fsnotify.Create) {
					w.handleNewDir(path)
				}
				continue
			}

			relPath, err := filepath.Rel(w.root, path)
			if err != nil {
				continue
			}
			relPath = filepath.ToSlash(relPath)

			op := mapOp(event.Op)

			if p, ok := byPath[relPath]; ok {
				p.timer.Stop()
				p.op = op
				p.timer = w.startTimer(relPath, fire)
			} else {
				byPath[relPath] = &pending{op: op, timer: w.startTimer(relPath, fire)}
			}

		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			slog.Error("drivesync watcher: fsnotify error", "err", err)
		}
	}
}

func (w *Watcher) startTimer(relPath string, fire chan<- string) *time.Timer {
	return time.AfterFunc(w.debounce, func() {
		fire <- relPath
	})
}

func (w *Watcher) handleNewDir(dirPath string) {
	if err := w.fw.Add(dirPath); err != nil {
		slog.Error("drivesync watcher: adding directory watch", "path", dirPath, "err", err)
		return
	}

	// Scan for files already present and emit synthetic Create events.
	// Runs in a goroutine so that sending to w.synthetic never blocks the run() loop,
	// regardless of how many files the directory contains.
	go func() {
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			fullPath := filepath.Join(dirPath, entry.Name())
			relPath, err := filepath.Rel(w.root, fullPath)
			if err != nil {
				continue
			}
			w.synthetic <- FileEvent{RelPath: filepath.ToSlash(relPath), Op: OpCreate}
		}
	}()
}

func mapOp(op fsnotify.Op) FileOp {
	switch {
	case op.Has(fsnotify.Remove):
		return OpRemove
	case op.Has(fsnotify.Rename):
		return OpRename
	case op.Has(fsnotify.Create):
		return OpCreate
	default:
		return OpWrite
	}
}
