package web_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"github.com/metap-org/metap/pkg/web"
)

func ok(http.ResponseWriter, *http.Request) {}

func TestARouteOnTheBareRouterIsReported(t *testing.T) {
	r := router.New()
	state := &web.State{}
	web.Public(r, state).Get("/health", ok)
	web.Authenticated(r, state).Post("/jobs/{id}", ok)
	web.Admin(r, state).Get("/admin/x", ok)
	web.PlatformAdmin(r, state).Get("/platform/x", ok)
	web.SelfAuth(r, state).Post("/callback", ok)
	assert.Empty(t, web.RoutesWithoutOneAccessRule(r))

	r.Post("/forgotten", ok)
	bad := web.RoutesWithoutOneAccessRule(r)
	assert.Len(t, bad, 1)
	assert.Contains(t, bad[0], "POST /forgotten")
}

func TestARouteInTwoGatesIsReported(t *testing.T) {
	r := router.New()
	state := &web.State{}
	web.Admin(web.Public(r, state), state).Get("/both", ok)
	assert.Len(t, web.RoutesWithoutOneAccessRule(r), 1)
}
