package chat_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// stubStreamer is a fake llm.Streamer that returns a fixed SSE body.
type stubStreamer struct {
	body                string
	err                 error
	capturedMsgs        []store.Message  // last call's messages
	capturedAttachments []llm.Attachment // last call's PDF attachments
	capturedSchema      string           // last call's schema content
}

func (s *stubStreamer) Stream(_ context.Context, msgs []store.Message, _ string, schema string, attachments []llm.Attachment) (io.ReadCloser, error) {
	s.capturedMsgs = msgs
	s.capturedAttachments = attachments
	s.capturedSchema = schema
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

func TestHandler_PostChat_PersistsNonEmptyPlaceholderOnLLMFailure(t *testing.T) {
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

	// Assert — store contains two messages: the user message and a non-empty
	// assistant placeholder. Without the placeholder the next request would
	// send two consecutive user messages (Anthropic rejects that with 400).
	// Without non-empty content, Anthropic also rejects empty assistant turns.
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	msgs, err := cs.ListMessages(session.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user + assistant placeholder), got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected first message role 'user', got %q", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %q", msgs[1].Role)
	}
	if msgs[1].Content == "" {
		t.Error("expected non-empty assistant placeholder so Anthropic accepts it in subsequent requests")
	}
	// The HTTP response body must equal the stored placeholder so the frontend
	// can display it directly in the assistant bubble without a separate error banner.
	if body := strings.TrimSpace(w.Body.String()); body != msgs[1].Content {
		t.Errorf("response body %q does not match stored placeholder %q", body, msgs[1].Content)
	}
}

