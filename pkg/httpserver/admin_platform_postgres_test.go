//go:build integration

// E2E tests for the admin/platform route surface (routes_admin.go, routes_cron.go,
// routes_dashboards.go, routes_preferences.go, routes_platform_config.go,
// routes_tenant_config.go, routes_users.go) — a real httpserver.BuildRouter server, bound to a
// real socket, hit with real HTTP requests and real JWTs, against real Postgres. Reuses the
// pool/key/entity/server helpers already defined in http_server_postgres_test.go.
package httpserver_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/peripherals"
)

func TestAdminUsersRolesAndPoliciesLifecycle(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.policies WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM users WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	// create a new user with a role, via the admin surface
	createRes := doRequest(t, "POST", base+"/admin/users", adminToken, map[string]any{
		"email": "new-user@example.com", "password": "hunter2", "roles": []string{"member"},
	})
	require.Equal(t, 201, createRes.StatusCode, "got %d", createRes.StatusCode)
	created := decodeJSON(t, createRes)
	createdData := created["data"].(map[string]any)
	newUserID := createdData["userId"].(string)

	// duplicate email -> 409
	dupRes := doRequest(t, "POST", base+"/admin/users", adminToken, map[string]any{
		"email": "new-user@example.com", "password": "hunter2",
	})
	require.Equal(t, 409, dupRes.StatusCode, "got %d", dupRes.StatusCode)

	// GET /admin/users lists role assignments
	listRes := doRequest(t, "GET", base+"/admin/users", adminToken, nil)
	require.Equal(t, 200, listRes.StatusCode, "got %d", listRes.StatusCode)
	listed := decodeJSON(t, listRes)
	foundMember := false
	for _, row := range listed["data"].([]any) {
		m := row.(map[string]any)
		if m["userId"] == newUserID {
			foundMember = true
		}
	}
	require.True(t, foundMember, "expected the newly created user in %v", listed["data"])

	// GET /users (the plain picker) also lists it
	usersRes := doRequest(t, "GET", base+"/users", adminToken, nil)
	require.Equal(t, 200, usersRes.StatusCode, "got %d", usersRes.StatusCode)

	// assign/revoke an additional role
	assignRes := doRequest(t, "POST", base+"/admin/users/"+newUserID+"/roles", adminToken, map[string]any{"role": "viewer"})
	require.Equal(t, 201, assignRes.StatusCode, "got %d", assignRes.StatusCode)
	revokeRes := doRequest(t, "DELETE", base+"/admin/users/"+newUserID+"/roles/viewer", adminToken, nil)
	require.Equal(t, 204, revokeRes.StatusCode, "got %d", revokeRes.StatusCode)

	// invalidate-context is a no-op 204 even with nothing cached
	invalidateRes := doRequest(t, "POST", base+"/admin/users/"+newUserID+"/context/invalidate", adminToken, nil)
	require.Equal(t, 204, invalidateRes.StatusCode, "got %d", invalidateRes.StatusCode)

	// create a policy directly
	createPolicyRes := doRequest(t, "POST", base+"/admin/policies", adminToken, map[string]any{
		"entity": "test.orders", "action": "read", "roles": []string{"viewer"},
	})
	require.Equal(t, 201, createPolicyRes.StatusCode, "got %d", createPolicyRes.StatusCode)
	policyCreated := decodeJSON(t, createPolicyRes)
	policyID := policyCreated["data"].(map[string]any)["id"].(string)

	// seed-defaults bulk-creates one policy per known action
	seedRes := doRequest(t, "POST", base+"/admin/policies/seed-defaults", adminToken, map[string]any{
		"entity": "test.other_entity", "roles": []string{"member"},
	})
	require.Equal(t, 201, seedRes.StatusCode, "got %d", seedRes.StatusCode)
	seeded := decodeJSON(t, seedRes)
	require.Len(t, seeded["data"].([]any), 5, "expected one policy per known action (5), got %d", len(seeded["data"].([]any)))

	// seed-defaults with an empty roles list is rejected
	badSeedRes := doRequest(t, "POST", base+"/admin/policies/seed-defaults", adminToken, map[string]any{
		"entity": "test.other_entity", "roles": []string{},
	})
	require.Equal(t, 400, badSeedRes.StatusCode, "got %d", badSeedRes.StatusCode)

	// list policies for an entity
	listPoliciesRes := doRequest(t, "GET", base+"/admin/policies?entity=test.orders", adminToken, nil)
	require.Equal(t, 200, listPoliciesRes.StatusCode, "got %d", listPoliciesRes.StatusCode)

	// the matrix sync replaces every basic-shaped policy for an entity in one call
	matrixRes := doRequest(t, "PUT", base+"/admin/policies/matrix", adminToken, map[string]any{
		"entity": "test.matrix_entity",
		"grants": []map[string]any{
			{"role": "admin", "action": "read"}, {"role": "member", "action": "read"}, {"role": "member", "action": "update"},
		},
	})
	require.Equal(t, 200, matrixRes.StatusCode, "got %d", matrixRes.StatusCode)
	matrixResult := decodeJSON(t, matrixRes)
	require.Len(t, matrixResult["data"].([]any), 3, "got %v", matrixResult["data"])
	// an unknown action in a matrix grant is rejected
	badMatrixRes := doRequest(t, "PUT", base+"/admin/policies/matrix", adminToken, map[string]any{
		"entity": "test.matrix_entity", "grants": []map[string]any{{"role": "member", "action": "fly"}},
	})
	require.Equal(t, 400, badMatrixRes.StatusCode, "got %d", badMatrixRes.StatusCode)

	// explain reports whether the admin bypass or an actual policy allows the action
	explainRes := doRequest(t, "POST", base+"/admin/policies/explain", adminToken, map[string]any{
		"entity": "test.matrix_entity", "action": "read",
	})
	require.Equal(t, 200, explainRes.StatusCode, "got %d", explainRes.StatusCode)
	explained := decodeJSON(t, explainRes)
	require.Equal(t, true, explained["data"].(map[string]any)["allowed"], "got %v", explained["data"])

	// delete the first policy
	deletePolicyRes := doRequest(t, "DELETE", base+"/admin/policies/"+policyID, adminToken, nil)
	require.Equal(t, 204, deletePolicyRes.StatusCode, "got %d", deletePolicyRes.StatusCode)

	// a non-admin (no role at all) is forbidden from every admin route
	plainUserID := uuid.New()
	plainToken, err := peripherals.MintJWT(privPEM, tenantID, plainUserID, 3600)
	require.NoError(t, err)
	forbiddenRes := doRequest(t, "GET", base+"/admin/policies", plainToken, nil)
	require.Equal(t, 403, forbiddenRes.StatusCode, "got %d", forbiddenRes.StatusCode)
}

