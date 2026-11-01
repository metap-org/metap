package runtime

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Serve binds addr and serves handler, shutting down gracefully on SIGINT/SIGTERM — ported from
// metap-runtime's serve::run (TcpListener::bind + axum::serve().with_graceful_shutdown(...)).
func Serve(addr string, handler http.Handler) error {
	server := &http.Server{Addr: addr, Handler: handler}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	WaitForShutdownSignal()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return err
	}
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
