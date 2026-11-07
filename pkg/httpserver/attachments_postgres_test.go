//go:build integration

// E2E tests for the permanent-REST route groups (routes_attachments.go,
// routes_workflow_events.go, routes_audit_events.go, routes_metrics.go) — a real
// httpserver.BuildRouter server, hit with real HTTP requests against real Postgres. Reuses the
// pool/key/entity/server helpers already defined in http_server_postgres_test.go.
package httpserver_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/storage"
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
	require.NoError(t, err)
	_, err = part.Write(data)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req, err := http.NewRequest("POST", url, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestAttachmentsUploadListDownloadAndDelete(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities"); err != nil {
		require.FailNow(t, fmt.Sprint(err))
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
		require.FailNow(t, fmt.Sprint(err))
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
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	// create the record attachments will point at
	createRes := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "First"}})
	require.Equal(t, 201, createRes.StatusCode, "got %d", createRes.StatusCode)
	recordID := decodeJSON(t, createRes)["data"].(map[string]any)["id"].(string)

	// upload
	uploadRes := doMultipartUpload(t, base+"/api/test.orders/"+recordID+"/attachments", token, "notes.txt", "text/plain", []byte("hello world"))
	require.Equal(t, 201, uploadRes.StatusCode, "got %d", uploadRes.StatusCode)
	uploaded := decodeJSON(t, uploadRes)["data"].(map[string]any)
	attachmentID := uploaded["id"].(string)
	require.Equal(t, "notes.txt", uploaded["filename"], "got %v", uploaded)
	require.Equal(t, "text/plain", uploaded["contentType"], "got %v", uploaded)

	// list
	listRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/attachments", token, nil)
	require.Equal(t, 200, listRes.StatusCode, "got %d", listRes.StatusCode)
	listed := decodeJSON(t, listRes)["data"].([]any)
	require.Len(t, listed, 1, "got %v", listed)

	// download
	downloadReq, _ := http.NewRequest("GET", base+"/api/test.orders/"+recordID+"/attachments/"+attachmentID+"/download", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+token)
	downloadRes, err := http.DefaultClient.Do(downloadReq)
	require.NoError(t, err)
	defer downloadRes.Body.Close()
	require.Equal(t, 200, downloadRes.StatusCode, "got %d", downloadRes.StatusCode)
	got := downloadRes.Header.Get("Content-Disposition")
	require.Equal(t, `attachment; filename="notes.txt"`, got, "got %q", got)
	downloadBody, err := io.ReadAll(downloadRes.Body)
	require.NoError(t, err)
	require.Equal(t, "hello world", string(downloadBody), "got %q", downloadBody)

	// a mismatched record id in the URL is a clean 404, not the attachment
	otherRecordID := uuid.New().String()
	mismatchRes := doRequest(t, "GET", base+"/api/test.orders/"+otherRecordID+"/attachments/"+attachmentID+"/download", token, nil)
	require.Equal(t, 404, mismatchRes.StatusCode, "got %d", mismatchRes.StatusCode)

	// delete
	deleteRes := doRequest(t, "DELETE", base+"/api/test.orders/"+recordID+"/attachments/"+attachmentID, token, nil)
	require.Equal(t, 204, deleteRes.StatusCode, "got %d", deleteRes.StatusCode)
	afterDeleteRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/attachments", token, nil)
	require.Len(t, decodeJSON(t, afterDeleteRes)["data"].([]any), 0, "expected no attachments after delete")
}

func TestAttachmentsReturn503WhenObjectStoreIsNotConfigured(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	res := doRequest(t, "GET", base+"/api/test.orders/"+uuid.New().String()+"/attachments/"+uuid.New().String()+"/download", token, nil)
	require.Equal(t, 503, res.StatusCode, "got %d", res.StatusCode)
}

func TestWorkflowEventsAndAuditEventsForARecord(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities"); err != nil {
		require.FailNow(t, fmt.Sprint(err))
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
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.workflow_events WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.orders'")
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", testOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	createRes := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "First"}})
	created := decodeJSON(t, createRes)["data"].(map[string]any)
	recordID := created["id"].(string)
	version := created["version"].(float64)

	// no transitions yet
	emptyEventsRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/workflow-events", token, nil)
	require.Equal(t, 200, emptyEventsRes.StatusCode, "got %d", emptyEventsRes.StatusCode)
	require.Len(t, decodeJSON(t, emptyEventsRes)["data"].([]any), 0, "expected no workflow events before any transition")

	transitionRes := doRequest(t, "POST", base+"/api/test.orders/"+recordID+"/transitions/activate", token, map[string]any{"version": version})
	require.Equal(t, 200, transitionRes.StatusCode, "got %d", transitionRes.StatusCode)

	eventsRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/workflow-events", token, nil)
	require.Equal(t, 200, eventsRes.StatusCode, "got %d", eventsRes.StatusCode)
	events := decodeJSON(t, eventsRes)["data"].([]any)
	require.Len(t, events, 1, "got %v", events)
	// deliberately snake_case — see routes_workflow_events.go's own doc comment.
	event := events[0].(map[string]any)
	require.Equal(t, "draft", event["from_state"], "got %v", event)
	require.Equal(t, "active", event["to_state"], "got %v", event)
	require.Equal(t, recordID, event["record_id"], "got %v", event)

	// audit-events: no EntityAuditConfig wired in this test state, so the route still works
	// (permission-checked) and simply reports no history, rather than erroring.
	auditRes := doRequest(t, "GET", base+"/api/test.orders/"+recordID+"/audit-events", token, nil)
	require.Equal(t, 200, auditRes.StatusCode, "got %d", auditRes.StatusCode)
	require.Len(t, decodeJSON(t, auditRes)["data"].([]any), 0, "expected no audit events with no audit store configured")

	// a nonexistent record is a clean 404 on both routes
	missingID := uuid.New().String()
	res := doRequest(t, "GET", base+"/api/test.orders/"+missingID+"/workflow-events", token, nil)
	require.Equal(t, 404, res.StatusCode, "got %d", res.StatusCode)
	res = doRequest(t, "GET", base+"/api/test.orders/"+missingID+"/audit-events", token, nil)
	require.Equal(t, 404, res.StatusCode, "got %d", res.StatusCode)
}

func TestMetricsEndpointExposesPrometheusTextFormat(t *testing.T) {
	pool := httpTestPool(t)
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)

	// one request before scraping so http_requests_total has something to report
	if _, err := http.Get(base + "/health"); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}

	res, err := http.Get(base + "/metrics")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode, "got %d", res.StatusCode)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	text := string(body)
	for _, want := range []string{"http_requests_total", "process_resident_memory_bytes", "go_goroutines"} {
		require.Contains(t, text, want, "expected %q in /metrics output", want)
	}
}