func TestCronJobLifecycleAndValidation(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.cron_job_runs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.cron_jobs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	// an unknown targetType is rejected
	badRes := doRequest(t, "POST", base+"/admin/cron-jobs", adminToken, map[string]any{
		"name": "Bad Job", "targetType": "not_a_real_type", "targetConfig": map[string]any{},
	})
	require.Equal(t, 400, badRes.StatusCode, "got %d", badRes.StatusCode)

	// schedule trigger with no cronExpr is rejected
	noCronRes := doRequest(t, "POST", base+"/admin/cron-jobs", adminToken, map[string]any{
		"name": "Bad Job", "targetType": "webhook", "targetConfig": map[string]any{"url": "https://example.com"},
	})
	require.Equal(t, 400, noCronRes.StatusCode, "got %d", noCronRes.StatusCode)

	// create a real job
	createRes := doRequest(t, "POST", base+"/admin/cron-jobs", adminToken, map[string]any{
		"name": "Nightly Webhook", "triggerType": "schedule", "cronExpr": "*/5 * * * *", "timezone": "UTC",
		"targetType": "webhook", "targetConfig": map[string]any{"url": "https://example.com/hook"},
	})
	if createRes.StatusCode != 201 {
		require.FailNow(t, fmt.Sprintf("got %d: %v", createRes.StatusCode, decodeJSON(t, createRes)))
	}
	created := decodeJSON(t, createRes)
	createdData := created["data"].(map[string]any)
	jobID := createdData["id"].(string)
	require.Equal(t, "outbox", createdData["dispatchMode"], "defaults not applied: %v", createdData)
	require.EqualValues(t, float64(1), createdData["maxAttempts"], "defaults not applied: %v", createdData)

	// list and get
	listRes := doRequest(t, "GET", base+"/admin/cron-jobs", adminToken, nil)
	require.Equal(t, 200, listRes.StatusCode, "got %d", listRes.StatusCode)
	getRes := doRequest(t, "GET", base+"/admin/cron-jobs/"+jobID, adminToken, nil)
	require.Equal(t, 200, getRes.StatusCode, "got %d", getRes.StatusCode)

	// update the name only — the effective trigger (unchanged) must still validate
	updateRes := doRequest(t, "PATCH", base+"/admin/cron-jobs/"+jobID, adminToken, map[string]any{"name": "Renamed Job"})
	require.Equal(t, 200, updateRes.StatusCode, "got %d", updateRes.StatusCode)
	updated := decodeJSON(t, updateRes)
	require.Equal(t, "Renamed Job", updated["data"].(map[string]any)["name"], "got %v", updated["data"])

	// runs list — empty, since nothing has ever claimed/dispatched this job
	runsRes := doRequest(t, "GET", base+"/admin/cron-jobs/"+jobID+"/runs", adminToken, nil)
	require.Equal(t, 200, runsRes.StatusCode, "got %d", runsRes.StatusCode)

	// 404 on a nonexistent job
	notFoundRes := doRequest(t, "GET", base+"/admin/cron-jobs/"+uuid.New().String(), adminToken, nil)
	require.Equal(t, 404, notFoundRes.StatusCode, "got %d", notFoundRes.StatusCode)

	// delete
	deleteRes := doRequest(t, "DELETE", base+"/admin/cron-jobs/"+jobID, adminToken, nil)
	require.Equal(t, 204, deleteRes.StatusCode, "got %d", deleteRes.StatusCode)
}

