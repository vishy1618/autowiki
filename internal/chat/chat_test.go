package chat_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// stubStreamer is a fake llm.Streamer that returns a fixed SSE body.
type stubStreamer struct {
	body              string
	err               error
	capturedMsgs      []store.Message  // last call's messages
	capturedAttachments []llm.Attachment // last call's PDF attachments
}

func (s *stubStreamer) Stream(_ context.Context, msgs []store.Message, _ string, attachments []llm.Attachment) (io.ReadCloser, error) {
	s.capturedMsgs = msgs
	s.capturedAttachments = attachments
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(strings.NewReader(s.body)), nil
}

// minimalAnthropicSSE is the smallest valid streaming response containing one
// text delta and a message_stop.
const minimalAnthropicSSE = `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

event: message_stop
data: {"type":"message_stop"}

`

// toolUseNoTextAnthropicSSE is a streaming response where the model calls the
// save_to_vault tool directly with no text preamble.
const toolUseNoTextAnthropicSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_silent","name":"save_to_vault","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"pages\":[{\"path\":\"notes/silent.md\",\"content\":\"# Silent\"}]}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

// toolUseAnthropicSSE is a streaming response where the model emits a text
// reply AND calls the save_to_vault tool.
const toolUseAnthropicSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Saving that for you."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_abc","name":"save_to_vault","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pages\":[{\"path\":\"notes/test.md\",\"content\":\"# Test\"}]}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}

