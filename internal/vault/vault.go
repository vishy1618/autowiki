package vault

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
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

// AttachmentMeta holds metadata for an uploaded attachment, stored as a
// sidecar JSON file next to the attachment in _attachments/.
type AttachmentMeta struct {
	ID           string    `json:"id"`
	OriginalName string    `json:"original_name"`
	MediaType    string    `json:"media_type"`
	Description  string    `json:"description"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

// SaveAttachment writes data to _attachments/ under a collision-safe filename
// derived from originalName and returns the vault-relative path.
// Filename format: {stem}-{yyyyMMdd}-{6-char-hex}.{ext}
func (m *Manager) SaveAttachment(originalName string, data []byte) (string, error) {
	ext := filepath.Ext(originalName)
	stem := strings.TrimSuffix(originalName, ext)
	// Replace characters unsafe in filenames.
	stem = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, stem)

	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating random suffix: %w", err)
	}
	suffix := hex.EncodeToString(buf[:])
	date := time.Now().Format("20060102")
	filename := fmt.Sprintf("%s-%s-%s%s", stem, date, suffix, ext)
	relPath := filepath.Join("_attachments", filename)

	full := filepath.Join(m.root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return relPath, nil
}

// WriteAttachmentMeta writes a sidecar JSON file at {path}.meta.json.
func (m *Manager) WriteAttachmentMeta(path string, meta AttachmentMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.root, path+".meta.json"), data, 0o644)
}

// ReadAttachmentMeta reads the sidecar JSON file for the given vault-relative path.
func (m *Manager) ReadAttachmentMeta(path string) (AttachmentMeta, error) {
	data, err := os.ReadFile(filepath.Join(m.root, path+".meta.json"))
	if err != nil {
		return AttachmentMeta{}, err
	}
	var meta AttachmentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return AttachmentMeta{}, err
	}
	return meta, nil
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

// ReadAttachmentData reads the raw bytes of a saved attachment by its
// vault-relative path (e.g. "_attachments/doc-20260413-abc123.pdf").
func (m *Manager) ReadAttachmentData(path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(m.root, path))
}

// SearchResult holds a single result from SearchPages.
type SearchResult struct {
	Path    string
	Snippet string
}

// SearchPages performs a case-insensitive substring search across all .md files
// in the vault (excluding _attachments/). It returns up to maxResults hits, each
// with the vault-relative path and a snippet (matching line ± 2 lines, trimmed
// to 300 chars).
func (m *Manager) SearchPages(query string, maxResults int) ([]SearchResult, error) {
	queryLower := strings.ToLower(query)
	var results []SearchResult

	err := filepath.WalkDir(m.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip _attachments directory entirely.
			if d.Name() == "_attachments" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		if len(results) >= maxResults {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		relPath, err := filepath.Rel(m.root, path)
		if err != nil {
			return nil
		}
		// Normalise to forward slashes.
		relPath = filepath.ToSlash(relPath)

		lines := splitLines(data)
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), queryLower) {
				snippet := buildSnippet(lines, i, 2, 300)
				results = append(results, SearchResult{Path: relPath, Snippet: snippet})
				if len(results) >= maxResults {
					return nil
				}
				break // one result per file
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// splitLines splits data into a slice of lines without trailing newlines.
func splitLines(data []byte) []string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// buildSnippet returns lines[i-context : i+context+1] joined by newline,
// clamped to the slice bounds and trimmed to maxChars.
func buildSnippet(lines []string, i, context, maxChars int) string {
	lo := i - context
	if lo < 0 {
		lo = 0
	}
	hi := i + context + 1
	if hi > len(lines) {
		hi = len(lines)
	}
	snippet := strings.Join(lines[lo:hi], "\n")
	if len(snippet) > maxChars {
		// Trim to maxChars on a rune boundary.
		runes := []rune(snippet)
		if len(runes) > maxChars {
			runes = runes[:maxChars]
		}
		snippet = strings.TrimRightFunc(string(runes), unicode.IsSpace)
	}
	return snippet
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