func TestDashboardsPersonalAndTenantDefault(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.dashboard_configs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	// no layout saved yet -> data: null
	emptyRes := doRequest(t, "GET", base+"/dashboards/me", adminToken, nil)
	require.Equal(t, 200, emptyRes.StatusCode, "got %d", emptyRes.StatusCode)
	require.Nil(t, decodeJSON(t, emptyRes)["data"], "expected no personal or tenant-default layout yet")

	// save the tenant default (admin-only)
	saveDefaultRes := doRequest(t, "PUT", base+"/dashboards/tenant-default", adminToken, map[string]any{
		"layout": map[string]any{"widgets": []string{"revenue"}},
	})
	require.Equal(t, 200, saveDefaultRes.StatusCode, "got %d", saveDefaultRes.StatusCode)

	// now /dashboards/me falls back to the tenant default
	fallbackRes := doRequest(t, "GET", base+"/dashboards/me", adminToken, nil)
	require.Equal(t, 200, fallbackRes.StatusCode, "got %d", fallbackRes.StatusCode)
	fallback := decodeJSON(t, fallbackRes)["data"].(map[string]any)
	require.Nil(t, fallback["ownerUserId"], "the tenant default has no owner, got %v", fallback["ownerUserId"])

	// save a personal override
	savePersonalRes := doRequest(t, "PUT", base+"/dashboards/me", adminToken, map[string]any{
		"layout": map[string]any{"widgets": []string{"mine"}},
	})
	require.Equal(t, 200, savePersonalRes.StatusCode, "got %d", savePersonalRes.StatusCode)
	mine := decodeJSON(t, doRequest(t, "GET", base+"/dashboards/me", adminToken, nil))["data"].(map[string]any)
	require.NotNil(t, mine["ownerUserId"], "the personal override must have an owner")

	// a non-admin cannot write the tenant default
	plainUserID := uuid.New()
	plainToken, err := peripherals.MintJWT(privPEM, tenantID, plainUserID, 3600)
	require.NoError(t, err)
	forbiddenRes := doRequest(t, "PUT", base+"/dashboards/tenant-default", plainToken, map[string]any{
		"layout": map[string]any{},
	})
	require.Equal(t, 403, forbiddenRes.StatusCode, "got %d", forbiddenRes.StatusCode)
}

