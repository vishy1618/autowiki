package chat_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/store"
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

func newTestHandler(streamer chat.Streamer) http.Handler {
	cs := store.NewMemChatStore()
	return chat.NewHandler(cs, streamer)
}

func TestHandler_PostChat_StreamsDeltaAndDoneEvents(t *testing.T) {
	// Arrange
	h := newTestHandler(&stubStreamer{body: minimalAnthropicSSE})
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
	h := newTestHandler(&stubStreamer{body: minimalAnthropicSSE})
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
	h := chat.NewHandler(cs, &stubStreamer{body: minimalAnthropicSSE})
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
