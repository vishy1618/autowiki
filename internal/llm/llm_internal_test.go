package llm

import (
	"net/http"
	"testing"
)

func TestWebFetchToolDefinition_CapsContentSize(t *testing.T) {
	maxContentTokens, ok := webFetchToolDefinition["max_content_tokens"]
	if !ok {
		t.Fatal("want webFetchToolDefinition to set max_content_tokens, got none")
	}
	if maxContentTokens.(int) <= 0 {
		t.Errorf("want positive max_content_tokens, got %v", maxContentTokens)
	}
}

func TestNewClient_DoesNotUseDefaultTransport(t *testing.T) {
	c := NewClient(Config{APIKey: "test"})
	if c.httpClient == http.DefaultClient {
		t.Fatal("NewClient must not use http.DefaultClient — shared transport prevents keepalive tuning")
	}
	if c.httpClient.Transport == http.DefaultTransport {
		t.Fatal("NewClient must clone the transport, not reuse http.DefaultTransport")
	}
}
