package runtime

import (
	"net/http"
	"strings"
)

// CORSOptions mirrors the Rust cors::build's parameters.
type CORSOptions struct {
	Origins []string
	Methods []string
	Headers []string
}

// CORS builds a CORS middleware. Empty Origins means the restrictive default: no
// Access-Control-* headers are ever set, so a browser denies every cross-origin request — same
// as tower-http's bare CorsLayer::new(). A non-empty Origins list always sets
// Access-Control-Allow-Credentials: true alongside an explicit (never wildcard) origin list,
// since the CORS spec forbids combining credentials with a wildcard origin.
func CORS(opts CORSOptions) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(opts.Origins))
	for _, o := range opts.Origins {
		allowed[o] = true
	}
	methods := strings.Join(opts.Methods, ", ")
	headers := strings.Join(opts.Headers, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(allowed) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
