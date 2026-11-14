package graphqlgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/graphql-go/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

type seenRequest struct {
	method, path, query, auth string
	body                      map[string]interface{}
}

const (
	userID   = "11111111-1111-1111-1111-111111111111"
	policyID = "22222222-2222-2222-2222-222222222222"
	tenantID = "33333333-3333-3333-3333-333333333333"
	jobID    = "44444444-4444-4444-4444-444444444444"
)

var cannedPolicy = map[string]interface{}{
	"id": policyID, "tenantId": tenantID, "entity": "shop.customers", "action": "read", "field": nil,
	"subject": "role", "roles": []string{"viewer"}, "condition": nil, "createdBy": nil, "effect": "allow",
}

var cannedCronJob = map[string]interface{}{
	"id": jobID, "tenantId": tenantID, "name": "nightly", "enabled": true, "triggerType": "schedule",
	"cronExpr": "0 3 * * *", "timezone": "UTC", "targetType": "webhook", "targetConfig": map[string]interface{}{"url": "https://x"},
	"dispatchMode": "direct", "maxAttempts": 3, "retryBackoffSeconds": 60,
	"createdAt": "2026-11-08T00:00:00Z", "updatedAt": "2026-11-08T00:00:00Z",
}

// fakePlatformREST answers like metap's REST routes and records what the gateway sent.
func fakePlatformREST(t *testing.T) (*httptest.Server, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			require.NoError(t, json.Unmarshal(raw, &req.body))
		}
		seen = append(seen, req)

		var data interface{}
		switch r.Method + " " + r.URL.Path {
		case "GET /admin/users":
			data = []interface{}{map[string]interface{}{"userId": userID, "roles": []string{"admin"}}}
		case "GET /users":
			data = []interface{}{map[string]interface{}{"id": userID, "email": "a@x"}}
		case "POST /admin/users":
			w.WriteHeader(http.StatusCreated)
			data = map[string]interface{}{"userId": userID, "email": req.body["email"], "roles": req.body["roles"]}
		case "POST /admin/users/" + userID + "/roles":
			w.WriteHeader(http.StatusCreated)
			data = map[string]interface{}{"userId": userID, "role": "viewer"}
		case "DELETE /admin/users/" + userID + "/roles/viewer", "DELETE /admin/policies/" + policyID, "DELETE /admin/cron-jobs/" + jobID:
			w.WriteHeader(http.StatusNoContent)
			return
		case "GET /admin/policies", "PUT /admin/policies/matrix":
			data = []interface{}{cannedPolicy}
		case "POST /admin/policies":
			w.WriteHeader(http.StatusCreated)
			data = cannedPolicy
		case "GET /admin/cron-jobs":
			data = []interface{}{cannedCronJob}
		case "POST /admin/cron-jobs", "PATCH /admin/cron-jobs/" + jobID:
			data = cannedCronJob
		case "GET /admin/cron-jobs/" + jobID + "/runs":
			data = []interface{}{}
		case "GET /preferences", "PUT /preferences":
			data = map[string]interface{}{"locale": "vi"}
		case "GET /admin/config":
			data = []interface{}{map[string]interface{}{"key": "auth.sessionTtlSeconds", "value": 3600, "level": "tenant", "overridden": false, "public": false}}
		case "PUT /admin/config/auth.sessionTtlSeconds":
			data = map[string]interface{}{"key": "auth.sessionTtlSeconds", "value": req.body["value"], "overridden": true}
		case "DELETE /admin/config/auth.sessionTtlSeconds":
			data = map[string]interface{}{"key": "auth.sessionTtlSeconds", "value": 3600, "overridden": false}
		case "GET /admin/cron-jobs/" + jobID:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"cron_job_not_found","message":"Cron job not found."}}`))
			return
		case "GET /platform/config":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"This action requires the platform_admin role."}}`))
			return
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func platformSchema(t *testing.T, base string) graphql.Schema {
	t.Helper()
	registry := shopRegistry(t)
	schema, err := BuildSchema(registry, map[string]crud.RecordBackend{"shop.customers": &fakeBackend{}}, WithPlatformBase(base))
	require.NoError(t, err)
	return schema
}

