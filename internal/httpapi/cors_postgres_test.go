//go:build integration

package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/internal/httpapi/attachmentroutes"
	"github.com/metap-org/metap/internal/httpapi/cronroutes"
	"github.com/metap-org/metap/internal/httpapi/dashboardroutes"
	"github.com/metap-org/metap/internal/httpapi/oauth2routes"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
)

// A frontend on another origin can only call a method the preflight allows, so every method a route
// is registered with has to be in the CORS list.
func TestThePreflightAllowsEveryMethodARouteUses(t *testing.T) {
	pool := httpTestPool(t)
	state, _ := newTestAppState(t, pool, httpTestEntity())
	state.Identity = &admin.IdentityConfig{Shared: pool, Router: state.Router, Audit: audit.NewPostgresStore(pool), PublicURL: "http://portal.test"}
	const origin = "http://portal.test"
	handler := httpapi.BuildRouter(state, []string{origin}, oauth2routes.Mount, cronroutes.Mount, dashboardroutes.Mount, attachmentroutes.Mount)

	req := httptest.NewRequest(http.MethodOptions, "/preferences", nil)
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	allowed := map[string]bool{}
	for _, m := range strings.Split(rec.Header().Get("Access-Control-Allow-Methods"), ",") {
		allowed[strings.TrimSpace(m)] = true
	}

	routes, ok := handler.(*router.Router)
	require.True(t, ok)
	missing := map[string]string{}
	for _, route := range routes.Routes() {
		if method, _, hasMethod := strings.Cut(route.Pattern, " "); hasMethod && !allowed[method] {
			missing[method] = route.Pattern
		}
	}
	assert.Empty(t, missing, "methods with a route that the preflight refuses (one example each)")
}
