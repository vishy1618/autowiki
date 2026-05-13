package chat_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// searchVaultSSE is a streaming response where the model calls search_vault.
// Unlike save_to_vault, this tool does not terminate the agentic loop, so the
// runner will call Stream again with the backfilled history.
const searchVaultSSE = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_sv1","name":"search_vault","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"files\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`

// sequenceStreamer returns successive SSE bodies from a slice and records the
// messages passed on every Stream call.
type sequenceStreamer struct {
	responses []string
	calls     [][]store.Message
}

func (s *sequenceStreamer) Stream(_ context.Context, _ string, msgs []store.Message, _ []llm.Attachment) (io.ReadCloser, error) {
	cp := make([]store.Message, len(msgs))
	copy(cp, msgs)
	s.calls = append(s.calls, cp)
	body := s.responses[len(s.calls)-1]
	return io.NopCloser(strings.NewReader(body)), nil
}

// TestAgenticRunner_Run_CrossSessionBackfill_MessagesStartWithUser is the
// regression test for the Anthropic API 400 caused by GetRecentContext trimming
// into the middle of a prior-session tool-call exchange.
//
// Setup: prior session has 28 messages where the tool exchange sits at indices
// 0-3. With the current session holding 3 messages after the first tool
// dispatch, GetRecentContext needs 27 from prior and trims to prior[1:] —
// which, before the fix, started with an assistant(tool_use) block and caused
// "unexpected tool_use_id found in tool_result blocks" from the API.
func TestAgenticRunner_Run_CrossSessionBackfill_MessagesStartWithUser(t *testing.T) {
	// Arrange — prior session: tool exchange at the start, then regular turns.
	cs := store.NewMemChatStore()
	prior, _ := cs.ResolveSession()
	_ = cs.AppendMessage(store.Message{SessionID: prior.ID, Role: "user", Content: "find my attachments"})
	_ = cs.AppendMessage(store.Message{SessionID: prior.ID, Role: "assistant", Content: `[{"type":"text","text":"Searching..."},{"type":"tool_use","id":"toolu_p1","name":"search_vault","input":{"query":"attachments"}}]`})
	_ = cs.AppendMessage(store.Message{SessionID: prior.ID, Role: "tool_result", Content: `{"tool_use_id":"toolu_p1","content":"found: attachments/"}`})
	_ = cs.AppendMessage(store.Message{SessionID: prior.ID, Role: "assistant", Content: "Found your attachments."})
	// Pad to 28 total so trimming (need=27, cap=27, prior[1:]) lands at index 1.
	for i := range 12 {
		_ = cs.AppendMessage(store.Message{SessionID: prior.ID, Role: "user", Content: fmt.Sprintf("follow-up %d", i)})
		_ = cs.AppendMessage(store.Message{SessionID: prior.ID, Role: "assistant", Content: fmt.Sprintf("reply %d", i)})
	}
	stalePrior := prior
	stalePrior.LastActiveAt = time.Now().Add(-31 * time.Minute)
	_ = cs.UpdateSession(stalePrior)

	// Current session — 1 user message; after tool dispatch it will have 3,
	// making need = 30-3 = 27 and triggering the prior-session trim.
	current, _ := cs.ResolveSession()
	_ = cs.AppendMessage(store.Message{SessionID: current.ID, Role: "user", Content: "show me the files"})

	streamer := &sequenceStreamer{responses: []string{searchVaultSSE, minimalAnthropicSSE}}
	vm := vault.NewManager(t.TempDir())

	// Open the first body the same way the handler does before calling Run.
	firstBody, _ := streamer.Stream(context.Background(), "system", nil, nil)
	runner := chat.NewAgenticRunner(streamer, cs, vm)

	// Act
	err := runner.Run(context.Background(), current.ID, "system", firstBody, io.Discard, 15)

	// Assert — Run must succeed and the messages passed to the second Stream
	// call (after tool dispatch and cross-session backfill) must begin with a
	// plain user message, not an orphaned assistant(tool_use) or tool_result.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(streamer.calls) < 2 {
		t.Fatalf("expected at least 2 Stream calls, got %d", len(streamer.calls))
	}
	secondCallMsgs := streamer.calls[1]
	if len(secondCallMsgs) == 0 {
		t.Fatal("second Stream call received no messages")
	}
	if secondCallMsgs[0].Role != "user" {
		t.Errorf("first message in second Stream call must be role %q, got %q (content: %q)",
			"user", secondCallMsgs[0].Role, secondCallMsgs[0].Content)
	}
}

func TestAgenticRunner_Run_TextOnlyResponse_StoresAssistantMessage(t *testing.T) {
	// Arrange
	cs := store.NewMemChatStore()
	session, _ := cs.ResolveSession()
	_ = cs.AppendMessage(store.Message{SessionID: session.ID, Role: "user", Content: "hello"})
	streamer := &stubStreamer{body: minimalAnthropicSSE}
	vm := vault.NewManager(t.TempDir())
	firstBody, _ := streamer.Stream(context.Background(), "system prompt", nil, nil)

	runner := chat.NewAgenticRunner(streamer, cs, vm)

	// Act
	err := runner.Run(context.Background(), session.ID, "system prompt", firstBody, io.Discard, 15)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	msgs, _ := cs.ListMessages(session.ID)
	if len(msgs) != 2 {
		t.Fatalf("expected user + assistant, got %d messages", len(msgs))
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected assistant role, got %q", msgs[1].Role)
	}
}
