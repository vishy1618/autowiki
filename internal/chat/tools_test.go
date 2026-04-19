package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// newHandlerForTools constructs a Handler with only the dependencies needed
// by the tool helpers (no streamer required).
func newHandlerForTools(t *testing.T) (*Handler, store.ChatStore, *vault.Manager, string) {
	t.Helper()
	cs := store.NewMemChatStore()
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	h := &Handler{store: cs, vault: vm}
	return h, cs, vm, vaultDir
}

// ── storeToolResult ───────────────────────────────────────────────────────────

func TestStoreToolResult_PersistsToolResultRole(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	h.storeToolResult(sess.ID, "tc1", "ok", false)

	msgs, _ := cs.ListMessages(sess.ID)
	var found bool
	for _, m := range msgs {
		if m.Role == "tool_result" {
			found = true
		}
	}
	if !found {
		t.Error("want a tool_result message in the store, found none")
	}
}

func TestStoreToolResult_JSONContainsToolUseID(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	h.storeToolResult(sess.ID, "toolu_abc", "content", false)

	msgs, _ := cs.ListMessages(sess.ID)
	var tr struct {
		ToolUseID string `json:"tool_use_id"`
	}
	for _, m := range msgs {
		if m.Role == "tool_result" {
			_ = json.Unmarshal([]byte(m.Content), &tr)
		}
	}
	if tr.ToolUseID != "toolu_abc" {
		t.Errorf("want tool_use_id %q, got %q", "toolu_abc", tr.ToolUseID)
	}
}

func TestStoreToolResult_SetsIsErrorTrue(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	h.storeToolResult(sess.ID, "tc1", "something failed", true)

	msgs, _ := cs.ListMessages(sess.ID)
	var tr struct {
		IsError bool `json:"is_error"`
	}
	for _, m := range msgs {
		if m.Role == "tool_result" {
			_ = json.Unmarshal([]byte(m.Content), &tr)
		}
	}
	if !tr.IsError {
		t.Error("want is_error=true, got false")
	}
}

func TestStoreToolResult_SetsIsErrorFalse(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	h.storeToolResult(sess.ID, "tc1", "success", false)

	msgs, _ := cs.ListMessages(sess.ID)
	var tr struct {
		IsError bool `json:"is_error"`
	}
	for _, m := range msgs {
		if m.Role == "tool_result" {
			_ = json.Unmarshal([]byte(m.Content), &tr)
		}
	}
	if tr.IsError {
		t.Error("want is_error=false, got true")
	}
}

// ── applyVaultWrites ──────────────────────────────────────────────────────────

func TestApplyVaultWrites_InvalidJSON_StoresErrorResult(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	h.applyVaultWrites(&buf, sess.ID, "tc1", "not json", false, nil)

	msgs, _ := cs.ListMessages(sess.ID)
	var tr struct {
		IsError bool `json:"is_error"`
	}
	for _, m := range msgs {
		if m.Role == "tool_result" {
			_ = json.Unmarshal([]byte(m.Content), &tr)
		}
	}
	if !tr.IsError {
		t.Error("want is_error=true for invalid JSON, got false")
	}
}

func TestApplyVaultWrites_NoPages_StoresErrorResult(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	h.applyVaultWrites(&buf, sess.ID, "tc1", `{"pages":[]}`, false, nil)

	msgs, _ := cs.ListMessages(sess.ID)
	var tr struct {
		IsError bool `json:"is_error"`
	}
	for _, m := range msgs {
		if m.Role == "tool_result" {
			_ = json.Unmarshal([]byte(m.Content), &tr)
		}
	}
	if !tr.IsError {
		t.Error("want is_error=true for empty pages, got false")
	}
}

func TestApplyVaultWrites_ValidPages_WritesVaultFile(t *testing.T) {
	h, cs, _, vaultDir := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	toolJSON := `{"pages":[{"path":"notes/test.md","content":"# Test"}]}`
	h.applyVaultWrites(&buf, sess.ID, "tc1", toolJSON, false, nil)

	content, err := os.ReadFile(filepath.Join(vaultDir, "notes/test.md"))
	if err != nil {
		t.Fatalf("expected vault file to be created: %v", err)
	}
	if string(content) != "# Test" {
		t.Errorf("want content %q, got %q", "# Test", string(content))
	}
}

func TestApplyVaultWrites_ValidPages_EmitsVaultSSEEvent(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	toolJSON := `{"pages":[{"path":"notes/test.md","content":"# Test"}]}`
	h.applyVaultWrites(&buf, sess.ID, "tc1", toolJSON, false, nil)

	out := buf.String()
	if !strings.Contains(out, "event: vault") {
		t.Errorf("want vault SSE event in output, got: %q", out)
	}
	if !strings.Contains(out, "notes/test.md") {
		t.Errorf("want page path in vault SSE event, got: %q", out)
	}
}

func TestApplyVaultWrites_ValidPages_StoresSuccessToolResult(t *testing.T) {
	h, cs, _, _ := newHandlerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	toolJSON := `{"pages":[{"path":"notes/test.md","content":"# Test"}]}`
	h.applyVaultWrites(&buf, sess.ID, "tc1", toolJSON, false, nil)

	msgs, _ := cs.ListMessages(sess.ID)
	var tr struct {
		IsError bool `json:"is_error"`
	}
	for _, m := range msgs {
		if m.Role == "tool_result" {
			_ = json.Unmarshal([]byte(m.Content), &tr)
		}
	}
	if tr.IsError {
		t.Error("want is_error=false for successful write, got true")
	}
}
