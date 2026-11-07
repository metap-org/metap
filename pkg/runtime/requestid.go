package runtime

import (
	"context"
	"net/http"
	"regexp"

	"github.com/google/uuid"
)

// RequestIDs is the request_id/trace_id pair generated once per request, the single source
// other middleware (Trace, RequestContext) reads from.
type RequestIDs struct {
	RequestID string
	TraceID   string
}

type requestIDsKey struct{}

// CurrentRequestIDs returns the ambient RequestIDs, or false if GenerateRequestIDs never ran.
func CurrentRequestIDs(ctx context.Context) (RequestIDs, bool) {
	ids, ok := ctx.Value(requestIDsKey{}).(RequestIDs)
	return ids, ok
}

var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

func isValidOpaqueID(v string) bool {
	return v != "" && opaqueIDPattern.MatchString(v)
}

func headerOrNewUUID(h http.Header, name string) string {
	if v := h.Get(name); v != "" && isValidOpaqueID(v) {
		return v
	}
	return uuid.New().String()
}

// GenerateRequestIDs must be the outermost middleware in a router's stack: it mints/propagates
// x-trace-id/x-request-id (stashed as RequestIDs) and parses the incoming W3C traceparent (via
// TraceContextFromHeaders), making both ambient for the rest of the request through r.Context().
func GenerateRequestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := headerOrNewUUID(r.Header, "x-trace-id")
		// A mesh ingress sets x-request-id before this service sees the request and expects it
		// preserved end-to-end — only mint a new one when genuinely absent.
		requestID := headerOrNewUUID(r.Header, "x-request-id")

		traceCtx := TraceContextFromHeaders(r.Header)

		ctx := context.WithValue(r.Context(), requestIDsKey{}, RequestIDs{RequestID: requestID, TraceID: traceID})
		ctx = WithTraceContext(ctx, traceCtx)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
