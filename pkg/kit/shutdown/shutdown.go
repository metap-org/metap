// Package shutdown handles process termination signals (SIGINT/SIGTERM).
package shutdown

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Context returns a context cancelled on SIGINT or SIGTERM; call stop to release the signal
// handler early.
func Context(parent context.Context) (ctx context.Context, stop context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// Wait blocks until SIGINT or SIGTERM is received.
func Wait() {
	ctx, stop := Context(context.Background())
	defer stop()
	<-ctx.Done()
}
