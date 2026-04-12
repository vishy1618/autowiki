package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/suvish/autowiki/internal/store"
)

const (
	defaultBaseURL        = "https://api.anthropic.com"
	defaultModel          = "claude-sonnet-4-6"
	anthropicVersion      = "2023-06-01"
	messagesPath          = "/v1/messages"
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

// streamRequest is the body sent to POST /v1/messages.
type streamRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	Stream    bool             `json:"stream"`
	Messages  []requestMessage `json:"messages"`
}

// Stream opens a streaming request to the Anthropic Messages API and returns
// the raw SSE response body. The caller must close the returned ReadCloser.
func (c *Client) Stream(ctx context.Context, messages []store.Message) (io.ReadCloser, error) {
	reqMsgs := make([]requestMessage, 0, len(messages))
	for _, m := range messages {
		reqMsgs = append(reqMsgs, requestMessage{Role: m.Role, Content: m.Content})
	}

	body, err := json.Marshal(streamRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Stream:    true,
		Messages:  reqMsgs,
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
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic API returned %d", resp.StatusCode)
	}
	return resp.Body, nil
}
