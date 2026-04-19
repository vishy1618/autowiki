package chat_test

import (
	"context"
	"io"
	"testing"

	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

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
