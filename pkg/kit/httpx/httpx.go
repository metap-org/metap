// Package httpx holds HTTP plumbing shared by every Go service: an outbound client that always
// has a timeout, and a server loop with graceful shutdown.
package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/metap-org/metap/pkg/kit/shutdown"
)

// DefaultClientTimeout bounds every outbound call made through DefaultClient.
const DefaultClientTimeout = 30 * time.Second

// DefaultShutdownTimeout is how long Serve waits for in-flight requests after a signal.
const DefaultShutdownTimeout = 10 * time.Second

// NewClient returns an *http.Client with the given overall request timeout. Never use
// http.DefaultClient: it has no timeout, so one hung peer pins a goroutine forever.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// DefaultClient returns NewClient(DefaultClientTimeout).
func DefaultClient() *http.Client {
	return NewClient(DefaultClientTimeout)
}

// Serve runs an HTTP server on addr until SIGINT/SIGTERM, then shuts it down gracefully. It
// returns the listener's error if the server fails to start or dies, nil after a clean shutdown.
func Serve(addr string, handler http.Handler) error {
	ctx, stop := shutdown.Context(context.Background())
	defer stop()
	return ServeContext(ctx, &http.Server{Addr: addr, Handler: handler}, DefaultShutdownTimeout)
}

// ServeContext runs server until ctx is cancelled, then shuts it down, waiting at most
// shutdownTimeout for in-flight requests.
func ServeContext(ctx context.Context, server *http.Server, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return <-errCh
}
