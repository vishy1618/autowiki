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

// systemBlock is the decoded shape of one element in a cached system array.
type systemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// systemText concatenates the text from all blocks in a cached system array,
// matching the helper used in tests that inspect system prompt content.
func systemText(blocks []systemBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

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

func TestClient_Stream_ForwardsSystemPromptExactly(t *testing.T) {
	want := "custom system prompt for testing"
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []systemBlock `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = systemText(body.System)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), want, []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()

	if got != want {
		t.Errorf("expected system prompt %q, got %q", want, got)
	}
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
			System []systemBlock `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if systemText(body.System) == "" {
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
	body, err := client.Stream(t.Context(), "test system prompt", messages, nil)
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
	_, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "x"}}, nil)

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

	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
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

	body, err := client.Stream(t.Context(), "test system prompt", messages, nil)
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

func TestClient_Stream_SetsCacheControlOnSecondToLastMessage(t *testing.T) {
	// When there are 2+ messages, the second-to-last API message (the last from
	// the previous turn) must have cache_control so the stable history prefix is
	// cached across turns and agentic loop iterations.
	var capturedMessages []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedMessages = body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	messages := []store.Message{
		{Role: "assistant", Content: "Hello! How can I help?"},
		{Role: "user", Content: "Tell me about Go"},
	}

	body, err := client.Stream(t.Context(), "test system prompt", messages, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()

	if len(capturedMessages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(capturedMessages))
	}

	// cache_control must be on the last content block of the second-to-last
	// message, not on the message itself (Anthropic rejects top-level cache_control).
	idx := len(capturedMessages) - 2
	var secondToLast struct {
		Content []struct {
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"content"`
	}
	if err := json.Unmarshal(capturedMessages[idx], &secondToLast); err != nil {
		t.Fatalf("unmarshalling second-to-last message: %v", err)
	}
	if len(secondToLast.Content) == 0 {
		t.Fatalf("expected content blocks in second-to-last message, got: %s", capturedMessages[idx])
	}
	lastBlock := secondToLast.Content[len(secondToLast.Content)-1]
	if lastBlock.CacheControl == nil || lastBlock.CacheControl.Type != "ephemeral" {
		t.Errorf("expected last content block to have cache_control ephemeral, got: %s", capturedMessages[idx])
	}
}

func TestClient_Stream_NoCacheControlWithSingleMessage(t *testing.T) {
	// With only one message (first turn), there is no prior history to cache.
	var capturedMessages []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedMessages = body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	messages := []store.Message{
		{Role: "user", Content: "Hello"},
	}

	body, err := client.Stream(t.Context(), "test system prompt", messages, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()

	for i, raw := range capturedMessages {
		var msg struct {
			CacheControl *struct{} `json:"cache_control"`
		}
		_ = json.Unmarshal(raw, &msg)
		if msg.CacheControl != nil {
			t.Errorf("message %d should not have cache_control on first turn, got: %s", i, raw)
		}
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

	body, err := client.Stream(t.Context(), "test system prompt", messages, attachments)
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
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_IncludesSearchChatHistoryTool(t *testing.T) {
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
			if tool.Name == "search_chat_history" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected search_chat_history tool in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
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
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
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

	body, err := client.Stream(t.Context(), "test system prompt", messages, attachments)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()

	if len(capturedMessages) == 0 {
		t.Fatal("expected at least one captured message")
	}

	// Assert 1: the FIRST user message must contain the PDF document block.
	// The LLM must still receive the PDF content even on iteration 2+.
	var firstMsg struct {
		Role    string `json:"role"`
		Content []struct {
			Type   string `json:"type"`
			Source *struct {
				MediaType string `json:"media_type"`
			} `json:"source,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(capturedMessages[0], &firstMsg); err != nil {
		t.Fatalf("unmarshalling first message: %v", err)
	}
	hasDocInFirst := false
	for _, block := range firstMsg.Content {
		if block.Type == "document" {
			hasDocInFirst = true
		}
	}
	if !hasDocInFirst {
		t.Errorf("expected PDF document block in first user message so LLM retains PDF access on loop iterations 2+")
	}

	// Assert 2: the LAST message must be a user message whose FIRST block is
	// the tool_result, not a document block.
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

func TestClient_Stream_IncludesSaveAttachmentNotesTool(t *testing.T) {
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
			if tool.Name == "save_attachment_notes" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected save_attachment_notes tool in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_IncludesWebFetchTool(t *testing.T) {
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
			if tool.Name == "web_fetch" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected web_fetch tool in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_SendsSystemPromptAsArrayWithCacheControl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Type         string `json:"type"`
				Text         string `json:"text"`
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control"`
			} `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if len(body.System) == 0 {
			t.Error("expected system to be a non-empty array")
		} else {
			last := body.System[len(body.System)-1]
			if last.CacheControl == nil || last.CacheControl.Type != "ephemeral" {
				t.Errorf("expected last system block to have cache_control ephemeral, got %+v", last.CacheControl)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_LastToolHasCacheControl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name         string `json:"name"`
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		if len(body.Tools) == 0 {
			t.Error("expected at least one tool")
		} else {
			last := body.Tools[len(body.Tools)-1]
			if last.CacheControl == nil || last.CacheControl.Type != "ephemeral" {
				t.Errorf("expected last tool to have cache_control ephemeral, got %+v", last.CacheControl)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_IncludesWebSearchTool(t *testing.T) {
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
			if tool.Name == "web_search" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected web_search tool in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}



func TestClient_Stream_WebSearchToolUses2026Version(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		for _, tool := range body.Tools {
			if tool.Name == "web_search" && tool.Type != "web_search_20260209" {
				t.Errorf("web_search tool must use version web_search_20260209, got %q", tool.Type)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_WebFetchToolUses2026Version(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		for _, tool := range body.Tools {
			if tool.Name == "web_fetch" && tool.Type != "web_fetch_20260209" {
				t.Errorf("web_fetch tool must use version web_fetch_20260209, got %q", tool.Type)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

func TestClient_Stream_IncludesCodeExecutionTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		found := false
		for _, tool := range body.Tools {
			if tool.Type == "code_execution_20260120" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected code_execution_20260120 tool in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	body, err := client.Stream(t.Context(), "test system prompt", []store.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}

// ── DescribeImage edge cases ───────────────────────────────────────────────────

func TestClient_DescribeImage_ReturnsEmptyStringWhenNoTextBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Response contains only a non-text content block.
		io.WriteString(w, `{"content":[{"type":"tool_use","id":"x"}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	desc, err := client.DescribeImage(t.Context(), []byte("img"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc != "" {
		t.Errorf("expected empty description, got %q", desc)
	}
}

// ── buildRequestMessages edge cases ──────────────────────────────────────────

func TestClient_Stream_SkipsMalformedToolResultMessage(t *testing.T) {
	// A tool_result message whose Content is not valid JSON should be skipped
	// rather than crashing or sending a corrupt message to the API.
	var capturedMessages []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedMessages = body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	messages := []store.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: `[{"type":"tool_use","id":"t1","name":"read_page","input":{}}]`},
		{Role: "tool_result", Content: `not valid json`},
		{Role: "user", Content: "follow up"},
	}

	body, err := client.Stream(t.Context(), "test system prompt", messages, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()

	// Malformed tool_result is skipped; the follow-up user message remains
	// a standalone user message (not merged with a tool_result block).
	if len(capturedMessages) == 0 {
		t.Fatal("expected captured messages")
	}
	var lastMsg struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	_ = json.Unmarshal(capturedMessages[len(capturedMessages)-1], &lastMsg)
	if lastMsg.Role != "user" {
		t.Errorf("expected last message role 'user', got %q", lastMsg.Role)
	}
}

// ── addCacheControl edge cases ────────────────────────────────────────────────

func TestClient_Stream_NoCacheControlMutationOnEmptyContentArray(t *testing.T) {
	// When the second-to-last message has an empty content array (edge case),
	// addCacheControl should not panic and Stream should succeed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, anthropicSSEResponse("ok"))
	}))
	defer srv.Close()

	client := llm.NewClient(llm.Config{APIKey: "test-key", BaseURL: srv.URL})
	// Assistant message with an empty JSON array produces []any{} after parsing,
	// triggering the len(c) == 0 guard in addCacheControl.
	messages := []store.Message{
		{Role: "assistant", Content: `[]`},
		{Role: "user", Content: "hello"},
	}

	body, err := client.Stream(t.Context(), "test system prompt", messages, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body.Close()
}
