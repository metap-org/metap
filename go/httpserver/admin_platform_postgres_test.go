// E2E tests for the admin/platform route surface (routes_admin.go, routes_cron.go,
// routes_dashboards.go, routes_preferences.go, routes_platform_config.go,
// routes_tenant_config.go, routes_users.go) — a real httpserver.BuildRouter server, bound to a
// real socket, hit with real HTTP requests and real JWTs, against real Postgres. Reuses the
// pool/key/entity/server helpers already defined in http_server_postgres_test.go.
package httpserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/peripherals"
)

func TestAdminUsersRolesAndPoliciesLifecycle(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.policies WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM users WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	// create a new user with a role, via the admin surface
	createRes := doRequest(t, "POST", base+"/admin/users", adminToken, map[string]any{
		"email": "new-user@example.com", "password": "hunter2", "roles": []string{"member"},
	})
	if createRes.StatusCode != 201 {
		t.Fatalf("got %d", createRes.StatusCode)
	}
	created := decodeJSON(t, createRes)
	createdData := created["data"].(map[string]any)
	newUserID := createdData["userId"].(string)

	// duplicate email -> 409
	dupRes := doRequest(t, "POST", base+"/admin/users", adminToken, map[string]any{
		"email": "new-user@example.com", "password": "hunter2",
	})
	if dupRes.StatusCode != 409 {
		t.Fatalf("got %d", dupRes.StatusCode)
	}

	// GET /admin/users lists role assignments
	listRes := doRequest(t, "GET", base+"/admin/users", adminToken, nil)
	if listRes.StatusCode != 200 {
		t.Fatalf("got %d", listRes.StatusCode)
	}
	listed := decodeJSON(t, listRes)
	foundMember := false
	for _, row := range listed["data"].([]any) {
		m := row.(map[string]any)
		if m["userId"] == newUserID {
			foundMember = true
		}
	}
	if !foundMember {
		t.Fatalf("expected the newly created user in %v", listed["data"])
	}

	// GET /users (the plain picker) also lists it
	usersRes := doRequest(t, "GET", base+"/users", adminToken, nil)
	if usersRes.StatusCode != 200 {
		t.Fatalf("got %d", usersRes.StatusCode)
	}

	// assign/revoke an additional role
	assignRes := doRequest(t, "POST", base+"/admin/users/"+newUserID+"/roles", adminToken, map[string]any{"role": "viewer"})
	if assignRes.StatusCode != 201 {
		t.Fatalf("got %d", assignRes.StatusCode)
	}
	revokeRes := doRequest(t, "DELETE", base+"/admin/users/"+newUserID+"/roles/viewer", adminToken, nil)
	if revokeRes.StatusCode != 204 {
		t.Fatalf("got %d", revokeRes.StatusCode)
	}

	// invalidate-context is a no-op 204 even with nothing cached
	invalidateRes := doRequest(t, "POST", base+"/admin/users/"+newUserID+"/context/invalidate", adminToken, nil)
	if invalidateRes.StatusCode != 204 {
		t.Fatalf("got %d", invalidateRes.StatusCode)
	}

	// create a policy directly
	createPolicyRes := doRequest(t, "POST", base+"/admin/policies", adminToken, map[string]any{
		"entity": "test.orders", "action": "read", "roles": []string{"viewer"},
	})
	if createPolicyRes.StatusCode != 201 {
		t.Fatalf("got %d", createPolicyRes.StatusCode)
	}
	policyCreated := decodeJSON(t, createPolicyRes)
	policyID := policyCreated["data"].(map[string]any)["id"].(string)

	// seed-defaults bulk-creates one policy per known action
	seedRes := doRequest(t, "POST", base+"/admin/policies/seed-defaults", adminToken, map[string]any{
		"entity": "test.other_entity", "roles": []string{"member"},
	})
	if seedRes.StatusCode != 201 {
		t.Fatalf("got %d", seedRes.StatusCode)
	}
	seeded := decodeJSON(t, seedRes)
	if len(seeded["data"].([]any)) != 5 {
		t.Fatalf("expected one policy per known action (5), got %d", len(seeded["data"].([]any)))
	}

	// seed-defaults with an empty roles list is rejected
	badSeedRes := doRequest(t, "POST", base+"/admin/policies/seed-defaults", adminToken, map[string]any{
		"entity": "test.other_entity", "roles": []string{},
	})
	if badSeedRes.StatusCode != 400 {
		t.Fatalf("got %d", badSeedRes.StatusCode)
	}

	// list policies for an entity
	listPoliciesRes := doRequest(t, "GET", base+"/admin/policies?entity=test.orders", adminToken, nil)
	if listPoliciesRes.StatusCode != 200 {
		t.Fatalf("got %d", listPoliciesRes.StatusCode)
	}

	// the matrix sync replaces every basic-shaped policy for an entity in one call
	matrixRes := doRequest(t, "PUT", base+"/admin/policies/matrix", adminToken, map[string]any{
		"entity": "test.matrix_entity",
		"grants": []map[string]any{
			{"role": "admin", "action": "read"}, {"role": "member", "action": "read"}, {"role": "member", "action": "update"},
		},
	})
	if matrixRes.StatusCode != 200 {
		t.Fatalf("got %d", matrixRes.StatusCode)
	}
	matrixResult := decodeJSON(t, matrixRes)
	if len(matrixResult["data"].([]any)) != 3 {
		t.Fatalf("got %v", matrixResult["data"])
	}
	// an unknown action in a matrix grant is rejected
	badMatrixRes := doRequest(t, "PUT", base+"/admin/policies/matrix", adminToken, map[string]any{
		"entity": "test.matrix_entity", "grants": []map[string]any{{"role": "member", "action": "fly"}},
	})
	if badMatrixRes.StatusCode != 400 {
		t.Fatalf("got %d", badMatrixRes.StatusCode)
	}

	// explain reports whether the admin bypass or an actual policy allows the action
	explainRes := doRequest(t, "POST", base+"/admin/policies/explain", adminToken, map[string]any{
		"entity": "test.matrix_entity", "action": "read",
	})
	if explainRes.StatusCode != 200 {
		t.Fatalf("got %d", explainRes.StatusCode)
	}
	explained := decodeJSON(t, explainRes)
	if explained["data"].(map[string]any)["allowed"] != true {
		t.Fatalf("got %v", explained["data"])
	}

	// delete the first policy
	deletePolicyRes := doRequest(t, "DELETE", base+"/admin/policies/"+policyID, adminToken, nil)
	if deletePolicyRes.StatusCode != 204 {
		t.Fatalf("got %d", deletePolicyRes.StatusCode)
	}

	// a non-admin (no role at all) is forbidden from every admin route
	plainUserID := uuid.New()
	plainToken, err := peripherals.MintJWT(privPEM, tenantID, plainUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenRes := doRequest(t, "GET", base+"/admin/policies", plainToken, nil)
	if forbiddenRes.StatusCode != 403 {
		t.Fatalf("got %d", forbiddenRes.StatusCode)
	}
}

func TestCronJobLifecycleAndValidation(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.cron_job_runs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.cron_jobs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	// an unknown targetType is rejected
	badRes := doRequest(t, "POST", base+"/admin/cron-jobs", adminToken, map[string]any{
		"name": "Bad Job", "targetType": "not_a_real_type", "targetConfig": map[string]any{},
	})
	if badRes.StatusCode != 400 {
		t.Fatalf("got %d", badRes.StatusCode)
	}

	// schedule trigger with no cronExpr is rejected
	noCronRes := doRequest(t, "POST", base+"/admin/cron-jobs", adminToken, map[string]any{
		"name": "Bad Job", "targetType": "webhook", "targetConfig": map[string]any{"url": "https://example.com"},
	})
	if noCronRes.StatusCode != 400 {
		t.Fatalf("got %d", noCronRes.StatusCode)
	}

	// create a real job
	createRes := doRequest(t, "POST", base+"/admin/cron-jobs", adminToken, map[string]any{
		"name": "Nightly Webhook", "triggerType": "schedule", "cronExpr": "*/5 * * * *", "timezone": "UTC",
		"targetType": "webhook", "targetConfig": map[string]any{"url": "https://example.com/hook"},
	})
	if createRes.StatusCode != 201 {
		t.Fatalf("got %d: %v", createRes.StatusCode, decodeJSON(t, createRes))
	}
	created := decodeJSON(t, createRes)
	createdData := created["data"].(map[string]any)
	jobID := createdData["id"].(string)
	if createdData["dispatchMode"] != "outbox" || createdData["maxAttempts"] != float64(1) {
		t.Fatalf("defaults not applied: %v", createdData)
	}

	// list and get
	listRes := doRequest(t, "GET", base+"/admin/cron-jobs", adminToken, nil)
	if listRes.StatusCode != 200 {
		t.Fatalf("got %d", listRes.StatusCode)
	}
	getRes := doRequest(t, "GET", base+"/admin/cron-jobs/"+jobID, adminToken, nil)
	if getRes.StatusCode != 200 {
		t.Fatalf("got %d", getRes.StatusCode)
	}

	// update the name only — the effective trigger (unchanged) must still validate
	updateRes := doRequest(t, "PATCH", base+"/admin/cron-jobs/"+jobID, adminToken, map[string]any{"name": "Renamed Job"})
	if updateRes.StatusCode != 200 {
		t.Fatalf("got %d", updateRes.StatusCode)
	}
	updated := decodeJSON(t, updateRes)
	if updated["data"].(map[string]any)["name"] != "Renamed Job" {
		t.Fatalf("got %v", updated["data"])
	}

	// runs list — empty, since nothing has ever claimed/dispatched this job
	runsRes := doRequest(t, "GET", base+"/admin/cron-jobs/"+jobID+"/runs", adminToken, nil)
	if runsRes.StatusCode != 200 {
		t.Fatalf("got %d", runsRes.StatusCode)
	}

	// 404 on a nonexistent job
	notFoundRes := doRequest(t, "GET", base+"/admin/cron-jobs/"+uuid.New().String(), adminToken, nil)
	if notFoundRes.StatusCode != 404 {
		t.Fatalf("got %d", notFoundRes.StatusCode)
	}

	// delete
	deleteRes := doRequest(t, "DELETE", base+"/admin/cron-jobs/"+jobID, adminToken, nil)
	if deleteRes.StatusCode != 204 {
		t.Fatalf("got %d", deleteRes.StatusCode)
	}
}

func TestDashboardsPersonalAndTenantDefault(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.dashboard_configs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	// no layout saved yet -> data: null
	emptyRes := doRequest(t, "GET", base+"/dashboards/me", adminToken, nil)
	if emptyRes.StatusCode != 200 {
		t.Fatalf("got %d", emptyRes.StatusCode)
	}
	if decodeJSON(t, emptyRes)["data"] != nil {
		t.Fatal("expected no personal or tenant-default layout yet")
	}

	// save the tenant default (admin-only)
	saveDefaultRes := doRequest(t, "PUT", base+"/dashboards/tenant-default", adminToken, map[string]any{
		"layout": map[string]any{"widgets": []string{"revenue"}},
	})
	if saveDefaultRes.StatusCode != 200 {
		t.Fatalf("got %d", saveDefaultRes.StatusCode)
	}

	// now /dashboards/me falls back to the tenant default
	fallbackRes := doRequest(t, "GET", base+"/dashboards/me", adminToken, nil)
	if fallbackRes.StatusCode != 200 {
		t.Fatalf("got %d", fallbackRes.StatusCode)
	}
	fallback := decodeJSON(t, fallbackRes)["data"].(map[string]any)
	if fallback["ownerUserId"] != nil {
		t.Fatalf("the tenant default has no owner, got %v", fallback["ownerUserId"])
	}

	// save a personal override
	savePersonalRes := doRequest(t, "PUT", base+"/dashboards/me", adminToken, map[string]any{
		"layout": map[string]any{"widgets": []string{"mine"}},
	})
	if savePersonalRes.StatusCode != 200 {
		t.Fatalf("got %d", savePersonalRes.StatusCode)
	}
	mine := decodeJSON(t, doRequest(t, "GET", base+"/dashboards/me", adminToken, nil))["data"].(map[string]any)
	if mine["ownerUserId"] == nil {
		t.Fatal("the personal override must have an owner")
	}

	// a non-admin cannot write the tenant default
	plainUserID := uuid.New()
	plainToken, err := peripherals.MintJWT(privPEM, tenantID, plainUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenRes := doRequest(t, "PUT", base+"/dashboards/tenant-default", plainToken, map[string]any{
		"layout": map[string]any{},
	})
	if forbiddenRes.StatusCode != 403 {
		t.Fatalf("got %d", forbiddenRes.StatusCode)
	}
}

func TestPreferencesGetAndPut(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'member')", tenantID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_preferences WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	defaultRes := doRequest(t, "GET", base+"/preferences", token, nil)
	if defaultRes.StatusCode != 200 {
		t.Fatalf("got %d", defaultRes.StatusCode)
	}
	if decodeJSON(t, defaultRes)["data"].(map[string]any)["locale"] != "en" {
		t.Fatalf("got %v", decodeJSON(t, defaultRes))
	}

	badRes := doRequest(t, "PUT", base+"/preferences", token, map[string]any{"locale": "xx"})
	if badRes.StatusCode != 400 {
		t.Fatalf("got %d", badRes.StatusCode)
	}

	setRes := doRequest(t, "PUT", base+"/preferences", token, map[string]any{"locale": "vi"})
	if setRes.StatusCode != 200 {
		t.Fatalf("got %d", setRes.StatusCode)
	}
	getRes := doRequest(t, "GET", base+"/preferences", token, nil)
	if decodeJSON(t, getRes)["data"].(map[string]any)["locale"] != "vi" {
		t.Fatalf("got %v", decodeJSON(t, getRes))
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
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')",
		tenantAdminTenantID, tenantAdminUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.platform_configs WHERE key = 'auth.sessionTtlSeconds'")
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id = $1", platformAdminUserID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantAdminTenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	if err := state.Config.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	platformAdminToken, err := peripherals.MintJWT(privPEM, control.PlatformTenantID, platformAdminUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	tenantAdminToken, err := peripherals.MintJWT(privPEM, tenantAdminTenantID, tenantAdminUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	// a normal tenant admin — even with the "admin" role — is not a platform admin
	deniedRes := doRequest(t, "GET", base+"/platform/config", tenantAdminToken, nil)
	if deniedRes.StatusCode != 403 {
		t.Fatalf("got %d", deniedRes.StatusCode)
	}

	listRes := doRequest(t, "GET", base+"/platform/config", platformAdminToken, nil)
	if listRes.StatusCode != 200 {
		t.Fatalf("got %d", listRes.StatusCode)
	}
	items := decodeJSON(t, listRes)["data"].([]any)
	foundRateLimit := false
	for _, item := range items {
		m := item.(map[string]any)
		if m["key"] == "http.rateLimitPerMillisecond" {
			foundRateLimit = true
		}
		if m["key"] == "cron.webhookAllowPrivateTargets" {
			t.Fatal("an operator-tier key must never be listed here")
		}
	}
	if !foundRateLimit {
		t.Fatal("expected http.rateLimitPerMillisecond in the platform-writable view")
	}

	// setting the rate limit reports appliesImmediately: false (restart needed)
	setRateLimitRes := doRequest(t, "PUT", base+"/platform/config/http.rateLimitPerMillisecond", platformAdminToken,
		map[string]any{"value": 500})
	if setRateLimitRes.StatusCode != 200 {
		t.Fatalf("got %d", setRateLimitRes.StatusCode)
	}
	if decodeJSON(t, setRateLimitRes)["data"].(map[string]any)["appliesImmediately"] != false {
		t.Fatal("the rate limit keys must report appliesImmediately: false")
	}
	// put it back to avoid perturbing the shared dev DB's effective rate limit for other tests
	doRequest(t, "DELETE", base+"/platform/config/http.rateLimitPerMillisecond", platformAdminToken, nil)

	// set the fleet default for a tenant-tier key
	setRes := doRequest(t, "PUT", base+"/platform/config/auth.sessionTtlSeconds", platformAdminToken,
		map[string]any{"value": 7200})
	if setRes.StatusCode != 200 {
		t.Fatalf("got %d", setRes.StatusCode)
	}
	setData := decodeJSON(t, setRes)["data"].(map[string]any)
	if setData["appliesImmediately"] != true || setData["value"] != float64(7200) {
		t.Fatalf("got %v", setData)
	}

	// an out-of-range value is rejected
	invalidRes := doRequest(t, "PUT", base+"/platform/config/auth.sessionTtlSeconds", platformAdminToken,
		map[string]any{"value": -1})
	if invalidRes.StatusCode != 422 {
		t.Fatalf("got %d", invalidRes.StatusCode)
	}

	// an operator-tier key is refused with 403
	operatorRes := doRequest(t, "PUT", base+"/platform/config/cron.webhookAllowPrivateTargets", platformAdminToken,
		map[string]any{"value": true})
	if operatorRes.StatusCode != 403 {
		t.Fatalf("got %d", operatorRes.StatusCode)
	}

	// an unknown key is a 404
	unknownRes := doRequest(t, "PUT", base+"/platform/config/not.a.real.key", platformAdminToken,
		map[string]any{"value": 1})
	if unknownRes.StatusCode != 404 {
		t.Fatalf("got %d", unknownRes.StatusCode)
	}

	// reset falls back to the declared default
	resetRes := doRequest(t, "DELETE", base+"/platform/config/auth.sessionTtlSeconds", platformAdminToken, nil)
	if resetRes.StatusCode != 200 {
		t.Fatalf("got %d", resetRes.StatusCode)
	}
	if decodeJSON(t, resetRes)["data"].(map[string]any)["value"] != float64(3600) {
		t.Fatalf("got %v", decodeJSON(t, resetRes))
	}
}

func TestTenantConfigListSetResetAndPublicConfig(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	adminUserID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, adminUserID); err != nil {
		t.Fatal(err)
	}
	// SetTenantHostname below needs a real control.tenants row to satisfy the hostname table's FK
	// — a minimal schema-strategy row is enough, no real provisioning needed for this test.
	if _, err := pool.Exec(ctx, "INSERT INTO control.tenants (id, tier, strategy, schema_name, status) VALUES ($1, 'free', 'schema', 'public', 'active')", tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM control.tenant_hostnames WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.tenant_configs WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	if err := state.Config.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	adminToken, err := peripherals.MintJWT(privPEM, tenantID, adminUserID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	// list — the fleet default, not yet overridden
	listRes := doRequest(t, "GET", base+"/admin/config", adminToken, nil)
	if listRes.StatusCode != 200 {
		t.Fatalf("got %d", listRes.StatusCode)
	}
	items := decodeJSON(t, listRes)["data"].([]any)
	foundTheme := false
	for _, item := range items {
		m := item.(map[string]any)
		if m["key"] == "theme.primaryColor" {
			foundTheme = true
			if m["overridden"] != false || m["public"] != true {
				t.Fatalf("got %v", m)
			}
		}
	}
	if !foundTheme {
		t.Fatal("expected theme.primaryColor in the tenant view")
	}

	// a platform-tier key is not writable by a tenant admin
	platformKeyRes := doRequest(t, "PUT", base+"/admin/config/graphql.maxDepth", adminToken, map[string]any{"value": 5})
	if platformKeyRes.StatusCode != 403 {
		t.Fatalf("got %d", platformKeyRes.StatusCode)
	}

	// set a public branding key
	setThemeRes := doRequest(t, "PUT", base+"/admin/config/theme.primaryColor", adminToken, map[string]any{"value": "#336699"})
	if setThemeRes.StatusCode != 200 {
		t.Fatalf("got %d", setThemeRes.StatusCode)
	}
	if decodeJSON(t, setThemeRes)["data"].(map[string]any)["overridden"] != true {
		t.Fatal("expected overridden: true after setting it")
	}

	// an invalid color is rejected
	badColorRes := doRequest(t, "PUT", base+"/admin/config/theme.primaryColor", adminToken, map[string]any{"value": "not-a-color"})
	if badColorRes.StatusCode != 422 {
		t.Fatalf("got %d", badColorRes.StatusCode)
	}

	// a credential-tier key is refused — write it through the secret path, not as a config value
	// (this Go port, like the Rust REST surface it was recovered from, predates the fix that
	// wired that orchestration — see config.Store.SetTenant's own doc comment).
	secretRes := doRequest(t, "PUT", base+"/admin/config/cron.webhookAuthorization", adminToken, map[string]any{"value": "Bearer x"})
	if secretRes.StatusCode != 403 {
		t.Fatalf("got %d", secretRes.StatusCode)
	}

	// public config with no Host header, or one that maps to no tenant, serves the fleet
	// defaults — never this tenant's own override.
	noHostRes, err := http.Get(base + "/public/config")
	if err != nil {
		t.Fatal(err)
	}
	noHostItems := decodeJSON(t, noHostRes)["data"].([]any)
	for _, item := range noHostItems {
		m := item.(map[string]any)
		if m["key"] == "theme.primaryColor" && m["value"] != "" {
			t.Fatalf("expected the fleet default (empty) for an unmapped request, got %v", m["value"])
		}
	}

	// register this tenant's hostname, then request with that Host header
	if _, err := control.SetTenantHostname(ctx, pool, tenantID, "tenant.example.test"); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", base+"/public/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "tenant.example.test"
	withHostRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	withHostItems := decodeJSON(t, withHostRes)["data"].([]any)
	foundOverride := false
	for _, item := range withHostItems {
		m := item.(map[string]any)
		if m["key"] == "theme.primaryColor" {
			if m["value"] != "#336699" {
				t.Fatalf("got %v", m["value"])
			}
			foundOverride = true
		}
		// a non-public key must never appear, even though it was overridden
		if m["key"] == "auth.sessionTtlSeconds" {
			t.Fatal("a non-public key leaked through /public/config")
		}
	}
	if !foundOverride {
		t.Fatal("expected this tenant's branding override once its hostname resolves")
	}

	// reset back to the fleet default
	resetRes := doRequest(t, "DELETE", base+"/admin/config/theme.primaryColor", adminToken, nil)
	if resetRes.StatusCode != 200 {
		t.Fatalf("got %d", resetRes.StatusCode)
	}
	if decodeJSON(t, resetRes)["data"].(map[string]any)["overridden"] != false {
		t.Fatal("expected overridden: false after reset")
	}
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

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, tenantAdminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", control.PlatformTenantID, sentinelUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'platform_admin')", control.PlatformTenantID, platformAdminUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id = $1", sentinelUserID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id = $1", platformAdminUserID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	if err := state.Config.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	tenantAdminToken, _ := peripherals.MintJWT(privPEM, tenantID, tenantAdminUserID, 3600)
	sentinelToken, _ := peripherals.MintJWT(privPEM, control.PlatformTenantID, sentinelUserID, 3600)
	platformAdminToken, _ := peripherals.MintJWT(privPEM, control.PlatformTenantID, platformAdminUserID, 3600)

	if res := doRequest(t, "GET", base+"/platform/config", tenantAdminToken, nil); res.StatusCode != 403 {
		t.Fatalf("a normal tenant admin must not pass, got %d", res.StatusCode)
	}
	if res := doRequest(t, "GET", base+"/platform/config", sentinelToken, nil); res.StatusCode != 403 {
		t.Fatalf("the sentinel tenant without platform_admin must not pass, got %d", res.StatusCode)
	}
	if res := doRequest(t, "GET", base+"/platform/config", platformAdminToken, nil); res.StatusCode != 200 {
		t.Fatalf("the sentinel tenant with platform_admin must pass, got %d", res.StatusCode)
	}
}
