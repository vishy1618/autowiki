package dream_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/dream"
)

func TestDreamHandler_NonPost_Returns405(t *testing.T) {
	// Arrange
	h := dream.NewHandler(func(_ context.Context) error { return nil })
	req := httptest.NewRequest(http.MethodGet, "/api/dream/run", nil)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestDreamHandler_PostRun_Returns202(t *testing.T) {
	// Arrange
	h := dream.NewHandler(func(_ context.Context) error { return nil })
	req := httptest.NewRequest(http.MethodPost, "/api/dream/run", nil)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}
}

func TestDreamHandler_PostRun_CallsConsolidateFn(t *testing.T) {
	// Arrange
	called := make(chan struct{}, 1)
	h := dream.NewHandler(func(_ context.Context) error {
		called <- struct{}{}
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/dream/run", nil)
	w := httptest.NewRecorder()

	// Act
	h.ServeHTTP(w, req)

	// Assert
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Error("expected consolidate to be called, timed out")
	}
}
