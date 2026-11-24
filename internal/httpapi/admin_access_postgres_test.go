//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"github.com/metap-org/metap/pkg/testkit"
)

type servedOperation struct{ id, method, path string }

// servedOperations reads the typed API's OpenAPI document: every operation the server really serves.
func servedOperations(t *testing.T, base string) []servedOperation {
	t.Helper()
	res, err := http.Get(base + httpapi.APIDocPath + ".json")
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	var out []servedOperation
	for path, methods := range doc.Paths {
		for method, op := range methods {
			out = append(out, servedOperation{id: op.OperationID, method: strings.ToUpper(method), path: path})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// concrete fills a path template with values that parse: a uuid for ids, a word for the rest.
func concrete(path string) string {
	for {
		i := strings.Index(path, "{")
		if i < 0 {
			return path
		}
		j := strings.Index(path[i:], "}") + i
		value := "x"
		if strings.Contains(strings.ToLower(path[i:j]), "id") {
			value = uuid.NewString()
		}
		path = path[:i] + value + path[j+1:]
	}
}

// Every administrative operation says who may call it, and the server refuses everyone else before
// the operation reads its input: no token is 401; an ordinary member is 403 wherever the operation
// needs more than a signed-in caller; a tenant's own admin is 403 on a platform operation.
func TestEveryAdministrativeOperationRefusesWhoItShould(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminMember, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "admin@acme.io", Password: "password-1"})
	require.NoError(t, err)
	testkit.SeedRole(t, pool, tenant, adminMember.ID, "admin")
	plain, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "plain@acme.io", Password: "password-1"})
	require.NoError(t, err)

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	state.Identity = &admin.IdentityConfig{Shared: pool, Router: state.Router, Audit: audit.NewPostgresStore(pool), PublicURL: "http://portal.test"}
	base := startTestServer(t, state, nil)
	token := func(tenantID, user uuid.UUID) string {
		tok, err := jwks.MintRSAJWT(privPEM, tenantID, user, 3600)
		require.NoError(t, err)
		return tok
	}
	asAdmin, asPlain := token(tenant, adminMember.ID), token(tenant, plain.ID)

	served := servedOperations(t, base)
	table := httpapi.AdminOperationAccess()
	byID := map[string]servedOperation{}
	for _, op := range served {
		byID[op.id] = op
	}
	// What the table lists is served, and the other way round is guaranteed by registration.
	for id := range table {
		_, ok := byID[id]
		assert.True(t, ok, "%s is in the access table but not served", id)
	}
	require.NotEmpty(t, table)

	counts := map[string]int{}
	for id, level := range table {
		op, ok := byID[id]
		if !ok {
			continue
		}
		counts[level]++
		url := base + concrete(op.path)
		var body map[string]any
		if op.method != http.MethodGet && op.method != http.MethodDelete {
			body = map[string]any{}
		}

		assert.Equal(t, http.StatusUnauthorized, doRequest(t, op.method, url, "", body).StatusCode, "%s (%s) with no token", id, level)

		switch level {
		case "admin", "durable", "platformAdmin":
			assert.Equal(t, http.StatusForbidden, doRequest(t, op.method, url, asPlain, body).StatusCode, "%s (%s) as an ordinary member", id, level)
		case "authenticated":
			if op.method == http.MethodGet {
				code := doRequest(t, op.method, url, asPlain, nil).StatusCode
				assert.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, code, "%s (authenticated) as an ordinary member", id)
			}
		}
		if level == "platformAdmin" {
			assert.Equal(t, http.StatusForbidden, doRequest(t, op.method, url, asAdmin, body).StatusCode, "%s (platform) as a tenant admin", id)
		}
	}
	t.Logf("operations by level: %v", counts)
	assert.Positive(t, counts["admin"])
	assert.Positive(t, counts["durable"])
	assert.Positive(t, counts["platformAdmin"])
	assert.Positive(t, counts["authenticated"])
}
