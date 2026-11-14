// Package httpx holds HTTP plumbing shared by every Go service, standard library only (the edge
// and analytics planes import it too): an outbound client that always has a timeout, a server loop
// with graceful shutdown and safe timeouts, JSON request/response helpers with the one error
// shape, a bounded JSON decoder, and the middleware every listener should have (Recover, MaxBody).
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

// Server timeouts applied by NewServer and ServeContext to any field left at zero. There is no
// default WriteTimeout: a proxied or exported response can legitimately take long, and a service
// that wants one sets it itself.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 60 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultMaxHeaderBytes    = 1 << 20
)

// NewServer returns an *http.Server for addr/handler with the default timeouts. Without
// ReadHeaderTimeout a client that opens a connection and dribbles headers pins it forever
// (slowloris).
func NewServer(addr string, handler http.Handler) *http.Server {
	s := &http.Server{Addr: addr, Handler: handler}
	ApplyServerDefaults(s)
	return s
}

// ApplyServerDefaults fills every timeout still at zero with its default; values the caller set
// are kept.
func ApplyServerDefaults(s *http.Server) {
	if s.ReadHeaderTimeout == 0 {
		s.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if s.ReadTimeout == 0 {
		s.ReadTimeout = DefaultReadTimeout
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = DefaultIdleTimeout
	}
	if s.MaxHeaderBytes == 0 {
		s.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
}

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
	return ServeContext(ctx, NewServer(addr, handler), DefaultShutdownTimeout)
}

// ServeContext runs server until ctx is cancelled, then shuts it down, waiting at most
// shutdownTimeout for in-flight requests.
func ServeContext(ctx context.Context, server *http.Server, shutdownTimeout time.Duration) error {
	ApplyServerDefaults(server)
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
