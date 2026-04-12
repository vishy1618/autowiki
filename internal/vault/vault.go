package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Manager provides read/write access to an Obsidian vault rooted at a
// directory on the local filesystem.
type Manager struct {
	root string
}

// NewManager returns a Manager backed by the given root directory.
func NewManager(root string) *Manager {
	return &Manager{root: root}
}

// WriteFile writes content to a vault-relative path, creating parent
// directories as needed. It overwrites any existing file at that path.
func (m *Manager) WriteFile(path, content string) error {
	full := filepath.Join(m.root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// AppendLog appends a timestamped entry to log.md in the vault root.
func (m *Manager) AppendLog(entry string) error {
	line := fmt.Sprintf("- %s — %s\n", time.Now().Format(time.RFC3339), entry)
	full := filepath.Join(m.root, "log.md")
	f, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

// ReadIndex is a convenience wrapper that reads index.md from the vault root.
func (m *Manager) ReadIndex() (string, error) {
	return m.ReadFile("index.md")
}

// ReadFile reads a vault-relative .md file and returns its contents.
// Returns an empty string (no error) when the file does not exist.
func (m *Manager) ReadFile(path string) (string, error) {
	full := filepath.Join(m.root, path)
	data, err := os.ReadFile(full)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}
