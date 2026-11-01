package runtime

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// WaitForShutdownSignal blocks until SIGINT or SIGTERM, whichever comes first — Go's net/http
// has no built-in graceful-shutdown future, so this plays the role of the Rust `shutdown::signal`
// future a caller awaits before calling Server.Shutdown(ctx).
func WaitForShutdownSignal() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}
