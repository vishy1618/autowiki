package vault

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
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

// UpdateAttachmentDescription reads the sidecar for path, sets Description to
// the given text, and writes the sidecar back. Returns an error if the sidecar
// does not yet exist.
func (m *Manager) UpdateAttachmentDescription(path, description string) error {
	meta, err := m.ReadAttachmentMeta(path)
	if err != nil {
		return err
	}
	meta.Description = description
	return m.WriteAttachmentMeta(path, meta)
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
	full, err := m.safePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// AppendLog appends a timestamped entry to log.md in the vault root.
func (m *Manager) AppendLog(entry string) error {
	line := fmt.Sprintf("- %s — %s\n", time.Now().Format(time.RFC3339), entry)
	full, err := m.safePath("log.md")
	if err != nil {
		return err
	}
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
			return nil
		}
		isMD := strings.HasSuffix(d.Name(), ".md")
		isSidecar := strings.HasSuffix(d.Name(), ".meta.json")
		if !isMD && !isSidecar {
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
		// For sidecars, report the attachment path (without .meta.json suffix)
		// so callers can use it directly in ![[...]] links or tool calls.
		if isSidecar {
			relPath = strings.TrimSuffix(relPath, ".meta.json")
		}

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

// safePath joins rel with the vault root, cleans the result, and verifies it
// is still inside the root. Returns an error for any path that would escape
// (e.g. "../../etc/passwd" or an absolute path outside the vault).
func (m *Manager) safePath(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes vault root", rel)
	}
	abs := filepath.Clean(filepath.Join(m.root, rel))
	root := filepath.Clean(m.root)
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes vault root", rel)
	}
	return abs, nil
}

// ReadFile reads a vault-relative .md file and returns its contents.
// Returns an empty string (no error) when the file does not exist.
func (m *Manager) ReadFile(path string) (string, error) {
	full, err := m.safePath(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// VaultEntry describes a single file or directory inside the vault.
type VaultEntry struct {
	Path string
	Type string // "file" or "dir"
	Size int64
}

// ListVault lists files and subdirectories under path (relative to vault root).
// When path is empty, the vault root is used. If recursive is true, all nested
// entries are returned; otherwise only the immediate children are listed.
// Returns an error if path escapes the vault root.
func (m *Manager) ListVault(path string, recursive bool) ([]VaultEntry, error) {
	var base string
	if path == "" {
		base = filepath.Clean(m.root)
	} else {
		var err error
		base, err = m.safePath(path)
		if err != nil {
			return nil, err
		}
	}

	var entries []VaultEntry
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == base {
			return nil // skip the root itself
		}
		rel, _ := filepath.Rel(m.root, p)
		rel = filepath.ToSlash(rel)

		if !recursive && d.IsDir() {
			entries = append(entries, VaultEntry{Path: rel, Type: "dir"})
			return filepath.SkipDir
		}
		if !recursive {
			// non-recursive: only immediate children
			if filepath.Dir(p) != base {
				return nil
			}
			info, _ := d.Info()
			var size int64
			if info != nil {
				size = info.Size()
			}
			entries = append(entries, VaultEntry{Path: rel, Type: "file", Size: size})
			return nil
		}
		// recursive
		if d.IsDir() {
			entries = append(entries, VaultEntry{Path: rel, Type: "dir"})
			return nil
		}
		info, _ := d.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		entries = append(entries, VaultEntry{Path: rel, Type: "file", Size: size})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// DeleteItem deletes a file or directory at the vault-relative path.
// If path is a non-empty directory and recursive is false, an error is returned.
// Returns an error if the path escapes the vault root or if path is empty.
func (m *Manager) DeleteItem(path string, recursive bool) error {
	if path == "" {
		return fmt.Errorf("path must not be empty")
	}
	abs, err := m.safePath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if info.IsDir() && !recursive {
		// Check if empty.
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return fmt.Errorf("directory %q is not empty; use recursive=true to delete", path)
		}
		return os.Remove(abs)
	}
	if recursive {
		return os.RemoveAll(abs)
	}
	return os.Remove(abs)
}

// MoveFile moves a file within the vault, creating parent directories as needed.
// Both from and to are vault-relative paths. Returns an error if either path
// escapes the vault root, is empty, or if the source file does not exist.
func (m *Manager) MoveFile(from, to string) error {
	if from == "" || to == "" {
		return fmt.Errorf("from and to paths must not be empty")
	}
	fromAbs, err := m.safePath(from)
	if err != nil {
		return err
	}
	toAbs, err := m.safePath(to)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o755); err != nil {
		return err
	}
	return os.Rename(fromAbs, toAbs)
}

// ReadFilePartial reads the first maxChars bytes of a vault-relative file.
// Uses io.LimitReader so it works correctly regardless of line structure.
// Returns an error if the path escapes the vault root.
func (m *Manager) ReadFilePartial(path string, maxChars int) (string, error) {
	full, err := m.safePath(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(maxChars)))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// PatchFile replaces the first (and only) occurrence of oldStr in the file at
// path with newStr. Returns an error if oldStr is not found or is ambiguous
// (occurs more than once).
func (m *Manager) PatchFile(path, oldStr, newStr string) error {
	full, err := m.safePath(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return fmt.Errorf("patch_page: %q not found in %s", oldStr, path)
	}
	if count > 1 {
		return fmt.Errorf("patch_page: %q is ambiguous (%d matches) in %s — widen the anchor string", oldStr, count, path)
	}
	patched := strings.Replace(content, oldStr, newStr, 1)
	return os.WriteFile(full, []byte(patched), 0o644)
}

// AppendToSection inserts content immediately before the next heading of the
// same or higher level following heading in the file. If heading is not found,
// the heading and content are appended at the end of the file.
func (m *Manager) AppendToSection(path, heading, content string) error {
	full, err := m.safePath(path)
	if err != nil {
		return err
	}

	var existing string
	if data, readErr := os.ReadFile(full); readErr == nil {
		existing = string(data)
	}

	// Determine the level of the target heading (count leading '#').
	targetLevel := 0
	for _, ch := range heading {
		if ch == '#' {
			targetLevel++
		} else {
			break
		}
	}

	idx := strings.Index(existing, heading)
	if idx == -1 {
		// Heading not present — append heading + content at end.
		sep := "\n"
		if strings.HasSuffix(existing, "\n\n") {
			sep = ""
		} else if strings.HasSuffix(existing, "\n") {
			sep = "\n"
		}
		patched := existing + sep + heading + "\n\n" + content + "\n"
		return os.WriteFile(full, []byte(patched), 0o644)
	}

	// Find the end of the heading line.
	afterHeading := idx + len(heading)
	if afterHeading < len(existing) && existing[afterHeading] == '\n' {
		afterHeading++
	}

	// Scan lines after the heading to find where the section ends — the next
	// heading at the same or higher level (fewer or equal '#').
	rest := existing[afterHeading:]
	lines := strings.Split(rest, "\n")
	insertAt := len(rest) // default: end of file
	for i, line := range lines {
		if i == 0 {
			continue // skip the blank line right after the heading
		}
		if strings.HasPrefix(line, "#") {
			// Count level of this heading.
			level := 0
			for _, ch := range line {
				if ch == '#' {
					level++
				} else {
					break
				}
			}
			if level <= targetLevel {
				// Rebuild offset: sum of lines[0..i-1] lengths + separators.
				insertAt = len(strings.Join(lines[:i], "\n")) + 1 // +1 for the joining \n
				break
			}
		}
	}

	before := existing[:afterHeading] + rest[:insertAt]
	after := rest[insertAt:]
	// Ensure a blank line before the inserted content.
	if !strings.HasSuffix(before, "\n\n") {
		before += "\n"
	}
	patched := before + content + "\n" + after
	return os.WriteFile(full, []byte(patched), 0o644)
}

// contentDisposition returns a Content-Disposition header value for the given
// filename. Plain ASCII filenames (letters, digits, '.', '-', '_') use the
// quoted-string form; all others use RFC 5987 UTF-8 percent-encoding.
func contentDisposition(filename string) string {
	plain := true
	for _, r := range filename {
		if !isAttrChar(r) {
			plain = false
			break
		}
	}
	if plain {
		return fmt.Sprintf(`attachment; filename="%s"`, filename)
	}
	return "attachment; filename*=UTF-8''" + rfc5987Encode(filename)
}

// isAttrChar reports whether r is safe for an unquoted ASCII filename token
// (RFC 5987 attr-char: ALPHA / DIGIT / "!" / "#" / "$" / "&" / "+" / "-" /
// "." / "^" / "_" / "`" / "|" / "~"). We use a conservative subset.
func isAttrChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
}