func exec(schema graphql.Schema, q string, vars map[string]interface{}) *graphql.Result {
	ctx := withRequestContext(context.Background(), &permission.RequestContext{ForwardedBearerToken: "caller-token"})
	return graphql.Do(graphql.Params{Schema: schema, RequestString: q, VariableValues: vars, Context: ctx})
}

// Every query/mutation string below is copied verbatim from @metap/platform-ui (adminApi.ts,
// useTenantUsers.ts, LocaleProvider.tsx) or this repo's web/src/pages/SettingsPage.tsx.
func TestPlatformUIQueriesForwardToREST(t *testing.T) {
	srv, seen := fakePlatformREST(t)
	schema := platformSchema(t, srv.URL)
	const policySel = "id tenantId entity action field subject roles condition createdBy effect"
	const cronSel = "id tenantId name enabled cronExpr timezone targetType targetConfig dispatchMode nextRunAt lastRunAt createdAt updatedAt createdBy"

	cases := []struct {
		name, query, wantMethod, wantPath string
		vars                              map[string]interface{}
	}{
		{"adminUsers", "{ adminUsers { userId roles } }", "GET", "/admin/users", nil},
		{"tenantUsers", "{ tenantUsers { id email } }", "GET", "/users", nil},
		{"createAdminUser", "mutation($email: String!, $password: String!, $roles: [String!]) { createAdminUser(email: $email, password: $password, roles: $roles) { userId email roles } }",
			"POST", "/admin/users", map[string]interface{}{"email": "b@x", "password": "pw", "roles": []interface{}{"viewer"}}},
		{"assignUserRole", "mutation($userId: ID!, $role: String!) { assignUserRole(userId: $userId, role: $role) }",
			"POST", "/admin/users/" + userID + "/roles", map[string]interface{}{"userId": userID, "role": "viewer"}},
		{"revokeUserRole", "mutation($userId: ID!, $role: String!) { revokeUserRole(userId: $userId, role: $role) }",
			"DELETE", "/admin/users/" + userID + "/roles/viewer", map[string]interface{}{"userId": userID, "role": "viewer"}},
		{"policies", "query($entity: String) { policies(entity: $entity) { " + policySel + " } }",
			"GET", "/admin/policies", map[string]interface{}{"entity": "shop.customers"}},
		{"createPolicy", "mutation($entity: String!, $action: String!, $roles: [String!], $condition: Json, $field: String, $subject: String, $effect: String) { createPolicy(entity: $entity, action: $action, roles: $roles, condition: $condition, field: $field, subject: $subject, effect: $effect) { " + policySel + " } }",
			"POST", "/admin/policies", map[string]interface{}{"entity": "shop.customers", "action": "read", "roles": []interface{}{"viewer"}, "condition": nil}},
		{"deletePolicy", "mutation($id: ID!) { deletePolicy(id: $id) }", "DELETE", "/admin/policies/" + policyID, map[string]interface{}{"id": policyID}},
		{"syncPolicyMatrix", "mutation($entity: String!, $grants: [MatrixGrantInput!]!) { syncPolicyMatrix(entity: $entity, grants: $grants) { " + policySel + " } }",
			"PUT", "/admin/policies/matrix", map[string]interface{}{"entity": "shop.customers", "grants": []interface{}{
				map[string]interface{}{"role": "viewer", "action": "read"}, map[string]interface{}{"action": "read"}}}},
		{"cronJobs", "{ cronJobs { " + cronSel + " } }", "GET", "/admin/cron-jobs", nil},
		{"cronJobRuns", "query($id: ID!) { cronJobRuns(id: $id) { id tenantId jobId status scheduledFor startedAt finishedAt error responseSummary createdAt } }",
			"GET", "/admin/cron-jobs/" + jobID + "/runs", map[string]interface{}{"id": jobID}},
		{"createCronJob", "mutation($input: CronJobInput!) { createCronJob(input: $input) { " + cronSel + " } }",
			"POST", "/admin/cron-jobs", map[string]interface{}{"input": map[string]interface{}{
				"name": "nightly", "cronExpr": "0 3 * * *", "targetType": "webhook", "targetConfig": map[string]interface{}{"url": "https://x"}, "triggerConfig": nil}}},
		{"updateCronJob", "mutation($id: ID!, $input: CronJobUpdateInput!) { updateCronJob(id: $id, input: $input) { id } }",
			"PATCH", "/admin/cron-jobs/" + jobID, map[string]interface{}{"id": jobID, "input": map[string]interface{}{"enabled": false}}},
		{"deleteCronJob", "mutation($id: ID!) { deleteCronJob(id: $id) }", "DELETE", "/admin/cron-jobs/" + jobID, map[string]interface{}{"id": jobID}},
		{"myPreferences", "{ myPreferences { locale } }", "GET", "/preferences", nil},
		{"setMyPreferences", "mutation($locale: String!) { setMyPreferences(locale: $locale) { locale } }", "PUT", "/preferences", map[string]interface{}{"locale": "vi"}},
		{"tenantConfig", "{ tenantConfig { key value level overridden public } }", "GET", "/admin/config", nil},
		{"setTenantConfig", "mutation($key: String!, $value: Json!) { setTenantConfig(key: $key, value: $value) { key } }",
			"PUT", "/admin/config/auth.sessionTtlSeconds", map[string]interface{}{"key": "auth.sessionTtlSeconds", "value": 7200}},
		{"resetTenantConfig", "mutation($key: String!) { resetTenantConfig(key: $key) { key } }",
			"DELETE", "/admin/config/auth.sessionTtlSeconds", map[string]interface{}{"key": "auth.sessionTtlSeconds"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(*seen)
			result := exec(schema, tc.query, tc.vars)
			require.Empty(t, result.Errors)
			require.Len(t, *seen, before+1)
			got := (*seen)[before]
			assert.Equal(t, tc.wantMethod, got.method)
			assert.Equal(t, tc.wantPath, got.path)
			assert.Equal(t, "Bearer caller-token", got.auth)
		})
	}

	// Spot-check request bodies/queries the REST handlers depend on.
	byName := map[string]seenRequest{}
	for i, tc := range cases {
		byName[tc.name] = (*seen)[i]
	}
	assert.Equal(t, "entity=shop.customers", byName["policies"].query)
	assert.NotContains(t, byName["createPolicy"].body, "condition", "a null optional arg is omitted, not sent as null")
	assert.Equal(t, map[string]interface{}{"role": "viewer"}, byName["assignUserRole"].body)
	grants := byName["syncPolicyMatrix"].body["grants"].([]interface{})
	assert.Equal(t, map[string]interface{}{"role": nil, "action": "read"}, grants[1], "a role-less grant keeps role: null")
	assert.NotContains(t, byName["createCronJob"].body, "triggerConfig")
	assert.Equal(t, map[string]interface{}{"enabled": false}, byName["updateCronJob"].body)
	assert.Equal(t, map[string]interface{}{"value": float64(7200)}, byName["setTenantConfig"].body)
}

func TestPlatformFieldResults(t *testing.T) {
	srv, _ := fakePlatformREST(t)
	schema := platformSchema(t, srv.URL)

	result := exec(schema, "{ adminUsers { userId roles } deleted: cronJob(id: \""+jobID+"\") { id } }", nil)
	require.Empty(t, result.Errors)
	b, _ := json.Marshal(result.Data)
	assert.JSONEq(t, `{"adminUsers":[{"userId":"`+userID+`","roles":["admin"]}],"deleted":null}`, string(b),
		"a 404 on a nullable single-object field resolves to null")

	result = exec(schema, `mutation { deletePolicy(id: "`+policyID+`") }`, nil)
	require.Empty(t, result.Errors)
	assert.Equal(t, map[string]interface{}{"deletePolicy": true}, result.Data)

	// A REST error surfaces as a GraphQL error carrying the same code/status extensions.
	result = exec(schema, "{ platformConfig { key } }", nil)
	require.Len(t, result.Errors, 1)
	assert.Equal(t, "forbidden", result.Errors[0].Extensions["code"])
	assert.EqualValues(t, 403, result.Errors[0].Extensions["status"])
}