`

func newTestHandler(t *testing.T, streamer chat.Streamer) http.Handler {
	t.Helper()
	cs := store.NewMemChatStore()
	vm := vault.NewManager(t.TempDir())
	return chat.NewHandler(cs, streamer, vm)
}

func TestHandler_PostChat_StreamsDeltaAndDoneEvents(t *testing.T) {
	// Arrange
	h := newTestHandler(t, &stubStreamer{body: minimalAnthropicSSE})
	form := url.Values{"message": {"hello"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — response must be SSE
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Parse SSE events from the response body.
	events := parseSSE(t, w.Body.String())

	// Must have at least one delta event with text.
	hasDelta := false
	for _, ev := range events {
		if ev.event == "delta" && strings.Contains(ev.data, `"text"`) {
			hasDelta = true
			break
		}
	}
	if !hasDelta {
		t.Errorf("expected a delta SSE event, got events: %v", events)
	}

	// Must end with a done event containing a session_id.
	if len(events) == 0 || events[len(events)-1].event != "done" {
		t.Errorf("expected last event to be 'done', got events: %v", events)
	}
	doneData := events[len(events)-1].data
	if !strings.Contains(doneData, "session_id") {
		t.Errorf("done event missing session_id, got: %q", doneData)
	}
}

func TestHandler_PostChat_MissingMessage_Returns400(t *testing.T) {
	// Arrange — no message field
	h := newTestHandler(t, &stubStreamer{body: minimalAnthropicSSE})
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_PostChat_PersistsUserAndAssistantMessages(t *testing.T) {
	// Arrange
	cs := store.NewMemChatStore()
	vm := vault.NewManager(t.TempDir())
	h := chat.NewHandler(cs, &stubStreamer{body: minimalAnthropicSSE}, vm)
	form := url.Values{"message": {"remember this"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — session should have two messages: user + assistant
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	msgs, err := cs.ListMessages(session.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", msgs[0].Role)
	}
	if msgs[0].Content != "remember this" {
		t.Errorf("expected user content 'remember this', got %q", msgs[0].Content)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %q", msgs[1].Role)
	}
	if msgs[1].Content == "" {
		t.Error("expected non-empty assistant content")
	}
}

func TestHandler_PostChat_WritesVaultAndEmitsVaultEvent(t *testing.T) {
	// Arrange
	cs := store.NewMemChatStore()
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	h := chat.NewHandler(cs, &stubStreamer{body: toolUseAnthropicSSE}, vm)

	form := url.Values{"message": {"I learned about Go interfaces"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — vault file was written
	content, err := os.ReadFile(filepath.Join(vaultDir, "notes/test.md"))
	if err != nil {
		t.Fatalf("expected vault file to be created: %v", err)
	}
	if string(content) != "# Test" {
		t.Errorf("unexpected vault content: %q", string(content))
	}

	// Assert — vault SSE event emitted
	events := parseSSE(t, w.Body.String())
	hasVault := false
	for _, ev := range events {
		if ev.event == "vault" && strings.Contains(ev.data, "notes/test.md") {
			hasVault = true
			break
		}
	}
	if !hasVault {
		t.Errorf("expected vault SSE event with path, got events: %v", events)
	}
}

func TestHandler_PostChat_NoVaultEventWhenNoToolCall(t *testing.T) {
	// Arrange — streamer returns text-only response (no tool call)
	h := newTestHandler(t, &stubStreamer{body: minimalAnthropicSSE})
	form := url.Values{"message": {"hello"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — no vault event
	events := parseSSE(t, w.Body.String())
	for _, ev := range events {
		if ev.event == "vault" {
			t.Errorf("unexpected vault SSE event: %v", ev)
		}
	}
}

func TestHandler_PostChat_InjectsAttachmentDescriptionIntoContext(t *testing.T) {
	// Arrange — write an attachment sidecar so the handler can look it up.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	attachPath, _ := vm.SaveAttachment("photo.png", []byte("x"))
	_ = vm.WriteAttachmentMeta(attachPath, vault.AttachmentMeta{
		ID:           "att_photo",
		OriginalName: "photo.png",
		MediaType:    "image/png",
		Description:  "a sunset over mountains",
	})

	cs := store.NewMemChatStore()
	streamer := &stubStreamer{body: minimalAnthropicSSE}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{
		"message":        {"What do you see?"},
		"attachment_ids": {attachPath},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — the streamer must have received both the description AND the
	// vault-relative path in the message sent to the LLM, so it can embed
	// the image using Obsidian ![[path]] syntax when saving to the vault.
	var userContent string
	for _, msg := range streamer.capturedMsgs {
		if msg.Role == "user" {
			userContent = msg.Content
			break
		}
	}
	if !strings.Contains(userContent, "a sunset over mountains") {
		t.Errorf("expected attachment description in LLM context; user message: %q", userContent)
	}
	if !strings.Contains(userContent, attachPath) {
		t.Errorf("expected vault path %q in LLM context; user message: %q", attachPath, userContent)
	}
}

func TestHandler_PostChat_NoEmptyTextBlockWhenLLMCallsToolWithoutText(t *testing.T) {
	// When the LLM calls save_to_vault with no text preamble, the stored
	// assistant content must NOT contain an empty text block — Anthropic
	// rejects messages with empty text blocks on subsequent turns (400).
	cs := store.NewMemChatStore()
	vm := vault.NewManager(t.TempDir())
	h := chat.NewHandler(cs, &stubStreamer{body: toolUseNoTextAnthropicSSE}, vm)

	form := url.Values{"message": {"save quietly"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	session, _ := cs.ResolveSession()
	msgs, _ := cs.ListMessages(session.ID)

	// Find the assistant message.
	var assistantContent string
	for _, m := range msgs {
		if m.Role == "assistant" {
			assistantContent = m.Content
		}
	}

	// If content is a JSON array, none of the blocks should be an empty text block.
	if len(assistantContent) > 0 && assistantContent[0] == '[' {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(assistantContent), &blocks); err != nil {
			t.Fatalf("parsing assistant content: %v", err)
		}
		for _, b := range blocks {
			if b.Type == "text" && b.Text == "" {
				t.Errorf("stored assistant content contains empty text block: %v", blocks)
			}
		}
	}
}

func TestHandler_PostChat_StoresAssistantContentBlocksAndToolResultAfterVaultWrite(t *testing.T) {
	// When the LLM calls save_to_vault, the handler must:
	// (a) store the assistant message with the full content-block array so the
	//     conversation history includes the tool_use block, and
	// (b) store a tool_result message so Anthropic knows the outcome on the
	//     next turn.
	cs := store.NewMemChatStore()
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	h := chat.NewHandler(cs, &stubStreamer{body: toolUseAnthropicSSE}, vm)

	form := url.Values{"message": {"I learned about Go interfaces"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	session, _ := cs.ResolveSession()
	msgs, _ := cs.ListMessages(session.ID)

	// Expect: user, assistant, tool_result
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, tool_result), got %d: %v", len(msgs), msgs)
	}

	// (a) Assistant message must be a JSON array containing a tool_use block.
	assistantMsg := msgs[1]
	if assistantMsg.Role != "assistant" {
		t.Fatalf("expected assistant role, got %q", assistantMsg.Role)
	}
	if len(assistantMsg.Content) == 0 || assistantMsg.Content[0] != '[' {
		t.Errorf("expected assistant content to be a JSON array, got %q", assistantMsg.Content)
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(assistantMsg.Content), &blocks); err != nil {
		t.Fatalf("parsing assistant content blocks: %v", err)
	}
	hasToolUse := false
	for _, b := range blocks {
		if b.Type == "tool_use" {
			hasToolUse = true
		}
	}
	if !hasToolUse {
		t.Errorf("expected tool_use block in assistant content, got: %v", blocks)
	}

	// (b) tool_result message must reference the tool_use and report success.
	toolResult := msgs[2]
	if toolResult.Role != "tool_result" {
		t.Fatalf("expected tool_result role, got %q", toolResult.Role)
	}
	var tr struct {
		ToolUseID string `json:"tool_use_id"`
		IsError   bool   `json:"is_error"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(toolResult.Content), &tr); err != nil {
		t.Fatalf("parsing tool_result content: %v", err)
	}
	if tr.ToolUseID == "" {
		t.Error("expected non-empty tool_use_id in tool_result")
	}
	if tr.IsError {
		t.Error("expected is_error=false for successful write")
	}
	if tr.Content == "" {
		t.Error("expected non-empty content in tool_result (saved paths)")
	}
}

