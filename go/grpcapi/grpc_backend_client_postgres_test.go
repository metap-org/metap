// E2E test for GrpcBackend specifically — a real gRPC server (RecordService, exactly as a real
// binary would run it) hit through the crud.RecordBackend interface via GrpcBackend, not through
// raw pb.RecordServiceClient calls (grpc_crud_postgres_test.go already covers the server side end
// to end). This is the piece a BFF gateway actually depends on: a GrpcBackend behind
// crud.RecordBackend must behave identically to an in-process CrudService for every method.
// Ported from crates/metap-grpc/tests/grpc_backend_client_postgres.rs.
package grpcapi

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/jwks"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/query"
	"github.com/metap-org/metap-go/runtime"
)

const grpcBackendTestOrdersTable = "entities.test_grpc_backend_orders"

func grpcBackendTestEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.grpc_backend_orders", Label: "Order", TableName: grpcBackendTestOrdersTable,
		Fields: []metadata.EntityField{
			{Name: "name", Label: "Name", Kind: metadata.KindString, Required: boolPtr(true), Sortable: boolPtr(true)},
			{Name: "status", Label: "Status", Kind: metadata.KindEnum, EnumValues: []string{"draft", "active"}},
		},
		ListViews: []metadata.EntityListView{{Name: "default", Label: "Default", Fields: []string{"name"}, MaxLimit: 50}},
		Workflow: &metadata.EntityWorkflow{
			StateField: "status", InitialState: "draft",
			Transitions: []metadata.WorkflowTransition{{Action: "activate", From: "draft", To: "active", Label: "Activate"}},
		},
	}
}

func adminRequestContext(tenantID uuid.UUID) *permission.RequestContext {
	return &permission.RequestContext{
		TenantID: tenantID.String(),
		UserID:   uuid.New().String(),
		Roles:    []string{"admin"},
		HasRoles: true,
	}
}

func unwrapOk[T any](t *testing.T, r crud.ServiceResult[T]) T {
	t.Helper()
	if !r.IsOk {
		t.Fatalf("expected Ok, got %d %s: %v", r.Status, r.ErrorCode, r.Message)
	}
	return r.Data
}

