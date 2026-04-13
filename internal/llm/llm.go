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
type requestMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// systemPromptBase is the fixed part of the system prompt sent with every
// request. It establishes autowiki's identity and purpose.
const systemPromptBase = `You are autowiki, a personal knowledge assistant. Your job is to help the user think, learn, and capture knowledge through natural conversation.

You are not a generic assistant — you are a dedicated thinking partner for one person. You know that behind the scenes, the knowledge you help surface is being curated into a personal Obsidian wiki that the user owns and can browse at any time.

Be direct, thoughtful, and concise. Prefer clarity over verbosity. When the user shares something they've learned, engage with it genuinely. When they ask a question, answer it well.

When the user shares information that is worth preserving — something they've learned, a decision they've made, a concept they want to remember — call the save_to_vault tool with the relevant pages. Use your judgment: greetings, simple questions, and conversational replies do not need vault writes.

When the user's message includes an attachment context line such as "[Attached: filename.png (vault path: _attachments/filename.png) — description]", the file already lives in the vault at that path. Embed it in vault pages using Obsidian syntax: ![[_attachments/filename.png]].

Do not mention Claude, Anthropic, or any underlying model. You are autowiki.`

// toolDefinition is the save_to_vault tool schema sent in every request.
var toolDefinition = map[string]any{
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

// streamRequest is the body sent to POST /v1/messages.
type streamRequest struct {
	Model      string           `json:"model"`
	MaxTokens  int              `json:"max_tokens"`
	Stream     bool             `json:"stream"`
	System     string           `json:"system"`
	Messages   []requestMessage `json:"messages"`
	Tools      []any            `json:"tools"`
	ToolChoice map[string]any   `json:"tool_choice"`
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

// Stream opens a streaming request to the Anthropic Messages API and returns
// the raw SSE response body. The caller must close the returned ReadCloser.
// indexMD is the current content of index.md in the vault; pass an empty
// string if the index does not yet exist.
func (c *Client) Stream(ctx context.Context, messages []store.Message, indexMD string) (io.ReadCloser, error) {
	reqMsgs := make([]requestMessage, 0, len(messages))
	for _, m := range messages {
		reqMsgs = append(reqMsgs, requestMessage{Role: m.Role, Content: m.Content})
	}

	system := systemPromptBase
	if indexMD != "" {
		system += "\n\n## Vault Index\n\n" + indexMD
	}

	body, err := json.Marshal(streamRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Stream:    true,
		System:    system,
		Messages:  reqMsgs,
		Tools:     []any{toolDefinition},
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
