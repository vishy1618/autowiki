package chat

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/suvish/autowiki/internal/store"
)

// vaultWriteInput is the parsed tool input for save_to_vault.
type vaultWriteInput struct {
	Pages []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"pages"`
}

// applyVaultWrites parses the tool JSON, writes each page, appends the log,
// emits a vault SSE event, and stores a tool_result message.
func (h *Handler) applyVaultWrites(w io.Writer, sessionID, toolUseID, toolJSON string, canFlush bool, flusher http.Flusher) {
	var input vaultWriteInput
	if err := json.Unmarshal([]byte(toolJSON), &input); err != nil {
		h.storeToolResult(sessionID, toolUseID, "invalid tool input", true)
		return
	}
	if len(input.Pages) == 0 {
		h.storeToolResult(sessionID, toolUseID, "no pages provided", true)
		return
	}

	var changed []string
	for _, page := range input.Pages {
		if err := h.vault.WriteFile(page.Path, page.Content); err == nil {
			changed = append(changed, page.Path)
		}
	}
	if len(changed) == 0 {
		h.storeToolResult(sessionID, toolUseID, "all page writes failed", true)
		return
	}

	_ = h.vault.AppendLog(fmt.Sprintf("wrote %s", strings.Join(changed, ", ")))
	h.storeToolResult(sessionID, toolUseID, fmt.Sprintf("saved: %s", strings.Join(changed, ", ")), false)

	type change struct {
		Path string `json:"path"`
	}
	changes := make([]change, len(changed))
	for i, p := range changed {
		changes[i] = change{Path: p}
	}
	payload, err := json.Marshal(map[string]any{"changes": changes})
	if err != nil {
		return
	}
	writeSSE(w, "vault", string(payload))
	if canFlush {
		flusher.Flush()
	}
}

// dispatchToolCalls executes each tool call, emits SSE status/vault events, and
// stores tool_result messages. Returns true when save_to_vault was called (the
// caller should emit "done" and stop the agentic loop).
func (h *Handler) dispatchToolCalls(w io.Writer, sessionID string, toolCalls []toolCall, canFlush bool, flusher http.Flusher) bool {
	flush := func() {
		if canFlush {
			flusher.Flush()
		}
	}
	hasSaveToVault := false
	for _, tc := range toolCalls {
		switch tc.name {
		case "save_to_vault":
			hasSaveToVault = true
			h.applyVaultWrites(w, sessionID, tc.id, tc.json, canFlush, flusher)

		case "read_page":
			var input struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal([]byte(tc.json), &input)
			writeSSE(w, "status", fmt.Sprintf(`{"message":"Reading %s\u2026"}`, input.Path))
			flush()
			content, _ := h.vault.ReadFile(input.Path)
			h.storeToolResult(sessionID, tc.id, content, false)

		case "search_vault":
			var input struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal([]byte(tc.json), &input)
			writeSSE(w, "status", fmt.Sprintf(`{"message":"Searching for %s\u2026"}`, input.Query))
			flush()
			results, _ := h.vault.SearchPages(input.Query, 10)
			resultJSON, _ := json.Marshal(results)
			h.storeToolResult(sessionID, tc.id, string(resultJSON), false)

		case "list_vault":
			var input struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive"`
			}
			_ = json.Unmarshal([]byte(tc.json), &input)
			writeSSE(w, "status", `{"message":"Listing vault\u2026"}`)
			flush()
			entries, _ := h.vault.ListVault(input.Path, input.Recursive)
			resultJSON, _ := json.Marshal(entries)
			h.storeToolResult(sessionID, tc.id, string(resultJSON), false)

		case "read_page_partial":
			var input struct {
				Path     string `json:"path"`
				MaxChars int    `json:"max_chars"`
			}
			_ = json.Unmarshal([]byte(tc.json), &input)
			writeSSE(w, "status", fmt.Sprintf(`{"message":"Reading %s\u2026"}`, input.Path))
			flush()
			content, _ := h.vault.ReadFilePartial(input.Path, input.MaxChars)
			h.storeToolResult(sessionID, tc.id, content, false)

		case "move_page":
			var input struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if err := json.Unmarshal([]byte(tc.json), &input); err != nil {
				slog.Warn("move_page: bad tool JSON", "err", err, "json", tc.json)
			}
			writeSSE(w, "status", fmt.Sprintf(`{"message":"Moving %s\u2026"}`, input.From))
			flush()
			if err := h.vault.MoveFile(input.From, input.To); err != nil {
				h.storeToolResult(sessionID, tc.id, err.Error(), true)
			} else {
				_ = h.vault.AppendLog(fmt.Sprintf("moved %s → %s", input.From, input.To))
				h.storeToolResult(sessionID, tc.id, fmt.Sprintf("moved %s to %s", input.From, input.To), false)
				payload, _ := json.Marshal(map[string]any{"action": "moved", "from": input.From, "to": input.To})
				writeSSE(w, "vault", string(payload))
				flush()
			}

		case "save_attachment_notes":
			var input struct {
				Path  string `json:"path"`
				Notes string `json:"notes"`
			}
			if err := json.Unmarshal([]byte(tc.json), &input); err != nil {
				slog.Warn("save_attachment_notes: bad tool JSON", "err", err, "json", tc.json)
				h.storeToolResult(sessionID, tc.id, "invalid tool input: "+err.Error(), true)
				continue
			}
			if err := h.vault.UpdateAttachmentDescription(input.Path, input.Notes); err != nil {
				h.storeToolResult(sessionID, tc.id, err.Error(), true)
			} else {
				h.storeToolResult(sessionID, tc.id, "notes saved for "+input.Path, false)
			}

		case "delete_item":
			var input struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive"`
			}
			if err := json.Unmarshal([]byte(tc.json), &input); err != nil {
				slog.Warn("delete_item: bad tool JSON", "err", err, "json", tc.json)
			}
			slog.Debug("delete_item: dispatching", "raw_json", tc.json, "path", input.Path, "recursive", input.Recursive)
			writeSSE(w, "status", fmt.Sprintf(`{"message":"Deleting %s\u2026"}`, input.Path))
			flush()
			if err := h.vault.DeleteItem(input.Path, input.Recursive); err != nil {
				slog.Debug("delete_item: vault error", "path", input.Path, "recursive", input.Recursive, "err", err)
				h.storeToolResult(sessionID, tc.id, err.Error(), true)
			} else {
				_ = h.vault.AppendLog(fmt.Sprintf("deleted %s", input.Path))
				h.storeToolResult(sessionID, tc.id, fmt.Sprintf("deleted %s", input.Path), false)
				payload, _ := json.Marshal(map[string]any{"action": "deleted", "path": input.Path})
				writeSSE(w, "vault", string(payload))
				flush()
			}

		case "search_chat_history":
			var input struct {
				Query  string `json:"query"`
				Offset int    `json:"offset"`
			}
			_ = json.Unmarshal([]byte(tc.json), &input)
			writeSSE(w, "status", `{"message":"Searching chat history\u2026"}`)
			flush()
			results, _ := h.store.SearchMessages(input.Query, input.Offset, 3)
			resultJSON, _ := json.Marshal(results)
			h.storeToolResult(sessionID, tc.id, string(resultJSON), false)

		default:
			h.storeToolResult(sessionID, tc.id, "unknown tool: "+tc.name, true)
		}
	}
	return hasSaveToVault
}

// storeToolResult persists a tool_result message for the given tool_use_id.
func (h *Handler) storeToolResult(sessionID, toolUseID, content string, isError bool) {
	tr, err := json.Marshal(map[string]any{
		"tool_use_id": toolUseID,
		"content":     content,
		"is_error":    isError,
	})
	if err != nil {
		return
	}
	_ = h.store.AppendMessage(store.Message{
		SessionID: sessionID,
		Role:      "tool_result",
		Content:   string(tr),
	})
}
