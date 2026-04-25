package llm

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/suvish/autowiki/internal/store"
)

func TestIsRetryable_ConnectionReset(t *testing.T) {
	err := &net.OpError{
		Op:  "read",
		Err: &net.OpError{Err: syscall.ECONNRESET},
	}
	if !isRetryable(err) {
		t.Fatalf("expected connection reset to be retryable, got false")
	}
}

func TestIsRetryable_ContextCanceled(t *testing.T) {
	if isRetryable(context.Canceled) {
		t.Fatal("context.Canceled must not be retried")
	}
}

func TestIsRetryable_ContextDeadlineExceeded(t *testing.T) {
	if isRetryable(context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded must not be retried")
	}
}

func TestIsRetryable_EOF(t *testing.T) {
	err := &net.OpError{Op: "read", Err: syscall.ECONNRESET}
	if !isRetryable(err) {
		t.Fatal("expected ECONNRESET wrapped in OpError to be retryable")
	}
}

func TestIsRetryable_NilError(t *testing.T) {
	if isRetryable(nil) {
		t.Fatal("nil error must not be retryable")
	}
}

func TestIsRetryable_TLSBadRecordMAC(t *testing.T) {
	// tls.AlertError(20) is bad_record_mac — the exact error seen in production.
	err := fmt.Errorf("remote: %w", tls.AlertError(20))
	if !isRetryable(err) {
		t.Fatal("tls.AlertError (bad record MAC) must be retryable")
	}
}

// minimalSSE is a bare-minimum Anthropic SSE response the client can parse.
func minimalSSE() string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
}

func TestStream_RetriesOnConnectionReset(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// First call: hijack and reset the connection.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("ResponseWriter does not support hijacking")
				return
			}
			conn, _, _ := hj.Hijack()
			conn.Close() // abrupt close mimics connection reset
			return
		}
		// Second call: serve a valid SSE response.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, minimalSSE())
	}))
	defer srv.Close()

	client := NewClient(Config{APIKey: "test", BaseURL: srv.URL})
	body, err := client.Stream(
		context.Background(),
		"system",
		[]store.Message{{Role: "user", Content: "hi"}},
		nil,
	)
	if err != nil {
		t.Fatalf("expected Stream to succeed after retry, got: %v", err)
	}
	body.Close()

	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 requests (1 fail + 1 retry), got %d", got)
	}
}

func TestStream_ReturnsErrorAfterMaxRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	client := NewClient(Config{APIKey: "test", BaseURL: srv.URL})
	_, err := client.Stream(
		context.Background(),
		"system",
		[]store.Message{{Role: "user", Content: "hi"}},
		nil,
	)
	if err == nil {
		t.Fatal("expected error after all retries exhausted, got nil")
	}
}