func TestHandler_PostChat_PersistsEmptyAssistantMessageOnLLMFailure(t *testing.T) {
	// Arrange — streamer always fails so the LLM is unavailable.
	cs := store.NewMemChatStore()
	vm := vault.NewManager(t.TempDir())
	h := chat.NewHandler(cs, &stubStreamer{err: errors.New("llm down")}, vm)

	form := url.Values{"message": {"hello"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — handler returns 500.
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}

	// Assert — store contains two messages: the user message and an empty
	// assistant placeholder. Without the placeholder the next request would
	// send two consecutive user messages, which Anthropic rejects with 400.
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	msgs, err := cs.ListMessages(session.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user + empty assistant), got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %q", msgs[1].Role)
	}
	if msgs[1].Content != "" {
		t.Errorf("expected empty assistant content, got %q", msgs[1].Content)
	}
}

func TestHandler_PostChat_UnknownAttachmentIDIsSkipped(t *testing.T) {
	// Arrange — attachment ID that has no sidecar in the vault.
	h := newTestHandler(t, &stubStreamer{body: minimalAnthropicSSE})
	form := url.Values{
		"message":        {"hello"},
		"attachment_ids": {"_attachments/nonexistent-20260413-aaaaaa.png"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — request succeeds; unknown attachment is silently skipped.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandler_PostChat_SendsPdfAttachmentInlineToStreamer(t *testing.T) {
	// Arrange — save a real PDF file to the vault and write its sidecar.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	pdfData := []byte("%PDF-1.4 fake content")
	attachPath, _ := vm.SaveAttachment("doc.pdf", pdfData)
	_ = vm.WriteAttachmentMeta(attachPath, vault.AttachmentMeta{
		ID:           "att_doc",
		OriginalName: "doc.pdf",
		MediaType:    "application/pdf",
	})

	cs := store.NewMemChatStore()
	streamer := &stubStreamer{body: minimalAnthropicSSE}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{
		"message":        {"summarise this"},
		"attachment_ids": {attachPath},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — the streamer received the PDF as an llm.Attachment.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(streamer.capturedAttachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(streamer.capturedAttachments))
	}
	att := streamer.capturedAttachments[0]
	if att.MediaType != "application/pdf" {
		t.Errorf("want media type %q, got %q", "application/pdf", att.MediaType)
	}
	if string(att.Data) != string(pdfData) {
		t.Errorf("attachment data mismatch: want %q, got %q", pdfData, att.Data)
	}

	// The user message text should reference the file by name.
	var userContent string
	for _, msg := range streamer.capturedMsgs {
		if msg.Role == "user" {
			userContent = msg.Content
			break
		}
	}
	if !strings.Contains(userContent, "doc.pdf") {
		t.Errorf("expected filename in user message context; got %q", userContent)
	}
}

// ── agentic loop fixtures ─────────────────────────────────────────────────────

// readPageToolUseSSE is a streaming response where the model calls read_page.
const readPageToolUseSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_rp1","name":"read_page","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"programming/go.md\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

// searchVaultToolUseSSE is a streaming response where the model calls search_vault.
const searchVaultToolUseSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_sv1","name":"search_vault","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"Go interfaces\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

// multiResponseStreamer returns a different SSE body for each successive call.
// Once all bodies are consumed it repeats the last one.
type multiResponseStreamer struct {
	bodies []string
	idx    int
}

func (s *multiResponseStreamer) Stream(_ context.Context, _ []store.Message, _ string, _ []llm.Attachment) (io.ReadCloser, error) {
	body := s.bodies[len(s.bodies)-1] // default: last body
	if s.idx < len(s.bodies) {
		body = s.bodies[s.idx]
		s.idx++
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func TestHandler_PostChat_EmitsStatusEventOnReadPage(t *testing.T) {
	// Arrange — vault has the page the LLM will read.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("programming/go.md", "# Go\nGo has interfaces.")

	cs := store.NewMemChatStore()
	// First call: model reads a page. Second call: model answers (text only).
	streamer := &multiResponseStreamer{bodies: []string{readPageToolUseSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"tell me about Go"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — status event with the page path was emitted.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	events := parseSSE(t, w.Body.String())
	hasStatus := false
	for _, ev := range events {
		if ev.event == "status" && strings.Contains(ev.data, "programming/go.md") {
			hasStatus = true
			break
		}
	}
	if !hasStatus {
		t.Errorf("expected status SSE event mentioning page path, got events: %v", events)
	}
}

func TestHandler_PostChat_EmitsStatusEventOnSearchVault(t *testing.T) {
	// Arrange — vault has a searchable page.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("programming/go.md", "Go interfaces are structural.")

	cs := store.NewMemChatStore()
	// First call: model searches vault. Second call: model answers.
	streamer := &multiResponseStreamer{bodies: []string{searchVaultToolUseSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"what do I know about Go interfaces?"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — status event emitted for the search.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	events := parseSSE(t, w.Body.String())
	hasStatus := false
	for _, ev := range events {
		if ev.event == "status" && strings.Contains(ev.data, "Go interfaces") {
			hasStatus = true
			break
		}
	}
	if !hasStatus {
		t.Errorf("expected status SSE event mentioning search query, got events: %v", events)
	}
}

func TestHandler_PostChat_LoopsUntilNoToolCall(t *testing.T) {
	// Arrange — model reads a page then answers without a tool call.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("notes.md", "some notes")

	cs := store.NewMemChatStore()
	streamer := &multiResponseStreamer{bodies: []string{readPageToolUseSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"what are my notes?"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — handler completed normally with a done event.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	events := parseSSE(t, w.Body.String())
	if len(events) == 0 || events[len(events)-1].event != "done" {
		t.Errorf("expected last event to be done, got: %v", events)
	}
}

func TestHandler_PostChat_BreaksAfterMaxToolCalls(t *testing.T) {
	// Arrange — model always calls read_page; loop should cap at 15 and emit error.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("programming/go.md", "Go notes")

	cs := store.NewMemChatStore()
	// Always returns a read_page tool call → triggers the cap.
	streamer := &multiResponseStreamer{bodies: []string{readPageToolUseSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"keep reading"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — an error SSE event must have been emitted.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (SSE headers already sent), got %d", w.Code)
	}
	events := parseSSE(t, w.Body.String())
	hasError := false
	for _, ev := range events {
		if ev.event == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Errorf("expected error SSE event after max tool calls, got: %v", events)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

type sseEvent struct {
	event string
	data  string
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			current.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if current.event != "" {
				events = append(events, current)
				current = sseEvent{}
			}
		}
	}
	return events
}
