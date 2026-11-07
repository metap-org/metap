package runtime

import (
	"context"
	"net/http"
	"time"

	"github.com/metap-org/metap/pkg/kit/httpx"
)

// DefaultTimeout bounds every call made through DefaultHTTPClient (= httpx.DefaultClientTimeout).
const DefaultTimeout = httpx.DefaultClientTimeout

// NewHTTPClient builds an *http.Client with the given timeout. Equivalent to httpx.NewClient.
func NewHTTPClient(timeout time.Duration) *http.Client { return httpx.NewClient(timeout) }

// DefaultHTTPClient returns a client with DefaultTimeout. Equivalent to httpx.DefaultClient.
func DefaultHTTPClient() *http.Client { return httpx.DefaultClient() }

// AttachTraceContext attaches the current request's W3C traceparent to an outbound request, if
// ctx carries one (via WithTraceContext) — a no-op otherwise, e.g. a boot-time or background call
// with no incoming request to propagate from.
func AttachTraceContext(ctx context.Context, req *http.Request) {
	if tc, ok := CurrentTrace(ctx); ok {
		req.Header.Set("traceparent", tc.ToTraceparentHeader())
	}
}
