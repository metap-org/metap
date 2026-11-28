package app

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"github.com/metap-org/metap/pkg/web"
)

func answer(http.ResponseWriter, *http.Request) {}

func TestAServiceWithARouteOutsideTheAccessGroupsDoesNotStart(t *testing.T) {
	state := web.NewState(nil, nil, nil, nil, "", nil, nil)
	handlerWith := func(routes httpapi.Mount) error {
		_, err := buildHandler(state, nil, append(DefaultModules(), routes))
		return err
	}

	require.NoError(t, handlerWith(nil), "metap's own routes and optional modules")
	require.NoError(t, handlerWith(func(r *router.Router, state *web.State) {
		web.Authenticated(r, state).Post("/internal/jobs/{id}", answer)
		web.SelfAuth(r, state).Post("/internal/callback", answer)
	}))

	err := handlerWith(func(r *router.Router, _ *web.State) { r.Post("/internal/forgotten", answer) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /internal/forgotten")

	err = handlerWith(func(r *router.Router, state *web.State) { web.Admin(web.Public(r, state), state).Get("/both", answer) })
	require.Error(t, err, "two groups say two different things about who may call it")
	assert.Contains(t, err.Error(), "GET /both")
}
