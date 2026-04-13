package llm_test

import (
	"bufio"
	"encoding/json"
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

		// Validate that a system prompt is included in the request body.
		var body struct {
			System string `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if body.System == "" {
			t.Error("expected a non-empty system prompt in the request")
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
	body, err := client.Stream(t.Context(), messages, "", nil)
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

func TestClient_DescribeImage_SendsBase64ImageAndReturnsDescription(t *testing.T) {
	// Arrange — stub server validates vision request shape and returns a description
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []struct {
					Type   string `json:"type"`
					Source *struct {
						Type      string `json:"type"`
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source,omitempty"`
					Text string `json:"text,omitempty"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if len(body.Messages) == 0 {
			t.Error("expected at least one message")
		}
		content := body.Messages[0].Content
		hasImage := false
		for _, block := range content {
			if block.Type == "image" && block.Source != nil && block.Source.Type == "base64" {
				hasImage = true
			}
		}
		if !hasImage {
			t.Error("expected image content block with base64 source")
		}

		// Return a non-streaming JSON response (DescribeImage uses non-streaming).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"content":[{"type":"text","text":"a red circle on white background"}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})

	desc, err := client.DescribeImage(t.Context(), []byte("fakeimgdata"), "image/png")

	if err != nil {
		t.Fatalf("DescribeImage: %v", err)
	}
	if desc != "a red circle on white background" {
		t.Errorf("want description %q, got %q", "a red circle on white background", desc)
	}
}

func TestClient_DescribeImage_ReturnsErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"authentication_error"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "bad-key", BaseURL: srv.URL})

	_, err := client.DescribeImage(t.Context(), []byte("x"), "image/png")

	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
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
	_, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "x"}}, "", nil)

	// Assert
	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
}

func TestClient_Stream_IncludesSaveToVaultTool(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			ToolChoice struct {
				Type string `json:"type"`
			} `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}

		found := false
		for _, tool := range body.Tools {
			if tool.Name == "save_to_vault" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected save_to_vault tool in request")
		}
		if body.ToolChoice.Type != "auto" {
			t.Errorf("expected tool_choice auto, got %q", body.ToolChoice.Type)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})

	body, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "hi"}}, "", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_IncludesIndexMDInSystemPrompt(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System string `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if !strings.Contains(body.System, "## Vault Index") {
			t.Errorf("expected system prompt to contain vault index section, got: %q", body.System)
		}
		if !strings.Contains(body.System, "existing content") {
			t.Errorf("expected system prompt to contain indexMD content, got: %q", body.System)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})

	body, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "hi"}}, "existing content", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_SystemPromptInstructsLLMToMaintainIndex(t *testing.T) {
	// Arrange — capture the system prompt the client sends.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System string `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		// The system prompt must tell the LLM to keep index.md current so
		// that the vault has a live map of content after every ingest.
		if !strings.Contains(body.System, "index.md") {
			t.Errorf("expected system prompt to instruct LLM to maintain index.md, got: %q", body.System)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})

	body, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "hi"}}, "", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_SendsAssistantContentBlocksWhenContentIsJSONArray(t *testing.T) {
	// When an assistant message has a JSON-array Content (text + tool_use
	// blocks), the client must send it as a content array — not a plain string.
	var capturedMessages []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		capturedMessages = body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	messages := []store.Message{
		{Role: "user", Content: "I learned Go has interfaces"},
		{Role: "assistant", Content: `[{"type":"text","text":"Saving!"},{"type":"tool_use","id":"toolu_abc","name":"save_to_vault","input":{}}]`},
		{Role: "tool_result", Content: `{"tool_use_id":"toolu_abc","content":"saved: notes/go.md","is_error":false}`},
		{Role: "user", Content: "What did I tell you about Go?"},
	}

	body, err := client.Stream(t.Context(), messages, "", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()

	// Expect 3 API messages: user, assistant, merged-user (tool_result + text).
	if len(capturedMessages) != 3 {
		t.Fatalf("expected 3 messages sent to API, got %d", len(capturedMessages))
	}

	// Assistant message content must be an array, not a string.
	var assistantMsg struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(capturedMessages[1], &assistantMsg); err != nil {
		t.Fatalf("unmarshalling assistant message: %v", err)
	}
	if assistantMsg.Role != "assistant" {
		t.Errorf("expected assistant role, got %q", assistantMsg.Role)
	}
	if len(assistantMsg.Content) != 2 {
		t.Errorf("expected 2 content blocks in assistant message, got %d", len(assistantMsg.Content))
	}

	// Last message must be user with tool_result block merged with text block.
	var mergedMsg struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text,omitempty"`
			ToolUseID string `json:"tool_use_id,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(capturedMessages[2], &mergedMsg); err != nil {
		t.Fatalf("unmarshalling merged user message: %v", err)
	}
	if mergedMsg.Role != "user" {
		t.Errorf("expected user role for merged message, got %q", mergedMsg.Role)
	}
	if len(mergedMsg.Content) != 2 {
		t.Fatalf("expected 2 content blocks in merged message, got %d", len(mergedMsg.Content))
	}
	if mergedMsg.Content[0].Type != "tool_result" {
		t.Errorf("expected first block to be tool_result, got %q", mergedMsg.Content[0].Type)
	}
	if mergedMsg.Content[0].ToolUseID != "toolu_abc" {
		t.Errorf("expected tool_use_id %q, got %q", "toolu_abc", mergedMsg.Content[0].ToolUseID)
	}
	if mergedMsg.Content[1].Type != "text" {
		t.Errorf("expected second block to be text, got %q", mergedMsg.Content[1].Type)
	}
	if mergedMsg.Content[1].Text != "What did I tell you about Go?" {
		t.Errorf("expected user text in merged message, got %q", mergedMsg.Content[1].Text)
	}
}

func TestClient_Stream_SendsPdfAttachmentAsDocumentContentBlock(t *testing.T) {
	// Arrange — capture the messages the client sends and verify the last
	// user message contains a document content block for the PDF.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type   string `json:"type"`
					Source *struct {
						Type      string `json:"type"`
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source,omitempty"`
					Text string `json:"text,omitempty"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}

		// Find the last user message.
		var lastUserContent []struct {
			Type   string `json:"type"`
			Source *struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source,omitempty"`
			Text string `json:"text,omitempty"`
		}
		for _, msg := range body.Messages {
			if msg.Role == "user" {
				lastUserContent = msg.Content
			}
		}

		hasDocumentBlock := false
		for _, block := range lastUserContent {
			if block.Type == "document" && block.Source != nil &&
				block.Source.Type == "base64" && block.Source.MediaType == "application/pdf" {
				hasDocumentBlock = true
			}
		}
		if !hasDocumentBlock {
			t.Error("expected last user message to contain a document content block for the PDF attachment")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	messages := []store.Message{{Role: "user", Content: "summarise this PDF"}}
	attachments := []llm.Attachment{
		{MediaType: "application/pdf", Data: []byte("%PDF-1.4 fake")},
	}

	body, err := client.Stream(t.Context(), messages, "", attachments)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_IncludesReadPageTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		found := false
		for _, tool := range body.Tools {
			if tool.Name == "read_page" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected read_page tool in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "hi"}}, "", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_IncludesSearchVaultTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		found := false
		for _, tool := range body.Tools {
			if tool.Name == "search_vault" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected search_vault tool in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "hi"}}, "", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_DoesNotInjectPdfIntoToolResultMessage(t *testing.T) {
	// When the last message in history is a tool_result (appears as a user
	// message with structured content blocks), PDF attachments must NOT be
	// prepended. Doing so would corrupt the tool_result block and cause Anthropic
	// to return 400: tool_use without a corresponding tool_result.
	var capturedMessages []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		capturedMessages = body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})

	// History ending in a tool_result (as produced by the agentic loop after
	// a read_page call).
	messages := []store.Message{
		{Role: "user", Content: "summarise this PDF"},
		{Role: "assistant", Content: `[{"type":"tool_use","id":"toolu_rp1","name":"read_page","input":{"path":"notes.md"}}]`},
		{Role: "tool_result", Content: `{"tool_use_id":"toolu_rp1","content":"# Notes","is_error":false}`},
	}
	attachments := []llm.Attachment{
		{MediaType: "application/pdf", Data: []byte("%PDF-1.4 fake")},
	}

	body, err := client.Stream(t.Context(), messages, "", attachments)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()

	// The last API message must be a user message whose FIRST block is the
	// tool_result, not a document block.
	if len(capturedMessages) == 0 {
		t.Fatal("expected at least one captured message")
	}
	last := capturedMessages[len(capturedMessages)-1]
	var lastMsg struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(last, &lastMsg); err != nil {
		t.Fatalf("unmarshalling last message: %v", err)
	}
	if lastMsg.Role != "user" {
		t.Fatalf("expected last message role 'user', got %q", lastMsg.Role)
	}
	if len(lastMsg.Content) == 0 {
		t.Fatal("expected at least one content block in last message")
	}
	if lastMsg.Content[0].Type != "tool_result" {
		t.Errorf("expected first content block to be tool_result, got %q — PDF was injected into tool_result message", lastMsg.Content[0].Type)
	}
}

func TestClient_Stream_SystemPromptMentionsAttachmentEmbedSyntax(t *testing.T) {
	// Arrange — capture the system prompt the client sends.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System string `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		// The system prompt must tell the LLM how to embed attachments in
		// vault pages so it doesn't refuse to reference uploaded files.
		if !strings.Contains(body.System, "![[") {
			t.Errorf("expected system prompt to mention Obsidian embed syntax ![[...]], got: %q", body.System)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})

	body, err := client.Stream(t.Context(), []store.Message{{Role: "user", Content: "hi"}}, "", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}
