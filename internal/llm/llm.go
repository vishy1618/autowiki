package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/suvish/autowiki/internal/store"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultModel     = "claude-sonnet-4-6"
	anthropicVersion = "2023-06-01"
	messagesPath     = "/v1/messages"
)

// Config holds configuration for the LLM client.
type Config struct {
	APIKey  string
	Model   string // defaults to claude-sonnet-4-6
	BaseURL string // defaults to https://api.anthropic.com; override in tests
}

// Client calls the Anthropic Messages API.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// NewClient returns a new Client with the given configuration.
func NewClient(cfg Config) *Client {
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &Client{cfg: cfg, httpClient: http.DefaultClient}
}

// requestMessage is the per-message shape expected by the Anthropic API.
// Content is either a plain string or a []any of content blocks.
type requestMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// toolDefinitions contains all tool schemas sent in every request.
var toolDefinitions = []any{
	readPageToolDefinition,
	searchVaultToolDefinition,
	listVaultToolDefinition,
	readPagePartialToolDefinition,
	movePageToolDefinition,
	deleteItemToolDefinition,
	saveAttachmentNotesToolDefinition,
	saveToVaultToolDefinition,
	searchChatHistoryToolDefinition,
	webSearchToolDefinition,
	webFetchToolDefinition,
}

// webSearchToolDefinition is the Anthropic-native web search tool.
var webSearchToolDefinition = map[string]any{
	"type":     "web_search_20250305",
	"name":     "web_search",
	"max_uses": 5,
}

// webFetchToolDefinition is the Anthropic-native web fetch tool.
// cache_control is placed here (last in toolDefinitions) so Anthropic caches
// the entire tool list on every request.
var webFetchToolDefinition = map[string]any{
	"type":          "web_fetch_20250910",
	"name":          "web_fetch",
	"max_uses":      5,
	"cache_control": map[string]any{"type": "ephemeral"},
}

// searchChatHistoryToolDefinition lets the LLM search across past chat sessions.
var searchChatHistoryToolDefinition = map[string]any{
	"name":        "search_chat_history",
	"description": "Search through past conversation sessions for a keyword or topic. Scans 3 sessions per call, newest first. Call again with offset+3 to look further back. Max offset is 50. Returns session date, role, and a snippet of each matching message.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":  map[string]any{"type": "string", "description": "The keyword or phrase to search for."},
			"offset": map[string]any{"type": "integer", "description": "Number of sessions to skip (default 0). Increment by 3 each call to paginate.", "default": 0},
		},
		"required": []string{"query"},
	},
}

// readPageToolDefinition allows the LLM to fetch the full content of a vault page.
var readPageToolDefinition = map[string]any{
	"name":        "read_page",
	"description": "Fetch the full content of a specific vault page by its vault-relative path (e.g. 'programming/go.md'). Use the vault index in the system prompt to decide which page to read.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Vault-relative path to the page, e.g. 'programming/go.md'"},
		},
		"required": []string{"path"},
	},
}

// searchVaultToolDefinition allows the LLM to keyword-search across all vault pages.
var searchVaultToolDefinition = map[string]any{
	"name":        "search_vault",
	"description": "Keyword search across all vault markdown files. Returns up to 10 results, each with the page path and a snippet of the matching text.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Search query string"},
		},
		"required": []string{"query"},
	},
}

// listVaultToolDefinition allows the LLM to list vault files and directories.
var listVaultToolDefinition = map[string]any{
	"name":        "list_vault",
	"description": "List files and directories inside the vault. Use this to discover orphan pages or explore the vault structure. Returns each entry's path, type ('file' or 'dir'), and size in bytes.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string", "description": "Vault-relative path to list. Omit or pass empty string for the vault root."},
			"recursive": map[string]any{"type": "boolean", "description": "If true, list all nested entries. Default false."},
		},
	},
}

