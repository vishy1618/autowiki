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

// newRunnerForTools constructs an AgenticRunner with only the dependencies
// needed by the tool helpers (no streamer required).
func newRunnerForTools(t *testing.T) (*AgenticRunner, store.ChatStore, *vault.Manager, string) {
	t.Helper()
	cs := store.NewMemChatStore()
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	r := NewAgenticRunner(nil, cs, vm)
	return r, cs, vm, vaultDir
}

// toolResultFromStore returns the first tool_result message in the session
// decoded into a map, or fails the test if none found.
func toolResultFromStore(t *testing.T, cs store.ChatStore, sessionID string) map[string]any {
	t.Helper()
	msgs, _ := cs.ListMessages(sessionID)
	for _, m := range msgs {
		if m.Role == "tool_result" {
			var tr map[string]any
			if err := json.Unmarshal([]byte(m.Content), &tr); err != nil {
				t.Fatalf("unmarshal tool_result: %v", err)
			}
			return tr
		}
	}
	t.Fatal("no tool_result message found in store")
	return nil
}

// ── storeToolResult ───────────────────────────────────────────────────────────

func TestStoreToolResult_PersistsToolResultRole(t *testing.T) {
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	r.storeToolResult(sess.ID, "tc1", "ok", false)

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
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	r.storeToolResult(sess.ID, "toolu_abc", "content", false)

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
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	r.storeToolResult(sess.ID, "tc1", "something failed", true)

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
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	r.storeToolResult(sess.ID, "tc1", "success", false)

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
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.applyVaultWrites(&buf, sess.ID, "tc1", "not json", false, nil)

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
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.applyVaultWrites(&buf, sess.ID, "tc1", `{"pages":[]}`, false, nil)

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
	r, cs, _, vaultDir := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	toolJSON := `{"pages":[{"path":"notes/test.md","content":"# Test"}]}`
	r.applyVaultWrites(&buf, sess.ID, "tc1", toolJSON, false, nil)

	content, err := os.ReadFile(filepath.Join(vaultDir, "notes/test.md"))
	if err != nil {
		t.Fatalf("expected vault file to be created: %v", err)
	}
	if string(content) != "# Test" {
		t.Errorf("want content %q, got %q", "# Test", string(content))
	}
}

func TestApplyVaultWrites_ValidPages_EmitsVaultSSEEvent(t *testing.T) {
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	toolJSON := `{"pages":[{"path":"notes/test.md","content":"# Test"}]}`
	r.applyVaultWrites(&buf, sess.ID, "tc1", toolJSON, false, nil)

	out := buf.String()
	if !strings.Contains(out, "event: vault") {
		t.Errorf("want vault SSE event in output, got: %q", out)
	}
	if !strings.Contains(out, "notes/test.md") {
		t.Errorf("want page path in vault SSE event, got: %q", out)
	}
}

func TestApplyVaultWrites_ValidPages_StoresSuccessToolResult(t *testing.T) {
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	toolJSON := `{"pages":[{"path":"notes/test.md","content":"# Test"}]}`
	r.applyVaultWrites(&buf, sess.ID, "tc1", toolJSON, false, nil)

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

// ── dispatchToolCalls: vault event payload shape ──────────────────────────────

func TestDispatch_PatchPage_VaultEventHasChangesPayload(t *testing.T) {
	r, cs, vm, _ := newRunnerForTools(t)
	_ = vm.WriteFile("notes.md", "old line")
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "patch_page", json: `{"path":"notes.md","old_str":"old line","new_str":"new line"}`},
	}, false, nil)

	out := buf.String()
	if !strings.Contains(out, `"changes"`) {
		t.Errorf("vault event missing 'changes' field, got: %q", out)
	}
	if !strings.Contains(out, `"notes.md"`) {
		t.Errorf("vault event missing path in changes, got: %q", out)
	}
}

func TestDispatch_SaveAttachmentNotes_EmitsVaultEventWithChangesPayload(t *testing.T) {
	r, cs, vm, _ := newRunnerForTools(t)
	attachPath := "_attachments/report.pdf"
	_ = vm.WriteFile(attachPath, "%PDF")
	_ = vm.WriteAttachmentMeta(attachPath, vault.AttachmentMeta{
		ID: "att1", OriginalName: "report.pdf", MediaType: "application/pdf",
	})
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "save_attachment_notes", json: `{"path":"_attachments/report.pdf","notes":"key facts"}`},
	}, false, nil)

	out := buf.String()
	if !strings.Contains(out, "event: vault") {
		t.Errorf("want vault SSE event for save_attachment_notes, got: %q", out)
	}
	if !strings.Contains(out, `"changes"`) {
		t.Errorf("vault event missing 'changes' field, got: %q", out)
	}
	if !strings.Contains(out, `_attachments/report.pdf`) {
		t.Errorf("vault event missing attachment path in changes, got: %q", out)
	}
}

