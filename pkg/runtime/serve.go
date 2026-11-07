package runtime

import (
	"net/http"

	"github.com/metap-org/metap/pkg/kit/httpx"
)

// Serve binds addr and serves handler, shutting down gracefully on SIGINT/SIGTERM. Equivalent to
// httpx.Serve.
func Serve(addr string, handler http.Handler) error { return httpx.Serve(addr, handler) }
