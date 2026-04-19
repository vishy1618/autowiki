package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// writeSSE writes a single SSE event to w.
func writeSSE(w io.Writer, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

// extractDelta parses an Anthropic content_block_delta payload.
// Returns (textDelta, inputJSONDelta, error).
func extractDelta(raw string) (string, string, error) {
	var payload struct {
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", "", err
	}
	switch payload.Delta.Type {
	case "text_delta":
		return payload.Delta.Text, "", nil
	case "input_json_delta":
		return "", payload.Delta.PartialJSON, nil
	}
	return "", "", nil
}

// serverToolStatusMessage returns the status message text for a server_tool_use
// block. For web_fetch it includes the URL; for web_search it is generic.
func serverToolStatusMessage(toolName, toolJSON string) string {
	switch toolName {
	case "web_fetch":
		var input struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(toolJSON), &input); err == nil && input.URL != "" {
			return "Fetching " + input.URL + "\u2026"
		}
		return "Fetching page\u2026"
	case "web_search":
		return "Searching the web\u2026"
	default:
		return "Working\u2026"
	}
}

// buildAssistantContent serialises a text + tool_use content-block array.
// Only includes a text block when text is non-empty (Anthropic rejects empty
// text blocks on subsequent turns). Handles multiple tool calls.
func buildAssistantContent(text string, calls []toolCall) string {
	var blocks []any
	if text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, tc := range calls {
		var toolInput json.RawMessage
		if err := json.Unmarshal([]byte(tc.json), &toolInput); err != nil {
			toolInput = json.RawMessage("{}")
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    tc.id,
			"name":  tc.name,
			"input": toolInput,
		})
	}
	b, err := json.Marshal(blocks)
	if err != nil {
		return text
	}
	return string(b)
}

// scanStream reads an Anthropic SSE body, forwarding text deltas to w and
// accumulating any tool call inputs. It returns the assembled text and all
// tool calls emitted in this stream (there may be more than one).
//
// Built-in Anthropic tools (web_fetch, web_search) emit server_tool_use blocks
// instead of tool_use. Anthropic executes these server-side and streams the
// result back in the same response — no Go dispatch is needed. scanStream
// emits a status SSE event when a server_tool_use block completes and does NOT
// add them to toolCalls.
func scanStream(body io.Reader, w io.Writer, canFlush bool, flusher http.Flusher) streamResult {
	var assembled strings.Builder
	var toolJSONBuf strings.Builder
	var currentID, currentName string
	var calls []toolCall
	inToolUseBlock := false

	var serverToolJSONBuf strings.Builder
	var serverToolName string
	inServerToolUseBlock := false

	writeSSE(w, "status", `{"message":"Working\u2026"}`)
	if canFlush {
		flusher.Flush()
	}

	scanner := bufio.NewScanner(body)
	var lastEvent string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			lastEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")

		switch lastEvent {
		case "content_block_start":
			var payload struct {
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(raw), &payload); err == nil {
				switch payload.ContentBlock.Type {
				case "tool_use":
					inToolUseBlock = true
					currentID = payload.ContentBlock.ID
					currentName = payload.ContentBlock.Name
					toolJSONBuf.Reset()
					if currentName == "save_to_vault" {
						writeSSE(w, "status", `{"message":"Saving to vault\u2026"}`)
						if canFlush {
							flusher.Flush()
						}
					}
				case "server_tool_use":
					inServerToolUseBlock = true
					serverToolName = payload.ContentBlock.Name
					serverToolJSONBuf.Reset()
				default:
					inToolUseBlock = false
					inServerToolUseBlock = false
				}
			}

		case "content_block_delta":
			text, inputJSON, err := extractDelta(raw)
			if err == nil {
				if text != "" {
					assembled.WriteString(text)
					writeSSE(w, "delta", fmt.Sprintf(`{"text":%q}`, text))
					if canFlush {
						flusher.Flush()
					}
				}
				if inputJSON != "" {
					if inToolUseBlock {
						toolJSONBuf.WriteString(inputJSON)
					} else if inServerToolUseBlock {
						serverToolJSONBuf.WriteString(inputJSON)
					}
				}
			}

		case "content_block_stop":
			if inToolUseBlock {
				calls = append(calls, toolCall{
					id:   currentID,
					name: currentName,
					json: toolJSONBuf.String(),
				})
			} else if inServerToolUseBlock {
				statusMsg := serverToolStatusMessage(serverToolName, serverToolJSONBuf.String())
				writeSSE(w, "status", fmt.Sprintf(`{"message":%q}`, statusMsg))
				if canFlush {
					flusher.Flush()
				}
			}
			inToolUseBlock = false
			inServerToolUseBlock = false
		}
	}

	return streamResult{
		assembled: assembled.String(),
		toolCalls: calls,
		scanErr:   scanner.Err(),
	}
}