// readPagePartialToolDefinition allows the LLM to read the first N characters of a file.
var readPagePartialToolDefinition = map[string]any{
	"name":        "read_page_partial",
	"description": "Read the first max_chars bytes of a vault page. Useful for previewing large pages or checking log.md without loading the full content.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string", "description": "Vault-relative path to the file."},
			"max_chars": map[string]any{"type": "integer", "description": "Maximum number of bytes to read."},
		},
		"required": []string{"path", "max_chars"},
	},
}

// movePageToolDefinition allows the LLM to rename or relocate a vault file.
var movePageToolDefinition = map[string]any{
	"name":        "move_page",
	"description": "Move or rename a page within the vault. Parent directories are created as needed. Use this to reorganise the vault structure.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from": map[string]any{"type": "string", "description": "Vault-relative source path."},
			"to":   map[string]any{"type": "string", "description": "Vault-relative destination path."},
		},
		"required": []string{"from", "to"},
	},
}

// deleteItemToolDefinition allows the LLM to delete a file or directory from the vault.
var deleteItemToolDefinition = map[string]any{
	"name":        "delete_item",
	"description": "Delete a file or directory from the vault. SAFETY RULE: never delete a file unless its content has already been confirmed saved to another location. Non-empty directories require recursive=true.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":      map[string]any{"type": "string", "description": "Vault-relative path to delete."},
			"recursive": map[string]any{"type": "boolean", "description": "Required true to delete a non-empty directory. Default false."},
		},
		"required": []string{"path"},
	},
}

// saveAttachmentNotesToolDefinition lets the LLM write its understanding of an
// attachment back to the sidecar, so future searches can find the content
// without re-sending the file.
var saveAttachmentNotesToolDefinition = map[string]any{
	"name":        "save_attachment_notes",
	"description": "Write a summary or extracted text to the sidecar of an attachment already saved in the vault. Call this whenever you receive a PDF attachment — capture key content, topics, and any important details so future searches can surface this file without re-sending it.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string", "description": "Vault-relative path to the attachment (e.g. '_attachments/report-20260416-abc123.pdf')."},
			"notes": map[string]any{"type": "string", "description": "Summary or extracted text to store in the sidecar. Be thorough: include key facts, topics, dates, names, and any other details worth searching for later."},
		},
		"required": []string{"path", "notes"},
	},
}

// saveToVaultToolDefinition is the save_to_vault tool schema sent in every request.
var saveToVaultToolDefinition = map[string]any{
	"name":        "save_to_vault",
	"description": "Save knowledge to the user's personal vault. Call this when the conversation contains information worth preserving. Each page should be a focused topic; use nested paths (e.g. 'programming/go.md') to organise by subject.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pages": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Vault-relative path, e.g. 'programming/go.md'"},
						"content": map[string]any{"type": "string", "description": "Full markdown content for the page"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		"required": []string{"pages"},
	},
}

// ConsolidationMessage matches dream.ConsolidationMessage; redefined here to
// avoid an import cycle (dream → llm is fine; llm → dream must not happen).
type ConsolidationMessage struct {
	Role    string
	Content string
}

