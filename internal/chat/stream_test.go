package chat

import (
	"strings"
	"testing"
)

// ── extractDelta ──────────────────────────────────────────────────────────────

func TestExtractDelta_TextDelta_ReturnsText(t *testing.T) {
	raw := `{"delta":{"type":"text_delta","text":"hello"}}`
	text, inputJSON, err := extractDelta(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello" {
		t.Errorf("want text %q, got %q", "hello", text)
	}
	if inputJSON != "" {
		t.Errorf("want empty inputJSON, got %q", inputJSON)
	}
}

func TestExtractDelta_InputJSONDelta_ReturnsInputJSON(t *testing.T) {
	raw := `{"delta":{"type":"input_json_delta","partial_json":"{\"key\":\"val\"}"}}`
	text, inputJSON, err := extractDelta(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("want empty text, got %q", text)
	}
	if inputJSON != `{"key":"val"}` {
		t.Errorf("want inputJSON %q, got %q", `{"key":"val"}`, inputJSON)
	}
}

func TestExtractDelta_UnknownType_ReturnsBothEmpty(t *testing.T) {
	raw := `{"delta":{"type":"unknown_delta"}}`
	text, inputJSON, err := extractDelta(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" || inputJSON != "" {
		t.Errorf("want both empty, got text=%q inputJSON=%q", text, inputJSON)
	}
}

func TestExtractDelta_InvalidJSON_ReturnsError(t *testing.T) {
	_, _, err := extractDelta("not json")
	if err == nil {
		t.Error("want error for invalid JSON, got nil")
	}
}

// ── buildAssistantContent ─────────────────────────────────────────────────────

func TestBuildAssistantContent_TextOnly_ReturnsJSONArrayWithTextBlock(t *testing.T) {
	result := buildAssistantContent("hello world", nil)
	if !strings.Contains(result, `"text"`) {
		t.Errorf("want text block in result, got %q", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("want text content in result, got %q", result)
	}
}

func TestBuildAssistantContent_ToolOnly_OmitsEmptyTextBlock(t *testing.T) {
	calls := []toolCall{{id: "tc1", name: "save_to_vault", json: "{}"}}
	result := buildAssistantContent("", calls)
	if strings.Contains(result, `"text":""`) {
		t.Errorf("result must not contain empty text block, got %q", result)
	}
	if !strings.Contains(result, `"tool_use"`) {
		t.Errorf("result must contain tool_use block, got %q", result)
	}
}

func TestBuildAssistantContent_TextAndTool_IncludesBothBlocks(t *testing.T) {
	calls := []toolCall{{id: "tc1", name: "save_to_vault", json: "{}"}}
	result := buildAssistantContent("preamble", calls)
	if !strings.Contains(result, `"text"`) {
		t.Errorf("expected text block in result, got %q", result)
	}
	if !strings.Contains(result, `"tool_use"`) {
		t.Errorf("expected tool_use block in result, got %q", result)
	}
}

// ── serverToolStatusMessage ───────────────────────────────────────────────────

func TestServerToolStatusMessage_WebFetch_IncludesURL(t *testing.T) {
	msg := serverToolStatusMessage("web_fetch", `{"url":"https://example.com"}`)
	if !strings.Contains(msg, "https://example.com") {
		t.Errorf("want URL in message, got %q", msg)
	}
}

func TestServerToolStatusMessage_WebFetch_MalformedJSON_ReturnsGeneric(t *testing.T) {
	msg := serverToolStatusMessage("web_fetch", "not json")
	if msg == "" {
		t.Error("want non-empty fallback message, got empty")
	}
}

func TestServerToolStatusMessage_WebSearch_ReturnsSearchingMessage(t *testing.T) {
	msg := serverToolStatusMessage("web_search", "{}")
	if !strings.Contains(strings.ToLower(msg), "search") {
		t.Errorf("want 'search' in message, got %q", msg)
	}
}

func TestServerToolStatusMessage_UnknownTool_ReturnsGenericMessage(t *testing.T) {
	msg := serverToolStatusMessage("unknown_tool", "{}")
	if msg == "" {
		t.Error("want non-empty fallback message, got empty")
	}
}

// ── writeSSE ──────────────────────────────────────────────────────────────────

func TestWriteSSE_FormatsEventAndDataOnSeparateLines(t *testing.T) {
	var buf strings.Builder
	writeSSE(&buf, "delta", `{"text":"hi"}`)
	got := buf.String()
	want := "event: delta\ndata: {\"text\":\"hi\"}\n\n"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}
