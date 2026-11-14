package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover turns a handler panic into a logged error and a 500 in the standard envelope, instead
// of a dropped connection. http.ErrAbortHandler is re-panicked: it is the deliberate way to abort
// a response and net/http expects to see it. If the handler had already started the response the
// connection is left to net/http, since a second status cannot be sent.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracker := &statusTracker{ResponseWriter: w}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			slog.Error("panic in handler", "panic", rec, "method", r.Method, "path", r.URL.Path, "stack", string(debug.Stack()))
			if !tracker.wrote {
				WriteError(w, http.StatusInternalServerError, "internal_error", "Internal server error.")
			}
		}()
		next.ServeHTTP(tracker, r)
	})
}

// MaxBody caps every request body at maxBytes (DefaultMaxBodyBytes when <= 0) for handlers that
// read it themselves. Do not apply it to an upload route that legitimately takes more.
func MaxBody(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// statusTracker records whether the response has started, and still exposes Flush/Unwrap so
// streaming handlers and http.ResponseController keep working through it.
type statusTracker struct {
	http.ResponseWriter
	wrote bool
}

func (t *statusTracker) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *statusTracker) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

func (t *statusTracker) Flush() {
	t.wrote = true
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *statusTracker) Unwrap() http.ResponseWriter { return t.ResponseWriter }