// StreamWithSystem sends a non-chat consolidation request to the Anthropic API
// with a caller-provided system prompt and returns the raw SSE body.
// The caller must close the returned ReadCloser.
func (c *Client) StreamWithSystem(ctx context.Context, systemPrompt string, messages []ConsolidationMessage) (io.ReadCloser, error) {
	reqMsgs := make([]requestMessage, len(messages))
	for i, m := range messages {
		reqMsgs[i] = requestMessage{Role: m.Role, Content: m.Content}
	}

	body, err := json.Marshal(streamRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Stream:    true,
		System:    cachedSystemBlocks(systemPrompt),
		Messages:  reqMsgs,
		Tools:     toolDefinitions,
		ToolChoice: map[string]any{
			"type": "auto",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+messagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return resp.Body, nil
}

// Attachment is a file to be sent inline in the current chat turn.
// Only PDFs are supported; the Data field holds the raw file bytes.
type Attachment struct {
	MediaType string
	Data      []byte
}

// streamRequest is the body sent to POST /v1/messages.
type streamRequest struct {
	Model      string           `json:"model"`
	MaxTokens  int              `json:"max_tokens"`
	Stream     bool             `json:"stream"`
	System     any              `json:"system"`
	Messages   []requestMessage `json:"messages"`
	Tools      []any            `json:"tools"`
	ToolChoice map[string]any   `json:"tool_choice"`
}

// cachedSystemBlocks wraps a system prompt string in a content-block array
// with cache_control so Anthropic caches it across requests.
func cachedSystemBlocks(text string) []map[string]any {
	return []map[string]any{
		{
			"type":          "text",
			"text":          text,
			"cache_control": map[string]any{"type": "ephemeral"},
		},
	}
}

// toolResultContent holds the parsed fields from a tool_result store message.
type toolResultContent struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error"`
}

// describeImagePrompt is sent with the image so the LLM produces a concise
// description suitable for knowledge indexing.
const describeImagePrompt = "Describe this image concisely in 1–3 sentences. Focus on the key information it conveys, suitable for indexing in a personal knowledge base."

// describeRequest is the non-streaming request body for image description.
type describeRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string `json:"role"`
		Content []any  `json:"content"`
	} `json:"messages"`
}

// describeResponse is the non-streaming response body.
type describeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// DescribeImage sends the image to the Anthropic vision API and returns a
// concise description. mediaType should be e.g. "image/png" or "image/jpeg".
func (c *Client) DescribeImage(ctx context.Context, data []byte, mediaType string) (string, error) {
	encoded := base64.StdEncoding.EncodeToString(data)

	reqBody := describeRequest{
		Model:     c.cfg.Model,
		MaxTokens: 512,
		Messages: []struct {
			Role    string `json:"role"`
			Content []any  `json:"content"`
		}{
			{
				Role: "user",
				Content: []any{
					map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": mediaType,
							"data":       encoded,
						},
					},
					map[string]any{
						"type": "text",
						"text": describeImagePrompt,
					},
				},
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+messagesPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic API returned %d", resp.StatusCode)
	}

	var result describeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	for _, block := range result.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", nil
}

// buildRequestMessages converts store messages into the Anthropic API format.
//
// Three special cases:
//  1. "assistant" messages whose Content is a JSON array are sent with a
//     content-block array (text + tool_use blocks) rather than a plain string.
//  2. "tool_result" messages are merged with the following "user" message so
//     we never send two consecutive user-role messages to the API.
//  3. A trailing "tool_result" with no following user message is sent as a
//     standalone user message with just the tool_result block.
func buildRequestMessages(messages []store.Message) []requestMessage {
	out := make([]requestMessage, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		m := messages[i]

		switch m.Role {
		case "tool_result":
			// Collect ALL consecutive tool_result messages into one user message.
			// Multiple consecutive tool_results arise when the LLM emits more than
			// one tool call in a single turn; Anthropic requires them to be sent as
			// a single user message containing multiple tool_result content blocks.
			var trBlocks []any
			for ; i < len(messages) && messages[i].Role == "tool_result"; i++ {
				var tr toolResultContent
				if err := json.Unmarshal([]byte(messages[i].Content), &tr); err != nil {
					// Malformed tool_result: skip rather than corrupt the history.
					continue
				}
				trBlock := map[string]any{
					"type":        "tool_result",
					"tool_use_id": tr.ToolUseID,
					"content":     tr.Content,
				}
				if tr.IsError {
					trBlock["is_error"] = true
				}
				trBlocks = append(trBlocks, trBlock)
			}
			i-- // outer loop will i++ past the last tool_result we consumed

			if len(trBlocks) == 0 {
				continue
			}

			// Merge with the next user message if present.
			if i+1 < len(messages) && messages[i+1].Role == "user" {
				i++
				next := messages[i]
				content := append(trBlocks, map[string]any{"type": "text", "text": next.Content})
				out = append(out, requestMessage{Role: "user", Content: content})
			} else {
				out = append(out, requestMessage{Role: "user", Content: trBlocks})
			}

		case "assistant":
			// If Content is a JSON array it contains content blocks.
			if len(m.Content) > 0 && m.Content[0] == '[' {
				var blocks []json.RawMessage
				if err := json.Unmarshal([]byte(m.Content), &blocks); err == nil {
					rawBlocks := make([]any, len(blocks))
					for j, b := range blocks {
						rawBlocks[j] = json.RawMessage(b)
					}
					out = append(out, requestMessage{Role: "assistant", Content: rawBlocks})
					continue
				}
			}
			out = append(out, requestMessage{Role: m.Role, Content: m.Content})

		default:
			out = append(out, requestMessage{Role: m.Role, Content: m.Content})
		}
	}
	return out
}

