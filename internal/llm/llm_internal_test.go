package llm

import (
	"net/http"
	"testing"
)

func TestNewClient_DoesNotUseDefaultTransport(t *testing.T) {
	c := NewClient(Config{APIKey: "test"})
	if c.httpClient == http.DefaultClient {
		t.Fatal("NewClient must not use http.DefaultClient — shared transport prevents keepalive tuning")
	}
	if c.httpClient.Transport == http.DefaultTransport {
		t.Fatal("NewClient must clone the transport, not reuse http.DefaultTransport")
	}
}
