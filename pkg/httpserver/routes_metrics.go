// GET /metrics — mounts state.Metrics' Prometheus exposition handler. The actual recording happens
// in the runtime.MetricsRegistry.Middleware wired into BuildRouter's middleware stack; this file
// only mounts the scrape endpoint itself.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func mountMetrics(r chi.Router, state *AppState) {
	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		state.Metrics.Handler().ServeHTTP(w, r)
	})
}
