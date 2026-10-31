package runtime

import "net/http"

const defaultCSP = "default-src 'self'; base-uri 'self'; font-src 'self' https: data:; " +
	"form-action 'self'; frame-ancestors 'self'; img-src 'self' data:; " +
	"object-src 'none'; script-src 'self'; script-src-attr 'none'; " +
	"style-src 'self' https: 'unsafe-inline'; upgrade-insecure-requests"

// SecurityHeaders is a helmet-equivalent middleware reproducing @fastify/helmet's default header
// set, since no Go HTTP framework ships this by default either. A handler that needs a different
// CSP (e.g. a GraphiQL playground loading third-party scripts) should set its own
// Content-Security-Policy header before this middleware runs — it only sets headers not already
// present, same as the Rust `.entry(...).or_insert_with(...)`.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Headers from the handler are written during ServeHTTP but not flushed until
		// WriteHeader/Write — so defaults can still be injected afterward as long as the handler
		// hasn't called WriteHeader yet. A handler that calls WriteHeader itself must set its own
		// CSP before doing so, matching the Rust version's ordering requirement.
		next.ServeHTTP(&securityHeaderWriter{ResponseWriter: w}, r)
	})
}

// securityHeaderWriter defers header defaults until the first WriteHeader/Write call, so a
// handler's own explicit header (set any time before then) always wins — mirrors axum's
// `next.run(request).await` returning a Response the Rust middleware then patches afterward.
type securityHeaderWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *securityHeaderWriter) applyDefaults() {
	h := w.Header()
	if h.Get("Content-Security-Policy") == "" {
		h.Set("Content-Security-Policy", defaultCSP)
	}
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Origin-Agent-Cluster", "?1")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Strict-Transport-Security", "max-age=15552000; includeSubDomains")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-DNS-Prefetch-Control", "off")
	h.Set("X-Download-Options", "noopen")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Permitted-Cross-Domain-Policies", "none")
	h.Set("X-XSS-Protection", "0")
}

func (w *securityHeaderWriter) WriteHeader(status int) {
	if !w.wrote {
		w.applyDefaults()
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *securityHeaderWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.applyDefaults()
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}
