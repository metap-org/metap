package runtime

import (
	"context"
	"net/http"
	"time"
)

// DefaultTimeout matches the Rust default — a bare http.Client{} has no timeout at all, which is
// the exact gap this helper exists to close (a hung upstream must not hang the caller forever).
const DefaultTimeout = 30 * time.Second

// NewHTTPClient builds an *http.Client with the given timeout.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// DefaultHTTPClient returns a client with DefaultTimeout.
func DefaultHTTPClient() *http.Client {
	return NewHTTPClient(DefaultTimeout)
}

// AttachTraceContext attaches the current request's W3C traceparent to an outbound request, if
// ctx carries one (via WithTraceContext) — a no-op otherwise, e.g. a boot-time or background call
// with no incoming request to propagate from.
func AttachTraceContext(ctx context.Context, req *http.Request) {
	if tc, ok := CurrentTrace(ctx); ok {
		req.Header.Set("traceparent", tc.ToTraceparentHeader())
	}
}
