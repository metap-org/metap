package runtime

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

// Error bodies are small JSON objects; this is just a sanity ceiling, not a tuned limit.
const maxErrorBodyBytes = 1024 * 1024

// RequestContext echoes x-request-id/x-trace-id response headers on every request, and folds
// both ids into any JSON error body (status >= 400) — ported from metap-runtime's
// request_context. A success response streams through untouched, exactly as the Rust version
// only buffers (to_bytes) the error branch.
func RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids, ok := CurrentRequestIDs(r.Context())
		if !ok {
			// Always present in practice — GenerateRequestIDs runs as the outermost middleware —
			// but degrade gracefully rather than panic if this is ever used standalone.
			ids = RequestIDs{RequestID: uuid.NewString(), TraceID: uuid.NewString()}
		}
		rcw := &requestContextWriter{ResponseWriter: w, requestID: ids.RequestID, traceID: ids.TraceID}
		next.ServeHTTP(rcw, r)
		rcw.finish()
	})
}

type requestContextWriter struct {
	http.ResponseWriter
	requestID, traceID string
	statusWritten      bool
	bufferMode         bool
	buf                bytes.Buffer
	status             int
}

func (w *requestContextWriter) WriteHeader(status int) {
	if w.statusWritten {
		return
	}
	w.statusWritten = true
	w.status = status
	w.Header().Set("x-request-id", w.requestID)
	w.Header().Set("x-trace-id", w.traceID)
	if status >= 400 {
		w.bufferMode = true
		return // deferred until finish(), so the body can still be patched
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestContextWriter) Write(b []byte) (int, error) {
	if !w.statusWritten {
		w.WriteHeader(http.StatusOK)
	}
	if w.bufferMode {
		return w.buf.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *requestContextWriter) finish() {
	if !w.statusWritten {
		w.WriteHeader(http.StatusOK)
	}
	if !w.bufferMode {
		return
	}
	body := w.buf.Bytes()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err == nil {
		if errObj, ok := parsed["error"].(map[string]any); ok {
			errObj["requestId"] = w.requestID
			errObj["traceId"] = w.traceID
			parsed["error"] = errObj
			if patched, mErr := json.Marshal(parsed); mErr == nil {
				body = patched
			}
		}
	}
	// The body length just changed; a stale Content-Length would make clients truncate or hang
	// waiting for bytes that aren't coming.
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(body)
}
