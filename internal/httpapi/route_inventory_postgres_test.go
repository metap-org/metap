//go:build integration

package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/internal/httpapi/attachmentroutes"
	"github.com/metap-org/metap/internal/httpapi/cronroutes"
	"github.com/metap-org/metap/internal/httpapi/dashboardroutes"
	"github.com/metap-org/metap/internal/httpapi/oauth2routes"
	"github.com/metap-org/metap/pkg/audit"
)

// The inventory of what the server answers — every method and path, and the OpenAPI document of the
// typed operations — pinned to golden files, so a change of router or of how routes are declared
// (roadmap 118) cannot add, drop or reshape an endpoint without a visible diff. Regenerate on purpose
// with UPDATE_GOLDEN=1 go test -tags integration -run TestRouteInventory ./pkg/httpapi.
func TestRouteInventory(t *testing.T) {
	pool := httpTestPool(t)
	state, _ := newTestAppState(t, pool, httpTestEntity())
	state.Identity = &admin.IdentityConfig{Shared: pool, Router: state.Router, Audit: audit.NewPostgresStore(pool), PublicURL: "http://portal.test"}
	handler := httpapi.BuildRouter(state, nil, oauth2routes.Mount, cronroutes.Mount, dashboardroutes.Mount, attachmentroutes.Mount)

	r, ok := handler.(*router.Router)
	require.True(t, ok)
	access := map[string]bool{
		httpapi.LabelPublic: true, httpapi.LabelAuthenticated: true, httpapi.LabelAdmin: true,
		httpapi.LabelPlatformAdmin: true, httpapi.LabelSelfAuth: true,
	}
	var lines []string
	for _, route := range r.Routes() {
		// Every route declares who may call it: it sits in a group labelled with one access rule.
		// A route with none was registered on the bare router, which is how an unguarded handler
		// gets added by accident.
		var rule []string
		for _, l := range route.Labels {
			if access[l] {
				rule = append(rule, l)
			}
		}
		require.Len(t, rule, 1, "route %q must sit in exactly one access group, has labels %v", route.Pattern, route.Labels)
		suffix := "  [" + rule[0] + "]"
		method, path, hasMethod := strings.Cut(route.Pattern, " ")
		if !hasMethod {
			// A pattern with no method serves every method (a Mount): list one line per method.
			for _, m := range []string{"CONNECT", "DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT", "QUERY", "TRACE"} {
				lines = append(lines, m+" "+route.Pattern+suffix)
			}
			continue
		}
		lines = append(lines, method+" "+path+suffix)
	}
	sort.Strings(lines)
	golden(t, "routes.golden", []byte(strings.Join(lines, "\n")+"\n"))

	base := startTestServer(t, state, nil)
	res := doRequest(t, "GET", base+"/metadata/api.json", "", nil)
	require.Equal(t, 200, res.StatusCode)
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	var pretty bytes.Buffer
	require.NoError(t, json.Indent(&pretty, raw, "", "  "))
	golden(t, "api.golden.json", append(pretty.Bytes(), '\n'))
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run with UPDATE_GOLDEN=1")
	require.Equal(t, string(want), string(got), "%s changed: run with UPDATE_GOLDEN=1 if the change is intended", name)
}