func TestGRPCBackendFullLifecycleMatchesDirectCrudServiceBehavior(t *testing.T) {
	pool := grpcTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.grpc_backend_orders'")
		pool.Exec(context.Background(), "DELETE FROM metadata.workflow_events WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM "+grpcBackendTestOrdersTable+" WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})
	createGrpcTestTable(t, pool, grpcBackendTestOrdersTable)

	privPEM, pubKey := grpcGenRSAKeyPair(t)
	// This test exercises GrpcBackend's dispatch, not ServiceTokenSource's login flow — there is
	// no real /auth/login server here, so mint a JWT directly against this test's own throwaway
	// keypair and wrap it in a static ServiceTokenSource.
	serviceJWT, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}

	reg := metadata.NewMetadataRegistry()
	if err := reg.Register(grpcBackendTestEntity()); err != nil {
		t.Fatal(err)
	}
	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(reg)
	permissions := permission.NewPermissionService(control.NewPostgresPolicyStore(grpcTestRouterFor(pool)))
	crudService := crud.NewCrudService(grpcTestRouterFor(pool), &mdPtr, permissions)

	auth := AuthConfig{
		Verifier:               &jwks.TokenVerifier{Kind: jwks.VerifierStatic, PublicKey: pubKey, Leeway: 20 * time.Second},
		Router:                 grpcTestRouterFor(pool),
		Metadata:               &mdPtr,
		ContextAttributesCache: control.NewContextAttributesCache(60 * time.Second),
	}
	service := NewRecordService(crudService, auth)
	srv := NewServer(service)
	lis := grpcListen(t)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	time.Sleep(200 * time.Millisecond)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	backend := NewGrpcBackend(conn, runtime.NewStaticServiceTokenSource(serviceJWT))
	rc := adminRequestContext(tenantID) // ignored by GrpcBackend itself — identity comes from serviceJWT

	// create
	createResult, err := backend.Create(ctx, "test.grpc_backend_orders", rawObject(t, map[string]any{"name": "First"}), rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	created := unwrapOk(t, createResult)
	if created.Status == nil || *created.Status != "draft" {
		t.Fatalf("got %v", created.Status)
	}
	id := created.ID
	version := created.Version

	// get — compares against a direct CrudService call for the exact same record
	getResult, err := backend.Get(ctx, "test.grpc_backend_orders", id, rc)
	if err != nil {
		t.Fatal(err)
	}
	fetched := unwrapOk(t, getResult)
	if fetched.Record.ID != id {
		t.Fatalf("got %v", fetched.Record.ID)
	}
	if !fetched.Capabilities.CanUpdate {
		t.Fatal("expected can_update")
	}
	directGetResult, err := crudService.Get(ctx, "test.grpc_backend_orders", id, rc)
	if err != nil {
		t.Fatal(err)
	}
	directFetched := unwrapOk(t, directGetResult)
	if string(mustMarshal(t, fetched.Record.Data)) != string(mustMarshal(t, directFetched.Record.Data)) {
		t.Fatalf("got %v want %v", fetched.Record.Data, directFetched.Record.Data)
	}

	// get_many — no batch RPC on the wire, implemented as N gets; still returns the right shape
	getManyResult, err := backend.GetMany(ctx, "test.grpc_backend_orders", []uuid.UUID{id, uuid.New()}, rc)
	if err != nil {
		t.Fatal(err)
	}
	batch := unwrapOk(t, getManyResult)
	if len(batch) != 1 {
		t.Fatalf("the nonexistent id must simply be absent, not error the whole batch: got %d", len(batch))
	}
	if batch[0].ID != id {
		t.Fatalf("got %v", batch[0].ID)
	}

	// transition
	transitionResult, err := backend.Transition(ctx, "test.grpc_backend_orders", id, "activate", version, nil, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	transitioned := unwrapOk(t, transitionResult)
	if transitioned.Status == nil || *transitioned.Status != "active" {
		t.Fatalf("got %v", transitioned.Status)
	}
	version = transitioned.Version

	// update with a stale version surfaces as a 409 ServiceResult::Err, not a Go error
	staleResult, err := backend.Update(ctx, "test.grpc_backend_orders", id, version-1, rawObject(t, map[string]any{"name": "Stale"}), rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if staleResult.IsOk || staleResult.Status != 409 {
		t.Fatalf("expected a 409 version-conflict error, got %+v", staleResult)
	}

	// update with the correct version succeeds
	updateResult, err := backend.Update(ctx, "test.grpc_backend_orders", id, version, rawObject(t, map[string]any{"name": "Renamed"}), rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated := unwrapOk(t, updateResult)
	var updatedName string
	_ = json.Unmarshal(updated.Data["name"], &updatedName)
	if updatedName != "Renamed" {
		t.Fatalf("got %v", updated.Data["name"])
	}
	version = updated.Version

	// list finds the record
	listResult, err := backend.List(ctx, "test.grpc_backend_orders", query.ListInput{Limit: 30}, rc)
	if err != nil {
		t.Fatal(err)
	}
	listed := unwrapOk(t, listResult)
	found := false
	for _, r := range listed {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the created record in the list result")
	}

	// delete
	deleteResult, err := backend.Delete(ctx, "test.grpc_backend_orders", id, version, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !deleteResult.IsOk {
		t.Fatalf("expected delete to succeed, got %+v", deleteResult)
	}

	afterDelete, err := backend.Get(ctx, "test.grpc_backend_orders", id, rc)
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.IsOk || afterDelete.Status != 404 {
		t.Fatalf("expected the record to be gone after delete, got %+v", afterDelete)
	}
}

func rawObject(t *testing.T, m map[string]any) crud.JsonObject {
	t.Helper()
	out := make(crud.JsonObject, len(m))
	for k, v := range m {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out[k] = b
	}
	return out
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func grpcListen(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return lis
}
