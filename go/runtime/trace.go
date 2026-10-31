package runtime

import (
	"log/slog"
	"net/http"
	"time"
)

// statusCapturingWriter records the status code written so Trace can log it after the handler
// returns — net/http has no equivalent of tower_http's on_response/on_failure hooks built in.
type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Trace logs one structured access-log line per request, correlated by the request/trace ids
// GenerateRequestIDs put in the context — ported from metap-runtime's trace::build. Must run
// inside (after) GenerateRequestIDs in the middleware chain.
func Trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}

		ids, _ := CurrentRequestIDs(r.Context())
		traceCtx, hasTrace := CurrentTrace(r.Context())
		legacyTraceID, requestID := ids.TraceID, ids.RequestID
		if legacyTraceID == "" {
			legacyTraceID = "unknown"
		}
		if requestID == "" {
			requestID = "unknown"
		}
		traceID, spanID := "unknown", "unknown"
		if hasTrace {
			traceID, spanID = traceCtx.TraceID, traceCtx.SpanID
		}

		next.ServeHTTP(sw, r)

		latency := time.Since(start)
		slog.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"request_id", requestID,
			"legacy_trace_id", legacyTraceID,
			"trace_id", traceID,
			"span_id", spanID,
			"status", sw.status,
			"latency_ms", latency.Milliseconds(),
		)
	})
}