// addCacheControl injects cache_control into the last content block of a
// message. Anthropic requires it at the block level, not the message level.
// Plain-string content is converted to a single text block first.
func addCacheControl(msg *requestMessage) {
	cc := map[string]any{"type": "ephemeral"}
	switch c := msg.Content.(type) {
	case string:
		msg.Content = []any{
			map[string]any{"type": "text", "text": c, "cache_control": cc},
		}
	case []any:
		if len(c) > 0 {
			if last, ok := c[len(c)-1].(map[string]any); ok {
				last["cache_control"] = cc
			}
		}
	}
}

// Stream opens a streaming request to the Anthropic Messages API and returns
// the raw SSE response body. The caller must close the returned ReadCloser.
// systemPrompt is sent as a cached content block; the caller is responsible
// for assembling it from whatever base prompt and injected context is needed.
func (c *Client) Stream(ctx context.Context, systemPrompt string, messages []store.Message, attachments []Attachment) (io.ReadCloser, error) {
	reqMsgs := buildRequestMessages(messages)

	// Inject PDF attachment content blocks into the most recent plain-text
	// user message so the LLM receives the full document inline.
	//
	// We search backwards because on agentic loop iterations 2+, the last
	// message is a user message containing a tool_result block (structured
	// []any content). We must not touch those — mixing document blocks with
	// tool_result blocks violates Anthropic's requirement that a tool_result
	// immediately follows its corresponding tool_use.
	//
	// Injecting into the original user message (the one that carries the
	// "[Attached PDF: …]" context line) ensures the LLM retains access to
	// the PDF on every iteration of the loop.
	if len(attachments) > 0 {
		for i := len(reqMsgs) - 1; i >= 0; i-- {
			msg := &reqMsgs[i]
			if msg.Role != "user" {
				continue
			}
			text, ok := msg.Content.(string)
			if !ok {
				continue // structured content (e.g. tool_result) — skip
			}
			var blocks []any
			for _, att := range attachments {
				blocks = append(blocks, map[string]any{
					"type": "document",
					"source": map[string]any{
						"type":       "base64",
						"media_type": att.MediaType,
						"data":       base64.StdEncoding.EncodeToString(att.Data),
					},
				})
			}
			if text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			msg.Content = blocks
			break
		}
	}

	// Cache the stable history prefix — everything before the current user turn.
	// This benefits both multi-turn conversations (5-min TTL) and the agentic
	// loop within a single request (same history re-sent on each iteration).
	if len(reqMsgs) >= 2 {
		addCacheControl(&reqMsgs[len(reqMsgs)-2])
	}

	body, err := json.Marshal(streamRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Stream:    true,
		System:    cachedSystemBlocks(systemPrompt),
		Messages:  reqMsgs,
		Tools:     toolDefinitions,
		ToolChoice: map[string]any{
			"type": "auto",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+messagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic API returned %d: %s", resp.StatusCode, string(body))
	}
	return resp.Body, nil
}
