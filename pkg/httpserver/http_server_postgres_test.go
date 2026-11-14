//go:build integration

// E2E test: a real httpserver.BuildRouter server, bound to a real socket, hit with real HTTP
// requests over a real network stack — auth included (a real RS256 JWT, minted and verified, not
// stubbed).
package httpserver_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metap-org/metap/pkg/httpserver"
	"github.com/metap-org/metap/pkg/httpserver/attachmentroutes"
	"github.com/metap-org/metap/pkg/httpserver/cronroutes"
	"github.com/metap-org/metap/pkg/httpserver/dashboardroutes"
	"github.com/metap-org/metap/pkg/httpserver/oauth2routes"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/reconciler"
)

func httpTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t)
	return pool
}

func genRSAKeyPair(t *testing.T) (privatePEM, publicPEM string, publicKey *rsa.PublicKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	return string(privPEM), string(pubPEM), &key.PublicKey
}

func testRouterFor(pool *pgxpool.Pool) *control.Router {
	registry := control.NewPostgresTenantRegistry(pool)
	return control.NewRouter(pool, control.NewRegistryCache(registry), control.EnvStore{})
}

const testOrdersTable = "entities.test_orders"

func httpTestEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.orders", Label: "Order", TableName: testOrdersTable,
		Fields: []metadata.EntityField{
			{Name: "name", Label: "Name", Kind: metadata.KindString, Required: boolp(true), Sortable: boolp(true)},
			{Name: "status", Label: "Status", Kind: metadata.KindEnum, EnumValues: []string{"draft", "active"}},
		},
		ListViews: []metadata.EntityListView{{Name: "default", Label: "Default", Fields: []string{"name"}, MaxLimit: 50}},
		Workflow: &metadata.EntityWorkflow{
			StateField: "status", InitialState: "draft",
			Transitions: []metadata.WorkflowTransition{{Action: "activate", From: "draft", To: "active", Label: "Activate"}},
		},
	}
}

func newTestAppState(t *testing.T, pool *pgxpool.Pool, entity metadata.EntityDefinition) (*httpserver.AppState, string) {
	privPEM, _, pubKey := genRSAKeyPair(t)
	reg := metadata.NewMetadataRegistry()
	require.NoError(t, reg.Register(entity))
	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(reg)
	permissions := permission.NewPermissionService(control.NewPostgresPolicyStore(testRouterFor(pool)))
	state := httpserver.NewAppState(pool, reg, &mdPtr, permissions, privPEM, pubKey, testRouterFor(pool))
	return state, privPEM
}

func startTestServer(t *testing.T, state *httpserver.AppState, corsOrigins []string) string {
	// Every optional route group, as pkg/app's DefaultModules mounts them.
	handler := httpserver.BuildRouter(state, corsOrigins, oauth2routes.Mount, cronroutes.Mount, dashboardroutes.Mount, attachmentroutes.Mount)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String()
}

func doRequest(t *testing.T, method, url, token string, body map[string]any) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var v map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v
}