func TestPreferencesGetAndPut(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'member')", tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_preferences WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	defaultRes := doRequest(t, "GET", base+"/preferences", token, nil)
	require.Equal(t, 200, defaultRes.StatusCode, "got %d", defaultRes.StatusCode)
	if decodeJSON(t, defaultRes)["data"].(map[string]any)["locale"] != "en" {
		require.FailNow(t, fmt.Sprintf("got %v", decodeJSON(t, defaultRes)))
	}

	badRes := doRequest(t, "PUT", base+"/preferences", token, map[string]any{"locale": "xx"})
	require.Equal(t, 400, badRes.StatusCode, "got %d", badRes.StatusCode)

	setRes := doRequest(t, "PUT", base+"/preferences", token, map[string]any{"locale": "vi"})
	require.Equal(t, 200, setRes.StatusCode, "got %d", setRes.StatusCode)
	getRes := doRequest(t, "GET", base+"/preferences", token, nil)
	if decodeJSON(t, getRes)["data"].(map[string]any)["locale"] != "vi" {
		require.FailNow(t, fmt.Sprintf("got %v", decodeJSON(t, getRes)))
	}
}

func TestPlatformConfigListSetAndReset(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	platformAdminUserID := uuid.New()
	tenantAdminTenantID := uuid.New()
	tenantAdminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'platform_admin')",
		control.PlatformTenantID, platformAdminUserID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')",
		tenantAdminTenantID, tenantAdminUserID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.platform_configs WHERE key = 'auth.sessionTtlSeconds'")
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id = $1", platformAdminUserID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantAdminTenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	require.NoError(t, state.Config.Reload(ctx))
	platformAdminToken, err := peripherals.MintJWT(privPEM, control.PlatformTenantID, platformAdminUserID, 3600)
	require.NoError(t, err)
	tenantAdminToken, err := peripherals.MintJWT(privPEM, tenantAdminTenantID, tenantAdminUserID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	// a normal tenant admin — even with the "admin" role — is not a platform admin
	deniedRes := doRequest(t, "GET", base+"/platform/config", tenantAdminToken, nil)
	require.Equal(t, 403, deniedRes.StatusCode, "got %d", deniedRes.StatusCode)

	listRes := doRequest(t, "GET", base+"/platform/config", platformAdminToken, nil)
	require.Equal(t, 200, listRes.StatusCode, "got %d", listRes.StatusCode)
	items := decodeJSON(t, listRes)["data"].([]any)
	foundRateLimit := false
	for _, item := range items {
		m := item.(map[string]any)
		if m["key"] == "http.rateLimitPerMillisecond" {
			foundRateLimit = true
		}
		require.NotEqual(t, "cron.webhookAllowPrivateTargets", m["key"], "an operator-tier key must never be listed here")
	}
	require.True(t, foundRateLimit, "expected http.rateLimitPerMillisecond in the platform-writable view")

	// setting the rate limit reports appliesImmediately: false (restart needed)
	setRateLimitRes := doRequest(t, "PUT", base+"/platform/config/http.rateLimitPerMillisecond", platformAdminToken,
		map[string]any{"value": 500})
	require.Equal(t, 200, setRateLimitRes.StatusCode, "got %d", setRateLimitRes.StatusCode)
	require.Equal(t, false, decodeJSON(t, setRateLimitRes)["data"].(map[string]any)["appliesImmediately"], "the rate limit keys must report appliesImmediately: false")
	// put it back to avoid perturbing the shared dev DB's effective rate limit for other tests
	doRequest(t, "DELETE", base+"/platform/config/http.rateLimitPerMillisecond", platformAdminToken, nil)

	// set the fleet default for a tenant-tier key
	setRes := doRequest(t, "PUT", base+"/platform/config/auth.sessionTtlSeconds", platformAdminToken,
		map[string]any{"value": 7200})
	require.Equal(t, 200, setRes.StatusCode, "got %d", setRes.StatusCode)
	setData := decodeJSON(t, setRes)["data"].(map[string]any)
	require.Equal(t, true, setData["appliesImmediately"], "got %v", setData)
	require.EqualValues(t, float64(7200), setData["value"], "got %v", setData)

	// an out-of-range value is rejected
	invalidRes := doRequest(t, "PUT", base+"/platform/config/auth.sessionTtlSeconds", platformAdminToken,
		map[string]any{"value": -1})
	require.Equal(t, 422, invalidRes.StatusCode, "got %d", invalidRes.StatusCode)

	// an operator-tier key is refused with 403
	operatorRes := doRequest(t, "PUT", base+"/platform/config/cron.webhookAllowPrivateTargets", platformAdminToken,
		map[string]any{"value": true})
	require.Equal(t, 403, operatorRes.StatusCode, "got %d", operatorRes.StatusCode)

	// an unknown key is a 404
	unknownRes := doRequest(t, "PUT", base+"/platform/config/not.a.real.key", platformAdminToken,
		map[string]any{"value": 1})
	require.Equal(t, 404, unknownRes.StatusCode, "got %d", unknownRes.StatusCode)

	// reset falls back to the declared default
	resetRes := doRequest(t, "DELETE", base+"/platform/config/auth.sessionTtlSeconds", platformAdminToken, nil)
	require.Equal(t, 200, resetRes.StatusCode, "got %d", resetRes.StatusCode)
	if decodeJSON(t, resetRes)["data"].(map[string]any)["value"] != float64(3600) {
		require.FailNow(t, fmt.Sprintf("got %v", decodeJSON(t, resetRes)))
	}
}

