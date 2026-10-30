package httpserver

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/metap-org/metap-go/config"
	"github.com/metap-org/metap-go/runtime"
)

// BuildRouter mirrors the Rust build_router — health/auth/entity-CRUD routes with the global
// CORS/rate-limit/request-id/trace/security-header middleware stack applied in the same
// outermost-to-innermost order the Rust layering produces: GenerateRequestIDs, Trace,
// SecurityHeaders, RequestContext, rate limit, CORS, then route dispatch. extraRoutes lets a
// caller mount additional routers (future admin/platform routes) under the same middleware
// stack rather than bypassing it.
//
// Deferred vs. the Rust original for this core wave: metrics (/metrics, axum-prometheus) and
// metadata/attachments/workflow-events/audit-events/oauth2 route groups — none are needed to
// exercise entity CRUD, and each is additive (a later wave adds its own mountX function here
// without touching this wiring).
func BuildRouter(state *AppState, corsOrigins []string, extraRoutes func(chi.Router)) http.Handler {
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
	r.Use(runtime.GenerateRequestIDs)
	r.Use(runtime.Trace)
	r.Use(runtime.SecurityHeaders)
	r.Use(runtime.RequestContext)
	r.Use(rateLimit)
	r.Use(cors)

	mountHealth(r)
	mountMetadata(r)
	mountAuth(r, state)
	mountRecords(r, state)
	if extraRoutes != nil {
		extraRoutes(r)
	}

	return r
}
