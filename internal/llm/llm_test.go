package llm_test

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/store"
)

// anthropicSSEResponse returns a minimal Anthropic streaming SSE response
// containing one content_block_delta event followed by a message_stop event.
func anthropicSSEResponse(delta string) string {
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + delta + `"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
}

func TestClient_Stream_YieldsTokenDeltas(t *testing.T) {
	// Arrange — stub server that mimics the Anthropic streaming API
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate the request basics
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("x-api-key") == "" {
			t.Error("expected x-api-key header")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("expected anthropic-version header")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("hello"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{
		APIKey:  "test-key",
		BaseURL: srv.URL,
	})
	messages := []store.Message{
		{Role: "user", Content: "say hello"},
	}

	// Act
	body, err := client.Stream(t.Context(), messages)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer body.Close()

	// Assert — collect all text_delta events from the SSE stream
	var got []string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			got = append(got, strings.TrimPrefix(line, "data: "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading stream: %v", err)
	}

	// The raw SSE body should have been forwarded as-is.
	if len(got) == 0 {
		t.Fatal("expected at least one data line in the SSE stream")
	}
	// At least one line should contain our delta text.
	found := false
	for _, line := range got {
		if strings.Contains(line, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find 'hello' in SSE data, got: %v", got)
	}
}

func TestClient_Stream_ReturnsErrorOnNon200(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"authentication_error"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{
		APIKey:  "bad-key",
		BaseURL: srv.URL,
	})

	// Act
	_, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "hi"}})

	// Assert
	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
}
