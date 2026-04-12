package chat_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// stubStreamer is a fake llm.Streamer that returns a fixed SSE body.
type stubStreamer struct {
	body string
	err  error
}

func (s *stubStreamer) Stream(_ context.Context, _ []store.Message, _ string) (io.ReadCloser, error) {
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
