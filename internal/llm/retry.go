package llm

import (
	"crypto/tls"
	"errors"
	"net"
	"syscall"
)

// isRetryable reports whether err is a transient network failure that warrants
// retrying the request from scratch (before any response bytes are consumed).
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if errors.Is(netErr, syscall.ECONNRESET) {
			return true
		}
	}
	var tlsErr tls.AlertError
	if errors.As(err, &tlsErr) {
		return true
	}
	return false
}
