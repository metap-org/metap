// E2E tests for the permanent-REST route groups (routes_attachments.go,
// routes_workflow_events.go, routes_audit_events.go, routes_metrics.go) — a real
// httpserver.BuildRouter server, hit with real HTTP requests against real Postgres. Reuses the
// pool/key/entity/server helpers already defined in http_server_postgres_test.go.
package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/storage"
)

// fakeObjectStore is an in-memory storage.ObjectStore test double — no real S3/SeaweedFS needed
// to exercise the attachments routes end to end.
type fakeObjectStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore { return &fakeObjectStore{data: map[string][]byte{}} }

func (f *fakeObjectStore) scopedKey(tenantID uuid.UUID, key string) string {
	return tenantID.String() + "/" + key
}

func (f *fakeObjectStore) Put(_ context.Context, tenantID uuid.UUID, key string, data []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[f.scopedKey(tenantID, key)] = append([]byte{}, data...)
	return nil
}

func (f *fakeObjectStore) Get(_ context.Context, tenantID uuid.UUID, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.data[f.scopedKey(tenantID, key)]
	return b, ok, nil
}

func (f *fakeObjectStore) Delete(_ context.Context, tenantID uuid.UUID, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, f.scopedKey(tenantID, key))
	return nil
}

func (f *fakeObjectStore) Exists(_ context.Context, tenantID uuid.UUID, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[f.scopedKey(tenantID, key)]
	return ok, nil
}

var _ storage.ObjectStore = (*fakeObjectStore)(nil)

func doMultipartUpload(t *testing.T, url, token, filename, contentType string, data []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", contentType)
	part, err := mw.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAttachmentsUploadListDownloadAndDelete(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		t.Fatal(err)
	}
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
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM attachments WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.orders'")
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", testOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	store := newFakeObjectStore()
	state.ObjectStore = store
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	// create the record attachments will point at
	createRes := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "First"}})
	if createRes.StatusCode != 201 {
		t.Fatalf("got %d", createRes.StatusCode)
	}
	recordID := decodeJSON(t, createRes)["data"].(map[string]any)["id"].(string)

	// upload
	uploadRes := doMultipartUpload(t, base+"/api/test.orders/"+recordID+"/attachments", token, "notes.txt", "text/plain", []byte("hello world"))
	if uploadRes.StatusCode != 201 {
		t.Fatalf("got %d", uploadRes.StatusCode)
	}
	uploaded := decodeJSON(t, uploadRes)["data"].(map[string]any)
	attachmentID := uploaded["id"].(string)
	if uploaded["filename"] != "notes.txt" || uploaded["contentType"] != "text/plain" {
		t.Fatalf("got %v", uploaded)
	}

	// list
	listRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/attachments", token, nil)
	if listRes.StatusCode != 200 {
		t.Fatalf("got %d", listRes.StatusCode)
	}
	listed := decodeJSON(t, listRes)["data"].([]any)
	if len(listed) != 1 {
		t.Fatalf("got %v", listed)
	}

	// download
	downloadReq, _ := http.NewRequest("GET", base+"/api/test.orders/"+recordID+"/attachments/"+attachmentID+"/download", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+token)
	downloadRes, err := http.DefaultClient.Do(downloadReq)
	if err != nil {
		t.Fatal(err)
	}
	defer downloadRes.Body.Close()
	if downloadRes.StatusCode != 200 {
		t.Fatalf("got %d", downloadRes.StatusCode)
	}
	if got := downloadRes.Header.Get("Content-Disposition"); got != `attachment; filename="notes.txt"` {
		t.Fatalf("got %q", got)
	}
	downloadBody, err := io.ReadAll(downloadRes.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloadBody) != "hello world" {
		t.Fatalf("got %q", downloadBody)
	}

	// a mismatched record id in the URL is a clean 404, not the attachment
	otherRecordID := uuid.New().String()
	mismatchRes := doRequest(t, "GET", base+"/api/test.orders/"+otherRecordID+"/attachments/"+attachmentID+"/download", token, nil)
	if mismatchRes.StatusCode != 404 {
		t.Fatalf("got %d", mismatchRes.StatusCode)
	}

	// delete
	deleteRes := doRequest(t, "DELETE", base+"/api/test.orders/"+recordID+"/attachments/"+attachmentID, token, nil)
	if deleteRes.StatusCode != 204 {
		t.Fatalf("got %d", deleteRes.StatusCode)
	}
	afterDeleteRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/attachments", token, nil)
	if len(decodeJSON(t, afterDeleteRes)["data"].([]any)) != 0 {
		t.Fatal("expected no attachments after delete")
	}
}

