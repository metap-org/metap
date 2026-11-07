package runtime

import "github.com/metap-org/metap/pkg/kit/shutdown"

// WaitForShutdownSignal blocks until SIGINT or SIGTERM. Equivalent to shutdown.Wait.
func WaitForShutdownSignal() { shutdown.Wait() }