func TestHandler_PostChat_RateLimitReturns429AndStoresNoRetryPlaceholder(t *testing.T) {
	// Arrange — streamer fails with a 429 rate-limit error.
	cs := store.NewMemChatStore()
	vm := vault.NewManager(t.TempDir())
	rateLimitErr := errors.New("anthropic API returned 429: rate_limit_error")
	h := chat.NewHandler(cs, &stubStreamer{err: rateLimitErr}, vm)

	form := url.Values{"message": {"fetch this url please"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — handler returns 429, not 500.
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	// Assert — stored placeholder does not tell LLM to "try again".
	// If it did, the LLM would re-attempt the same fetch on the next message
	// and hit 429 again, causing a permanent stuck loop.
	session, err := cs.ResolveSession()
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	msgs, err := cs.ListMessages(session.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user + assistant placeholder), got %d", len(msgs))
	}
	placeholder := msgs[1].Content
	if strings.Contains(strings.ToLower(placeholder), "try again") {
		t.Errorf("placeholder must not encourage retry, got: %q", placeholder)
	}
	if placeholder == "" {
		t.Error("placeholder must be non-empty so Anthropic accepts it in subsequent requests")
	}
	if body := strings.TrimSpace(w.Body.String()); body != placeholder {
		t.Errorf("response body %q does not match stored placeholder %q", body, placeholder)
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

func (s *multiResponseStreamer) Stream(_ context.Context, _ []store.Message, _ string, _ string, _ []llm.Attachment) (io.ReadCloser, error) {
	body := s.bodies[len(s.bodies)-1] // default: last body
	if s.idx < len(s.bodies) {
		body = s.bodies[s.idx]
		s.idx++
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func TestHandler_PostChat_EmitsStatusEventOnRetrievalTool(t *testing.T) {
	for _, tc := range []struct {
		name        string
		vaultFile   string
		toolSSE     string
		wantInEvent string
	}{
		{"read_page includes path", "programming/go.md", readPageToolUseSSE, "programming/go.md"},
		{"search_vault includes query", "programming/go.md", searchVaultToolUseSSE, "Go interfaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			vaultDir := t.TempDir()
			vm := vault.NewManager(vaultDir)
			_ = vm.WriteFile(tc.vaultFile, "Go interfaces are structural.")
			cs := store.NewMemChatStore()
			streamer := &multiResponseStreamer{bodies: []string{tc.toolSSE, minimalAnthropicSSE}}
			h := chat.NewHandler(cs, streamer, vm)

			form := url.Values{"message": {"tell me about Go"}}
			req := httptest.NewRequest(http.MethodPost, "/api/chat",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			// Act
			h.ServeHTTP(w, req)

			// Assert
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			hasStatus := false
			for _, ev := range parseSSE(t, w.Body.String()) {
				if ev.event == "status" && strings.Contains(ev.data, tc.wantInEvent) {
					hasStatus = true
					break
				}
			}
			if !hasStatus {
				t.Errorf("expected status event containing %q, got none", tc.wantInEvent)
			}
		})
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

func TestHandler_PostChat_ForwardsSchemaContentToStreamer(t *testing.T) {
	// Arrange — pre-populate schema.md so EnsureSchema returns known content.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("schema.md", "# My Schema\n\nmy rules\n")

	cs := store.NewMemChatStore()
	streamer := &stubStreamer{body: minimalAnthropicSSE}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"hello"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — streamer received the schema content.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(streamer.capturedSchema, "my rules") {
		t.Errorf("expected schema content forwarded to streamer, got %q", streamer.capturedSchema)
	}
}

// ── Group 3: new tool dispatch ────────────────────────────────────────────────

const movePageToolUseSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_mv1","name":"move_page","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"from\":\"old.md\",\"to\":\"new/new.md\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

const deleteItemToolUseSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_di1","name":"delete_item","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"trash.md\",\"recursive\":false}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

func TestHandler_PostChat_DispatchesMovePageAndEmitsVaultEvent(t *testing.T) {
	// Arrange
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("old.md", "content to move")

	cs := store.NewMemChatStore()
	// First call: move_page. Second call: final text answer.
	streamer := &multiResponseStreamer{bodies: []string{movePageToolUseSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"reorganise my vault"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — file was moved
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "new/new.md")); os.IsNotExist(err) {
		t.Error("expected new/new.md to exist after move_page")
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "old.md")); !os.IsNotExist(err) {
		t.Error("expected old.md to be gone after move_page")
	}

	// Assert — vault SSE event emitted
	events := parseSSE(t, w.Body.String())
	hasVault := false
	for _, ev := range events {
		if ev.event == "vault" {
			hasVault = true
			break
		}
	}
	if !hasVault {
		t.Errorf("expected vault SSE event for move_page, got events: %v", events)
	}
}

func TestHandler_PostChat_DispatchesDeleteItemAndEmitsVaultEvent(t *testing.T) {
	// Arrange
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("trash.md", "delete me")

	cs := store.NewMemChatStore()
	// First call: delete_item. Second call: final text answer.
	streamer := &multiResponseStreamer{bodies: []string{deleteItemToolUseSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"clean up my vault"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — file was deleted
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "trash.md")); !os.IsNotExist(err) {
		t.Error("expected trash.md to be deleted after delete_item")
	}

	// Assert — vault SSE event emitted
	events := parseSSE(t, w.Body.String())
	hasVault := false
	for _, ev := range events {
		if ev.event == "vault" {
			hasVault = true
			break
		}
	}
	if !hasVault {
		t.Errorf("expected vault SSE event for delete_item, got events: %v", events)
	}
}

// saveAttachmentNotesSSE is a streaming response where the model calls
// save_attachment_notes to write notes about a PDF to its sidecar.
const saveAttachmentNotesSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_san1","name":"save_attachment_notes","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"_attachments/report.pdf\",\"notes\":\"Q1 results: revenue up 12%, costs stable.\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

func TestHandler_PostChat_DispatchesSaveAttachmentNotesAndUpdatesSidecar(t *testing.T) {
	// Arrange — create an attachment sidecar with no description.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	attachPath := "_attachments/report.pdf"
	_ = vm.WriteFile(attachPath, "%PDF")
	_ = vm.WriteAttachmentMeta(attachPath, vault.AttachmentMeta{
		ID:           "att_report",
		OriginalName: "report.pdf",
		MediaType:    "application/pdf",
	})

	cs := store.NewMemChatStore()
	// First call: save_attachment_notes. Second call: final text answer.
	streamer := &multiResponseStreamer{bodies: []string{saveAttachmentNotesSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"summarise this report"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — sidecar description was updated.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	meta, err := vm.ReadAttachmentMeta(attachPath)
	if err != nil {
		t.Fatalf("ReadAttachmentMeta: %v", err)
	}
	if meta.Description != "Q1 results: revenue up 12%, costs stable." {
		t.Errorf("want description %q, got %q", "Q1 results: revenue up 12%, costs stable.", meta.Description)
	}

	// Assert — response ends with done (not an error).
	events := parseSSE(t, w.Body.String())
	if len(events) == 0 || events[len(events)-1].event != "done" {
		t.Errorf("expected last event to be done, got: %v", events)
	}
}

// twoDeleteItemsSSE is a streaming response where the model calls delete_item
// twice in a single response — one for a file and one for its sidecar.
const twoDeleteItemsSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_di1","name":"delete_item","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"file1.md\",\"recursive\":false}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_di2","name":"delete_item","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"file2.md\",\"recursive\":false}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}

`

// searchChatHistoryToolUseSSE is a streaming response where the model calls search_chat_history.
const searchChatHistoryToolUseSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_sch1","name":"search_chat_history","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"Go interfaces\",\"offset\":0}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

func TestHandler_PostChat_DispatchesSearchChatHistory_EmitsStatusAndToolResult(t *testing.T) {
	// Arrange — seed two sessions with content matching the query.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)

	cs := store.NewMemChatStore()
	sess, _ := cs.ResolveSession()
	_ = cs.AppendMessage(store.Message{SessionID: sess.ID, Role: "user", Content: "Go interfaces are structural types"})
	// Age it out so the next ResolveSession creates a new one.
	stale := sess
	stale.LastActiveAt = time.Now().Add(-31 * time.Minute)
	_ = cs.UpdateSession(stale)

	// First call: search_chat_history. Second call: final text answer.
	streamer := &multiResponseStreamer{bodies: []string{searchChatHistoryToolUseSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"didn't we talk about Go interfaces?"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — request succeeded.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	events := parseSSE(t, w.Body.String())

	// Assert — status SSE event emitted before the search.
	hasStatus := false
	for _, ev := range events {
		if ev.event == "status" && strings.Contains(strings.ToLower(ev.data), "searching") {
			hasStatus = true
			break
		}
	}
	if !hasStatus {
		t.Errorf("expected status event containing 'Searching', got events: %v", events)
	}

	// Assert — ends with done.
	if len(events) == 0 || events[len(events)-1].event != "done" {
		t.Errorf("expected last event to be done, got: %v", events)
	}

	// Assert — tool result persisted in the current (new) session.
	sessions, err := cs.ListSessions(1, 0)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("ListSessions: %v, %v", sessions, err)
	}
	msgs, err := cs.ListMessages(sessions[0].ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	hasToolResult := false
	for _, m := range msgs {
		if m.Role == "tool_result" {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		t.Errorf("expected a tool_result message in session %q, got: %v", sessions[0].ID, msgs)
	}
}

func TestHandler_PostChat_DispatchesMultipleToolCallsInOneResponse(t *testing.T) {
	// Arrange — vault has two files; model deletes both in one response.
	vaultDir := t.TempDir()
	vm := vault.NewManager(vaultDir)
	_ = vm.WriteFile("file1.md", "delete me")
	_ = vm.WriteFile("file2.md", "delete me too")

	cs := store.NewMemChatStore()
	// First call: two delete_items in one SSE stream. Second call: final text.
	streamer := &multiResponseStreamer{bodies: []string{twoDeleteItemsSSE, minimalAnthropicSSE}}
	h := chat.NewHandler(cs, streamer, vm)

	form := url.Values{"message": {"clean up"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — both files were deleted.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "file1.md")); !os.IsNotExist(err) {
		t.Error("expected file1.md to be deleted")
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "file2.md")); !os.IsNotExist(err) {
		t.Error("expected file2.md to be deleted")
	}

	// Assert — two vault SSE events emitted (one per deleted file).
	events := parseSSE(t, w.Body.String())
	vaultCount := 0
	for _, ev := range events {
		if ev.event == "vault" {
			vaultCount++
		}
	}
	if vaultCount != 2 {
		t.Errorf("expected 2 vault SSE events, got %d; events: %v", vaultCount, events)
	}

	// Assert — response ends with done.
	if len(events) == 0 || events[len(events)-1].event != "done" {
		t.Errorf("expected last event to be done, got: %v", events)
	}
}

// ── Group: server_tool_use (web_fetch / web_search built-in tools) ───────────

// webFetchServerToolUseSSE simulates a stream where Anthropic executes web_fetch
// server-side: server_tool_use block → result block → text response, all in one stream.
const webFetchServerToolUseSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_wf1","name":"web_fetch","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"url\":\"https://example.com/page\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"web_fetch_tool_result","tool_use_id":"srvtoolu_wf1"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Here is the page content."}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_stop
data: {"type":"message_stop"}

`

func TestHandler_PostChat_ServerToolUseBlockDoesNotDispatchAsCustomTool(t *testing.T) {
	// Arrange — stream has server_tool_use + text (all in one stream, no second call needed).
	// If server_tool_use were mistakenly added to toolCalls, the handler would
	// attempt dispatch, store "unknown tool: web_fetch", and re-loop — ending
	// with an error event instead of done.
	h := newTestHandler(t, &stubStreamer{body: webFetchServerToolUseSSE})
	form := url.Values{"message": {"fetch this page"}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert — response ends with done (not error), and no "unknown tool" result stored.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	events := parseSSE(t, w.Body.String())
	if len(events) == 0 || events[len(events)-1].event != "done" {
		t.Errorf("expected last event to be done, got events: %v", events)
	}
	for _, ev := range events {
		if ev.event == "error" {
			t.Errorf("unexpected error event: %v", ev)
		}
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
