package dream

import (
	"context"
	"log/slog"
	"net/http"
)

// NewHandler returns an http.Handler for POST /api/dream/run.
// It fires the consolidation in a background goroutine and returns 202 immediately.
func NewHandler(consolidate ConsolidateFn) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/dream/run", func(w http.ResponseWriter, r *http.Request) {
		go func() {
			if err := consolidate(context.Background()); err != nil {
				slog.Error("dream: manual run failed", "error", err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}