// rfc5987Encode percent-encodes a UTF-8 string per RFC 5987, leaving
// attr-char bytes unencoded.
func rfc5987Encode(s string) string {
	var b strings.Builder
	for _, bt := range []byte(s) {
		if bt < 128 && isAttrChar(rune(bt)) {
			b.WriteByte(bt)
		} else {
			fmt.Fprintf(&b, "%%%02X", bt)
		}
	}
	return b.String()
}

// ServeFile resolves relPath within the vault and streams the file to w.
// Path traversal attempts receive 400; missing files receive 404.
func (m *Manager) ServeFile(w http.ResponseWriter, r *http.Request, relPath string) {
	abs, err := m.safePath(relPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	f, err := os.Open(abs)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ext := filepath.Ext(relPath)
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = "application/octet-stream"
	}

	filename := filepath.Base(relPath)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	http.ServeContent(w, r, filename, stat.ModTime(), f)
}

// defaultSchema is the template written to schema.md when it does not exist.
const defaultSchema = `# Wiki Schema

## Folders
Lowercase, topic-based. Nest only when a topic grows large.
Examples: ` + "`programming/go.md`" + `, ` + "`science/biology/cell-theory.md`" + `

## Files
Lowercase hyphenated noun phrases. Example: ` + "`go-interfaces.md`" + `

## Links
Use [[wikilinks]] to connect related pages. Link on first meaningful mention.

## Headings
H1 for page title, H2 for major sections, H3 for subsections.

## Style
Concise present-tense prose. Bullet lists for enumerations and steps, not explanations.
`

// EnsureSchema returns the content of schema.md, creating it from the default
// template if it does not already exist.
func (m *Manager) EnsureSchema() (string, error) {
	content, err := m.ReadFile("schema.md")
	if err != nil {
		return "", err
	}
	if content != "" {
		return content, nil
	}
	if err := m.WriteFile("schema.md", defaultSchema); err != nil {
		return "", err
	}
	return defaultSchema, nil
}
