package chat

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

// AgenticRunner owns the agentic loop: it drives the LLM call/tool-dispatch
// cycle and writes SSE events to an io.Writer.
type AgenticRunner struct {
	streamer Streamer
	store    store.ChatStore
	vault    *vault.Manager
}

// NewAgenticRunner returns an AgenticRunner backed by the given dependencies.
func NewAgenticRunner(streamer Streamer, cs store.ChatStore, vm *vault.Manager) *AgenticRunner {
	return &AgenticRunner{streamer: streamer, store: cs, vault: vm}
}

// Run drives the agentic loop starting from firstBody (the already-opened first
// LLM stream). It emits SSE events to w, persists messages to the store, and
// returns when the LLM produces a final answer or an error occurs. maxToolCalls
// caps the total number of tool dispatches. If w implements http.Flusher, Run
// flushes after each event.
func (r *AgenticRunner) Run(ctx context.Context, sessionID, systemPrompt string, firstBody io.ReadCloser, w io.Writer, maxToolCalls int) error {
	flusher, canFlush := w.(http.Flusher)
	flush := func() {
		if canFlush {
			flusher.Flush()
		}
	}

	body := firstBody
	for toolCallCount := 0; ; {
		sr := scanStream(body, w, canFlush, flusher)
		body.Close()

		if sr.scanErr != nil {
			writeSSE(w, "error", fmt.Sprintf(`{"message":%q}`, sr.scanErr.Error()))
			flush()
			return sr.scanErr
		}

		assistantContent := sr.assembled
		if len(sr.toolCalls) > 0 {
			assistantContent = buildAssistantContent(sr.assembled, sr.toolCalls)
		}
		_ = r.store.AppendMessage(store.Message{
			SessionID: sessionID,
			Role:      "assistant",
			Content:   assistantContent,
		})

		if len(sr.toolCalls) == 0 {
			writeSSE(w, "done", fmt.Sprintf(`{"session_id":%q}`, sessionID))
			flush()
			return nil
		}

		r.dispatchToolCalls(w, sessionID, sr.toolCalls, canFlush, flusher)

		toolCallCount += len(sr.toolCalls)
		if toolCallCount > maxToolCalls {
			writeSSE(w, "error", `{"message":"exceeded maximum tool call limit"}`)
			flush()
			return fmt.Errorf("exceeded maximum tool call limit")
		}

		history, err := r.store.GetRecentContext(sessionID, 30)
		if err != nil {
			writeSSE(w, "error", `{"message":"store error"}`)
			flush()
			return err
		}
		body, err = r.streamer.Stream(ctx, systemPrompt, history, nil)
		if err != nil {
			slog.Error("LLM stream failed in agentic loop", "error", err, "session_id", sessionID)
			writeSSE(w, "error", fmt.Sprintf(`{"message":%q}`, err.Error()))
			flush()
			return err
		}
	}
}