func TestDispatch_DeleteItem_VaultEventHasChangesPayload(t *testing.T) {
	r, cs, vm, _ := newRunnerForTools(t)
	_ = vm.WriteFile("trash.md", "delete me")
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "delete_item", json: `{"path":"trash.md","recursive":false}`},
	}, false, nil)

	out := buf.String()
	if !strings.Contains(out, `"changes"`) {
		t.Errorf("vault event missing 'changes' field, got: %q", out)
	}
	if !strings.Contains(out, `"trash.md"`) {
		t.Errorf("vault event missing path in changes, got: %q", out)
	}
}

func TestDispatch_MovePage_VaultEventHasChangesPayload(t *testing.T) {
	r, cs, vm, _ := newRunnerForTools(t)
	_ = vm.WriteFile("old.md", "content")
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "move_page", json: `{"from":"old.md","to":"new.md"}`},
	}, false, nil)

	out := buf.String()
	if !strings.Contains(out, `"changes"`) {
		t.Errorf("vault event missing 'changes' field, got: %q", out)
	}
	if !strings.Contains(out, `"new.md"`) {
		t.Errorf("vault event missing destination path in changes, got: %q", out)
	}
}

func TestDispatch_AppendToSection_VaultEventHasChangesPayload(t *testing.T) {
	r, cs, vm, _ := newRunnerForTools(t)
	_ = vm.WriteFile("notes.md", "# Notes\n\n## Log\n\nexisting\n")
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "append_to_section", json: `{"path":"notes.md","heading":"## Log","content":"- new"}`},
	}, false, nil)

	out := buf.String()
	if !strings.Contains(out, `"changes"`) {
		t.Errorf("vault event missing 'changes' field, got: %q", out)
	}
	if !strings.Contains(out, `"notes.md"`) {
		t.Errorf("vault event missing path in changes, got: %q", out)
	}
}

// ── dispatchToolCalls: read tool error propagation ────────────────────────────

func TestDispatch_SearchVault_StoresErrorResultOnVaultFailure(t *testing.T) {
	cs := store.NewMemChatStore()
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	r := NewAgenticRunner(nil, cs, vm)
	sess, _ := cs.ResolveSession()

	// Remove the vault root so WalkDir fails.
	os.RemoveAll(vaultDir)

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "search_vault", json: `{"query":"anything"}`},
	}, false, nil)

	tr := toolResultFromStore(t, cs, sess.ID)
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Errorf("want is_error=true when vault search fails, got result: %v", tr)
	}
}

func TestDispatch_ListVault_StoresErrorResultOnVaultFailure(t *testing.T) {
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "list_vault", json: `{"path":"../../escape"}`},
	}, false, nil)

	tr := toolResultFromStore(t, cs, sess.ID)
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Errorf("want is_error=true when vault list fails, got result: %v", tr)
	}
}

func TestDispatch_ReadPagePartial_StoresErrorResultOnVaultFailure(t *testing.T) {
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "read_page_partial", json: `{"path":"../../escape.md","max_chars":100}`},
	}, false, nil)

	tr := toolResultFromStore(t, cs, sess.ID)
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Errorf("want is_error=true when vault partial read fails, got result: %v", tr)
	}
}

func TestDispatch_ReadPage_StoresErrorResultOnVaultFailure(t *testing.T) {
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "read_page", json: `{"path":"../../escape.md"}`},
	}, false, nil)

	tr := toolResultFromStore(t, cs, sess.ID)
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Errorf("want is_error=true when vault read fails, got result: %v", tr)
	}
}

func TestDispatch_ReadPage_StoresErrorResultWhenFileDoesNotExist(t *testing.T) {
	r, cs, _, _ := newRunnerForTools(t)
	sess, _ := cs.ResolveSession()

	var buf strings.Builder
	r.dispatchToolCalls(&buf, sess.ID, []toolCall{
		{id: "tc1", name: "read_page", json: `{"path":"no/such/page.md"}`},
	}, false, nil)

	tr := toolResultFromStore(t, cs, sess.ID)
	if isErr, _ := tr["is_error"].(bool); !isErr {
		t.Errorf("want is_error=true for missing file, got result: %v", tr)
	}
}
