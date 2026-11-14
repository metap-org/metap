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

// BuildRouter mirrors the Rust build_router — the global middleware stack is applied in the
// same outermost-to-innermost order the Rust layering produces: GenerateRequestIDs, Trace,
// SecurityHeaders, RequestContext, rate limit, CORS, then route dispatch; every Mount is
// registered under that same stack rather than bypassing it.
//
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
	cors := runtime.CORS(runtime.CORSOptions{
		Origins: corsOrigins,
		Methods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete},
		Headers: []string{"Authorization", "Content-Type", "Accept"},
	})

	// Outermost first — see this function's own doc comment for why this order matches the
	// Rust original's reversed .layer() chain.
	// Recover is outermost so a panic anywhere below (including another middleware) becomes a
	// logged 500 in the standard envelope rather than a dropped connection.
	r.Use(httpx.Recover)
	r.Use(runtime.GenerateRequestIDs)
	r.Use(runtime.Trace)
	r.Use(state.Metrics.Middleware)
	r.Use(runtime.SecurityHeaders)
	r.Use(runtime.RequestContext)
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
