package httpserver

import (
	"github.com/metap-org/metap/pkg/kit/httpx"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/runtime"
)

// BuildRouter builds the HTTP handler: middleware, then the core route groups every service has
// (health, metrics, metadata, auth, records, users, admin, preferences, platform/tenant config,
// workflow and audit events), then each of mounts in order — the optional groups
// (oauth2routes/cronroutes/dashboardroutes/attachmentroutes) and the service's own routes.
func BuildRouter(state *AppState, corsOrigins []string, mounts ...Mount) http.Handler {
	r := chi.NewRouter()

	cfg := state.Config.Current()
	rateLimit := runtime.RateLimit(
		time.Duration(cfg.GetU64(config.HTTPRateLimitPerMs))*time.Millisecond,
		int(cfg.GetU64(config.HTTPRateLimitBurst)),
	)
	cors := httpx.CORS(httpx.CORSOptions{
		Origins: corsOrigins,
		Methods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete},
		Headers: []string{"Authorization", "Content-Type", "Accept"},
	})

	r.Use(httpx.GenerateRequestIDs)
	r.Use(httpx.Trace)
	r.Use(state.Metrics.Middleware)
	r.Use(httpx.SecurityHeaders)
	r.Use(httpx.RequestContext)
	// Recover sits just inside RequestContext on purpose: a handler panic becomes a logged 500 in
	// the standard envelope that still passes back through RequestContext (so it carries
	// x-request-id and requestId/traceId), SecurityHeaders and the metrics, instead of a dropped
	// connection. Outermost would skip all three.
	r.Use(httpx.Recover)
	r.Use(rateLimit)
	r.Use(cors)

	mountHealth(r)
	if state.JwksKeys != nil {
		r.Get("/.well-known/jwks.json", jwks.Handler(state.JwksKeys))
	}
	mountMetrics(r, state)
	mountMetadata(r, state)
	mountAuth(r, state)
	mountRecords(r, state)
	mountUsers(r, state)
	mountAdmin(r, state)
	mountPreferences(r, state)
	mountPlatformConfig(r, state)
	mountTenantConfig(r, state)
	mountWorkflowEvents(r, state)
	mountAuditEvents(r, state)
	for _, mount := range mounts {
		if mount != nil {
			mount(r, state)
		}
	}

	return r
}
