// E2E test: a real httpserver.BuildRouter server, bound to a real socket, hit with real HTTP
// requests over a real network stack — auth included (a real RS256 JWT, minted and verified,
// not stubbed). Recovered from the Rust metap-http crate's git history (tests/http_server.rs,
// the commit before REST entity CRUD was removed) and ported essentially unchanged for the
// lifecycle/rate-limit assertions that still apply to this Go port's reinstated REST surface.
package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/permission"
)

func httpTestPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func genRSAKeyPair(t *testing.T) (privatePEM, publicPEM string, publicKey *rsa.PublicKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
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

func newTestAppState(t *testing.T, pool *pgxpool.Pool, entity metadata.EntityDefinition) (*AppState, string) {
	privPEM, _, pubKey := genRSAKeyPair(t)
	reg := metadata.NewMetadataRegistry()
	if err := reg.Register(entity); err != nil {
		t.Fatal(err)
	}
	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(reg)
	permissions := permission.NewPermissionService(control.NewPostgresPolicyStore(testRouterFor(pool)))
	state := NewAppState(pool, reg, &mdPtr, permissions, privPEM, pubKey, testRouterFor(pool))
	return state, privPEM
}

func startTestServer(t *testing.T, state *AppState, corsOrigins []string) string {
	handler := BuildRouter(state, corsOrigins, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFullHTTPLifecycleOverARealServerAndARealJWT(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.orders'")
		pool.Exec(context.Background(), "DELETE FROM metadata.workflow_events WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", testOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities"); err != nil {
		t.Fatal(err)
	}
	createTableSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
		tenant_id uuid NOT NULL,
		code varchar(120),
		status varchar(80),
		data jsonb DEFAULT '{}'::jsonb NOT NULL,
		version integer DEFAULT 1 NOT NULL,
		deleted boolean DEFAULT false NOT NULL,
		created_at timestamptz DEFAULT now() NOT NULL,
		updated_at timestamptz DEFAULT now() NOT NULL,
		created_by uuid,
		updated_by uuid
	)`, testOrdersTable)
	if _, err := pool.Exec(ctx, createTableSQL); err != nil {
		t.Fatal(err)
	}

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}

	// A real origin list, not empty — exercises the allow_credentials + explicit-origin/header
	// CORS branch.
	base := startTestServer(t, state, []string{"http://localhost:5173"})
	client := http.DefaultClient

	// health is public, no auth needed
	health, err := client.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if health.StatusCode != 200 {
		t.Fatalf("got %d", health.StatusCode)
	}
	if health.Header.Get("x-content-type-options") != "nosniff" {
		t.Fatalf("got %q", health.Header.Get("x-content-type-options"))
	}
	if health.Header.Get("x-frame-options") != "SAMEORIGIN" {
		t.Fatalf("got %q", health.Header.Get("x-frame-options"))
	}
	if health.Header.Get("content-security-policy") == "" {
		t.Fatal("expected a content-security-policy header")
	}
	if health.Header.Get("x-request-id") == "" || health.Header.Get("x-trace-id") == "" {
		t.Fatal("expected request-id/trace-id headers")
	}
	health.Body.Close()

	// an incoming x-trace-id is validated and echoed back rather than replaced
	req, _ := http.NewRequest("GET", base+"/health", nil)
	req.Header.Set("x-trace-id", "caller-supplied-trace-id")
	withTraceID, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := withTraceID.Header.Get("x-trace-id"); got != "caller-supplied-trace-id" {
		t.Fatalf("got %q", got)
	}
	withTraceID.Body.Close()

	// openapi.json is public
	openapi, err := client.Get(base + "/metadata/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if openapi.StatusCode != 200 {
		t.Fatalf("got %d", openapi.StatusCode)
	}
	openapi.Body.Close()

	// records route without a token -> 401, with requestId/traceId injected into the error body
	unauthed := doRequest(t, "GET", base+"/api/test.orders", "", nil)
	if unauthed.StatusCode != 401 {
		t.Fatalf("got %d", unauthed.StatusCode)
	}
	expectedRequestID := unauthed.Header.Get("x-request-id")
	unauthedBody := decodeJSON(t, unauthed)
	errObj := unauthedBody["error"].(map[string]any)
	if errObj["requestId"] != expectedRequestID {
		t.Fatalf("got %v, want %v", errObj["requestId"], expectedRequestID)
	}
	if _, ok := errObj["traceId"].(string); !ok {
		t.Fatal("expected traceId to be a string")
	}

	// create
	createRes := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "First"}})
	if createRes.StatusCode != 201 {
		t.Fatalf("got %d", createRes.StatusCode)
	}
	created := decodeJSON(t, createRes)
	createdData := created["data"].(map[string]any)
	id := createdData["id"].(string)
	if createdData["status"] != "draft" {
		t.Fatalf("got %v", createdData["status"])
	}
	version := createdData["version"].(float64)

	// get
	getRes := doRequest(t, "GET", base+"/api/test.orders/"+id, token, nil)
	if getRes.StatusCode != 200 {
		t.Fatalf("got %d", getRes.StatusCode)
	}
	fetched := decodeJSON(t, getRes)
	fetchedData := fetched["data"].(map[string]any)
	if fetchedData["id"] != id {
		t.Fatalf("got %v", fetchedData["id"])
	}
	caps := fetchedData["capabilities"].(map[string]any)
	transitions := caps["transitions"].([]any)
	if transitions[0].(map[string]any)["action"] != "activate" {
		t.Fatalf("got %v", transitions)
	}

	// transition
	transitionRes := doRequest(t, "POST", base+"/api/test.orders/"+id+"/transitions/activate", token, map[string]any{"version": version})
	if transitionRes.StatusCode != 200 {
		t.Fatalf("got %d", transitionRes.StatusCode)
	}
	transitioned := decodeJSON(t, transitionRes)
	transitionedData := transitioned["data"].(map[string]any)
	if transitionedData["status"] != "active" {
		t.Fatalf("got %v", transitionedData["status"])
	}
	version = transitionedData["version"].(float64)

	// stale-version update -> 409
	conflictRes := doRequest(t, "PATCH", base+"/api/test.orders/"+id, token, map[string]any{"version": 999, "data": map[string]any{"name": "Changed"}})
	if conflictRes.StatusCode != 409 {
		t.Fatalf("got %d", conflictRes.StatusCode)
	}
	conflictBody := decodeJSON(t, conflictRes)
	if conflictBody["error"].(map[string]any)["code"] != "version_conflict" {
		t.Fatalf("got %v", conflictBody["error"])
	}

	// delete
	deleteRes := doRequest(t, "DELETE", base+"/api/test.orders/"+id, token, map[string]any{"version": version})
	if deleteRes.StatusCode != 200 {
		t.Fatalf("got %d", deleteRes.StatusCode)
	}

	// post-delete get -> 404
	afterDelete := doRequest(t, "GET", base+"/api/test.orders/"+id, token, nil)
	if afterDelete.StatusCode != 404 {
		t.Fatalf("got %d", afterDelete.StatusCode)
	}
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
	if rateLimited == nil {
		t.Fatal("expected at least one 429 among 400 concurrent requests against a burst limit")
	}
	if rateLimited.retryAfter == "" {
		t.Fatal("expected a Retry-After header on the rate-limited response")
	}
	errObj, _ := rateLimited.body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "too_many_requests" {
		t.Fatalf("got %v", rateLimited.body)
	}
	if _, ok := errObj["requestId"].(string); !ok {
		t.Fatal("expected requestId")
	}
	if _, ok := errObj["traceId"].(string); !ok {
		t.Fatal("expected traceId")
	}
}

func boolp(b bool) *bool { return &b }
