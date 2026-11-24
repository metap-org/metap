// GET /metrics — mounts state.Metrics' Prometheus exposition handler. The actual recording happens
// in the runtime.MetricsRegistry.Middleware wired into BuildRouter's middleware stack; this file
// only mounts the scrape endpoint itself.
package httpapi

import (
	"net/http"

	"github.com/metap-org/metap/pkg/kit/httpx/router"
)

func mountMetrics(r *router.Router, state *AppState) {
	acc := AccessGroups(r, state)
	acc.Public.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		state.Metrics.Handler().ServeHTTP(w, r)
	})
}