func TestFullHTTPLifecycleOverARealServerAndARealJWT(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.orders'")
		pool.Exec(context.Background(), "DELETE FROM metadata.workflow_events WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", testOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	ensureTestOrdersTable(t, pool)

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)

	// A real origin list, not empty — exercises the allow_credentials + explicit-origin/header
	// CORS branch.
	base := startTestServer(t, state, []string{"http://localhost:5173"})
	client := http.DefaultClient

	// health is public, no auth needed
	health, err := client.Get(base + "/health")
	require.NoError(t, err)
	require.Equal(t, 200, health.StatusCode, "got %d", health.StatusCode)
	if health.Header.Get("x-content-type-options") != "nosniff" {
		require.FailNow(t, fmt.Sprintf("got %q", health.Header.Get("x-content-type-options")))
	}
	if health.Header.Get("x-frame-options") != "SAMEORIGIN" {
		require.FailNow(t, fmt.Sprintf("got %q", health.Header.Get("x-frame-options")))
	}
	require.NotEqual(t, "", health.Header.Get("content-security-policy"), "expected a content-security-policy header")
	require.NotEqual(t, "", health.Header.Get("x-request-id"), "expected request-id/trace-id headers")
	require.NotEqual(t, "", health.Header.Get("x-trace-id"), "expected request-id/trace-id headers")
	health.Body.Close()

	// an incoming x-trace-id is validated and echoed back rather than replaced
	req, _ := http.NewRequest("GET", base+"/health", nil)
	req.Header.Set("x-trace-id", "caller-supplied-trace-id")
	withTraceID, err := client.Do(req)
	require.NoError(t, err)
	got := withTraceID.Header.Get("x-trace-id")
	require.Equal(t, "caller-supplied-trace-id", got, "got %q", got)
	withTraceID.Body.Close()

	// openapi.json is public
	openapi, err := client.Get(base + "/metadata/openapi.json")
	require.NoError(t, err)
	require.Equal(t, 200, openapi.StatusCode, "got %d", openapi.StatusCode)
	openapi.Body.Close()

	// records route without a token -> 401, with requestId/traceId injected into the error body
	unauthed := doRequest(t, "GET", base+"/api/test.orders", "", nil)
	require.Equal(t, 401, unauthed.StatusCode, "got %d", unauthed.StatusCode)
	expectedRequestID := unauthed.Header.Get("x-request-id")
	unauthedBody := decodeJSON(t, unauthed)
	errObj := unauthedBody["error"].(map[string]any)
	require.Equal(t, expectedRequestID, errObj["requestId"], "got %v, want %v", errObj["requestId"], expectedRequestID)
	_, ok := errObj["traceId"].(string)
	require.True(t, ok, "expected traceId to be a string")

	// create
	createRes := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "First"}})
	require.Equal(t, 201, createRes.StatusCode, "got %d", createRes.StatusCode)
	created := decodeJSON(t, createRes)
	createdData := created["data"].(map[string]any)
	id := createdData["id"].(string)
	require.Equal(t, "draft", createdData["status"], "got %v", createdData["status"])
	version := createdData["version"].(float64)

	// get
	getRes := doRequest(t, "GET", base+"/api/test.orders/"+id, token, nil)
	require.Equal(t, 200, getRes.StatusCode, "got %d", getRes.StatusCode)
	fetched := decodeJSON(t, getRes)
	fetchedData := fetched["data"].(map[string]any)
	require.Equal(t, id, fetchedData["id"], "got %v", fetchedData["id"])
	caps := fetchedData["capabilities"].(map[string]any)
	transitions := caps["transitions"].([]any)
	require.Equal(t, "activate", transitions[0].(map[string]any)["action"], "got %v", transitions)

	// transition
	transitionRes := doRequest(t, "POST", base+"/api/test.orders/"+id+"/transitions/activate", token, map[string]any{"version": version})
	require.Equal(t, 200, transitionRes.StatusCode, "got %d", transitionRes.StatusCode)
	transitioned := decodeJSON(t, transitionRes)
	transitionedData := transitioned["data"].(map[string]any)
	require.Equal(t, "active", transitionedData["status"], "got %v", transitionedData["status"])
	version = transitionedData["version"].(float64)

	// stale-version update -> 409
	conflictRes := doRequest(t, "PATCH", base+"/api/test.orders/"+id, token, map[string]any{"version": 999, "data": map[string]any{"name": "Changed"}})
	require.Equal(t, 409, conflictRes.StatusCode, "got %d", conflictRes.StatusCode)
	conflictBody := decodeJSON(t, conflictRes)
	require.Equal(t, "version_conflict", conflictBody["error"].(map[string]any)["code"], "got %v", conflictBody["error"])

	// delete
	deleteRes := doRequest(t, "DELETE", base+"/api/test.orders/"+id, token, map[string]any{"version": version})
	require.Equal(t, 200, deleteRes.StatusCode, "got %d", deleteRes.StatusCode)

	// post-delete get -> 404
	afterDelete := doRequest(t, "GET", base+"/api/test.orders/"+id, token, nil)
	require.Equal(t, 404, afterDelete.StatusCode, "got %d", afterDelete.StatusCode)
}

func TestRateLimitReturns429OnceTheBurstIsExhausted(t *testing.T) {
	pool := httpTestPool(t)
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)

	client := &http.Client{Timeout: 10 * time.Second}
	type result struct {
		status     int
		retryAfter string
		body       map[string]any
	}
	results := make(chan result, 400)
	for i := 0; i < 400; i++ {
		go func() {
			resp, err := client.Get(base + "/health")
			if err != nil {
				results <- result{status: 0}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				var body map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&body)
				results <- result{status: resp.StatusCode, retryAfter: resp.Header.Get("retry-after"), body: body}
				return
			}
			results <- result{status: resp.StatusCode}
		}()
	}

	var rateLimited *result
	for i := 0; i < 400; i++ {
		r := <-results
		if r.status == http.StatusTooManyRequests {
			rr := r
			rateLimited = &rr
		}
	}
	require.NotNil(t, rateLimited, "expected at least one 429 among 400 concurrent requests against a burst limit")
	require.NotEqual(t, "", rateLimited.retryAfter, "expected a Retry-After header on the rate-limited response")
	errObj, _ := rateLimited.body["error"].(map[string]any)
	require.NotNil(t, errObj, "got %v", rateLimited.body)
	require.Equal(t, "too_many_requests", errObj["code"], "got %v", rateLimited.body)
	_, ok := errObj["requestId"].(string)
	require.True(t, ok, "expected requestId")
	_, ok = errObj["traceId"].(string)
	require.True(t, ok, "expected traceId")
}

func boolp(b bool) *bool { return &b }

// ensureTestOrdersTable creates the dedicated table httpTestEntity() points at, through the
// reconciler like production — every test that serves /api/test.orders needs it in its own
// (testkit-isolated) database.
func ensureTestOrdersTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := reconciler.ReconcileWithScope(context.Background(), pool, uuid.Nil, httpTestEntity(), nil, reconciler.ScopeAllTenants)
	require.NoError(t, err)
}

// A panic in a mounted route must come back as the standard 500 with the request ids, through the
// real BuildRouter middleware chain (not a hand-built copy of it).
func TestAPanickingRouteIsAnEnvelopedInternalErrorThroughBuildRouter(t *testing.T) {
	pool := httpTestPool(t)
	state, _ := newTestAppState(t, pool, httpTestEntity())
	boom := func(r chi.Router, _ *httpserver.AppState) {
		r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	}
	srv := httptest.NewServer(httpserver.BuildRouter(state, nil, boom))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/boom")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 500, res.StatusCode)
	require.NotEmpty(t, res.Header.Get("x-request-id"))
	errObj := decodeJSON(t, res)["error"].(map[string]any)
	require.Equal(t, "internal_error", errObj["code"])
	require.NotEmpty(t, errObj["requestId"])
	require.NotEmpty(t, errObj["traceId"])
}