func TestAttachmentsReturn503WhenObjectStoreIsNotConfigured(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	res := doRequest(t, "GET", base+"/api/test.orders/"+uuid.New().String()+"/attachments/"+uuid.New().String()+"/download", token, nil)
	if res.StatusCode != 503 {
		t.Fatalf("got %d", res.StatusCode)
	}
}

func TestWorkflowEventsAndAuditEventsForARecord(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		t.Fatal(err)
	}
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
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.workflow_events WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.orders'")
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", testOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)

	createRes := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "First"}})
	created := decodeJSON(t, createRes)["data"].(map[string]any)
	recordID := created["id"].(string)
	version := created["version"].(float64)

	// no transitions yet
	emptyEventsRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/workflow-events", token, nil)
	if emptyEventsRes.StatusCode != 200 {
		t.Fatalf("got %d", emptyEventsRes.StatusCode)
	}
	if len(decodeJSON(t, emptyEventsRes)["data"].([]any)) != 0 {
		t.Fatal("expected no workflow events before any transition")
	}

	transitionRes := doRequest(t, "POST", base+"/api/test.orders/"+recordID+"/transitions/activate", token, map[string]any{"version": version})
	if transitionRes.StatusCode != 200 {
		t.Fatalf("got %d", transitionRes.StatusCode)
	}

	eventsRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/workflow-events", token, nil)
	if eventsRes.StatusCode != 200 {
		t.Fatalf("got %d", eventsRes.StatusCode)
	}
	events := decodeJSON(t, eventsRes)["data"].([]any)
	if len(events) != 1 {
		t.Fatalf("got %v", events)
	}
	// deliberately snake_case — see routes_workflow_events.go's own doc comment.
	event := events[0].(map[string]any)
	if event["from_state"] != "draft" || event["to_state"] != "active" || event["record_id"] != recordID {
		t.Fatalf("got %v", event)
	}

	// audit-events: no EntityAuditConfig wired in this test state, so the route still works
	// (permission-checked) and simply reports no history, rather than erroring.
	auditRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/audit-events", token, nil)
	if auditRes.StatusCode != 200 {
		t.Fatalf("got %d", auditRes.StatusCode)
	}
	if len(decodeJSON(t, auditRes)["data"].([]any)) != 0 {
		t.Fatal("expected no audit events with no audit store configured")
	}

	// a nonexistent record is a clean 404 on both routes
	missingID := uuid.New().String()
	if res := doRequest(t, "GET", base+"/api/test.orders/"+missingID+"/workflow-events", token, nil); res.StatusCode != 404 {
		t.Fatalf("got %d", res.StatusCode)
	}
	if res := doRequest(t, "GET", base+"/api/test.orders/"+missingID+"/audit-events", token, nil); res.StatusCode != 404 {
		t.Fatalf("got %d", res.StatusCode)
	}
}

func TestMetricsEndpointExposesPrometheusTextFormat(t *testing.T) {
	pool := httpTestPool(t)
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)

	// one request before scraping so http_requests_total has something to report
	if _, err := http.Get(base + "/health"); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("got %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"http_requests_total", "process_resident_memory_bytes", "go_goroutines"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in /metrics output", want)
		}
	}
}
