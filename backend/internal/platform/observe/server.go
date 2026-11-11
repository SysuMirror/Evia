package observe

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// NewServer builds the dedicated administrative listener for processes that do
// not serve the management API (the worker commands). It exposes only
// /metrics and /healthz; it deliberately carries no business routes or
// authentication and must be bound to a restricted network via configuration.
func NewServer(addr string, registry *Registry) *http.Server {
	mux := http.NewServeMux()
	// Method-neutral registration so MetricsHandler can answer non-GET with 405.
	mux.HandleFunc("/metrics", MetricsHandler(registry))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
}

// RunServer starts the administrative server and blocks until ctx is cancelled,
// then performs a bounded graceful shutdown. It is shaped for the process
// supervisor: a bind failure returns immediately while expected cancellation
// resolves to ctx.Err() instead of http.ErrServerClosed.
func RunServer(ctx context.Context, server *http.Server) error {
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	select {
	case err := <-result:
		if ctx.Err() != nil && errors.Is(err, http.ErrServerClosed) {
			return ctx.Err()
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		select {
		case err := <-result:
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return ctx.Err()
		case <-shutdown.Done():
			server.Close()
			return shutdown.Err()
		}
	}
}