func TestTenantConfigListSetResetAndPublicConfig(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	// SetTenantHostname below needs a real control.tenants row to satisfy the hostname table's FK
	// — a minimal schema-strategy row is enough, no real provisioning needed for this test.
	if _, err := pool.Exec(ctx, "INSERT INTO control.tenants (id, tier, strategy, schema_name, status) VALUES ($1, 'free', 'schema', 'public', 'active')", tenantID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM control.tenant_hostnames WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.tenant_configs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	require.NoError(t, state.Config.Reload(ctx))
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	// list — the fleet default, not yet overridden
	listRes := doRequest(t, "GET", base+"/admin/config", adminToken, nil)
	require.Equal(t, 200, listRes.StatusCode, "got %d", listRes.StatusCode)
	items := decodeJSON(t, listRes)["data"].([]any)
	foundTheme := false
	for _, item := range items {
		m := item.(map[string]any)
		if m["key"] == "theme.primaryColor" {
			foundTheme = true
			require.Equal(t, false, m["overridden"], "got %v", m)
			require.Equal(t, true, m["public"], "got %v", m)
		}
	}
	require.True(t, foundTheme, "expected theme.primaryColor in the tenant view")

	// a platform-tier key is not writable by a tenant admin
	platformKeyRes := doRequest(t, "PUT", base+"/admin/config/graphql.maxDepth", adminToken, map[string]any{"value": 5})
	require.Equal(t, 403, platformKeyRes.StatusCode, "got %d", platformKeyRes.StatusCode)

	// set a public branding key
	setThemeRes := doRequest(t, "PUT", base+"/admin/config/theme.primaryColor", adminToken, map[string]any{"value": "#336699"})
	require.Equal(t, 200, setThemeRes.StatusCode, "got %d", setThemeRes.StatusCode)
	require.Equal(t, true, decodeJSON(t, setThemeRes)["data"].(map[string]any)["overridden"], "expected overridden: true after setting it")

	// an invalid color is rejected
	badColorRes := doRequest(t, "PUT", base+"/admin/config/theme.primaryColor", adminToken, map[string]any{"value": "not-a-color"})
	require.Equal(t, 422, badColorRes.StatusCode, "got %d", badColorRes.StatusCode)

	// a credential-tier key is refused — write it through the secret path, not as a config value
	// (this Go port, like the Rust REST surface it was recovered from, predates the fix that
	// wired that orchestration — see config.Store.SetTenant's own doc comment).
	secretRes := doRequest(t, "PUT", base+"/admin/config/cron.webhookAuthorization", adminToken, map[string]any{"value": "Bearer x"})
	require.Equal(t, 403, secretRes.StatusCode, "got %d", secretRes.StatusCode)

	// public config with no Host header, or one that maps to no tenant, serves the fleet
	// defaults — never this tenant's own override.
	noHostRes, err := http.Get(base + "/public/config")
	require.NoError(t, err)
	noHostItems := decodeJSON(t, noHostRes)["data"].([]any)
	for _, item := range noHostItems {
		m := item.(map[string]any)
		require.False(t, m["key"] == "theme.primaryColor" && m["value"] != "", "expected the fleet default (empty) for an unmapped request, got %v", m["value"])
	}

	// register this tenant's hostname, then request with that Host header
	_, err = control.SetTenantHostname(ctx, pool, tenantID, "tenant.example.test")
	require.NoError(t, err)
	req, err := http.NewRequest("GET", base+"/public/config", nil)
	require.NoError(t, err)
	req.Host = "tenant.example.test"
	withHostRes, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	withHostItems := decodeJSON(t, withHostRes)["data"].([]any)
	foundOverride := false
	for _, item := range withHostItems {
		m := item.(map[string]any)
		if m["key"] == "theme.primaryColor" {
			require.Equal(t, "#336699", m["value"], "got %v", m["value"])
			foundOverride = true
		}
		// a non-public key must never appear, even though it was overridden
		require.NotEqual(t, "auth.sessionTtlSeconds", m["key"], "a non-public key leaked through /public/config")
	}
	require.True(t, foundOverride, "expected this tenant's branding override once its hostname resolves")

	// reset back to the fleet default
	resetRes := doRequest(t, "DELETE", base+"/admin/config/theme.primaryColor", adminToken, nil)
	require.Equal(t, 200, resetRes.StatusCode, "got %d", resetRes.StatusCode)
	require.Equal(t, false, decodeJSON(t, resetRes)["data"].(map[string]any)["overridden"], "expected overridden: false after reset")
}

// Mirrors the Rust platform_admin_context_gates_by_sentinel_tenant_and_role test: a normal
// tenant admin (even with the "admin" role) must not pass requirePlatformAdmin, a user in the
// platform sentinel tenant without "platform_admin" must not pass, and only a user in the
// platform sentinel tenant *with* "platform_admin" passes.
func TestPlatformAdminContextGatesBySentinelTenantAndRole(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	tenantAdminUserID := uuid.New()
	sentinelUserID := uuid.New()
	platformAdminUserID := uuid.New()

	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, tenantAdminUserID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", control.PlatformTenantID, sentinelUserID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'platform_admin')", control.PlatformTenantID, platformAdminUserID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id = $1", sentinelUserID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id = $1", platformAdminUserID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	require.NoError(t, state.Config.Reload(ctx))
	base := startTestServer(t, state, nil)

	tenantAdminToken, _ := peripherals.MintJWT(privPEM, tenantID, tenantAdminUserID, 3600)
	sentinelToken, _ := peripherals.MintJWT(privPEM, control.PlatformTenantID, sentinelUserID, 3600)
	platformAdminToken, _ := peripherals.MintJWT(privPEM, control.PlatformTenantID, platformAdminUserID, 3600)

	res := doRequest(t, "GET", base+"/platform/config", tenantAdminToken, nil)
	require.Equal(t, 403, res.StatusCode, "a normal tenant admin must not pass, got %d", res.StatusCode)
	res = doRequest(t, "GET", base+"/platform/config", sentinelToken, nil)
	require.Equal(t, 403, res.StatusCode, "the sentinel tenant without platform_admin must not pass, got %d", res.StatusCode)
	res = doRequest(t, "GET", base+"/platform/config", platformAdminToken, nil)
	require.Equal(t, 200, res.StatusCode, "the sentinel tenant with platform_admin must pass, got %d", res.StatusCode)
}
