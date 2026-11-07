//go:build integration

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
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
	"github.com/metap-org/metap/pkg/runtime"
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
	require.True(t, r.IsOk, "expected Ok, got %d %s: %v", r.Status, r.ErrorCode, r.Message)
	return r.Data
}

func TestGRPCBackendFullLifecycleMatchesDirectCrudServiceBehavior(t *testing.T) {
	pool := grpcTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
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
	require.NoError(t, err)

	reg := metadata.NewMetadataRegistry()
	require.NoError(t, reg.Register(grpcBackendTestEntity()))
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
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	backend := NewGrpcBackend(conn, runtime.NewStaticServiceTokenSource(serviceJWT))
	rc := adminRequestContext(tenantID) // ignored by GrpcBackend itself — identity comes from serviceJWT

	// create
	createResult, err := backend.Create(ctx, "test.grpc_backend_orders", rawObject(t, map[string]any{"name": "First"}), rc, nil)
	require.NoError(t, err)
	created := unwrapOk(t, createResult)
	require.NotNil(t, created.Status, "got %v", created.Status)
	require.Equal(t, "draft", *created.Status, "got %v", created.Status)
	id := created.ID
	version := created.Version

	// get — compares against a direct CrudService call for the exact same record
	getResult, err := backend.Get(ctx, "test.grpc_backend_orders", id, rc)
	require.NoError(t, err)
	fetched := unwrapOk(t, getResult)
	require.Equal(t, id, fetched.Record.ID, "got %v", fetched.Record.ID)
	require.True(t, fetched.Capabilities.CanUpdate, "expected can_update")
	directGetResult, err := crudService.Get(ctx, "test.grpc_backend_orders", id, rc)
	require.NoError(t, err)
	directFetched := unwrapOk(t, directGetResult)
	require.Equal(t, string(mustMarshal(t, directFetched.Record.Data)), string(mustMarshal(t, fetched.Record.Data)), "got %v want %v", fetched.Record.Data, directFetched.Record.Data)

	// get_many — no batch RPC on the wire, implemented as N gets; still returns the right shape
	getManyResult, err := backend.GetMany(ctx, "test.grpc_backend_orders", []uuid.UUID{id, uuid.New()}, rc)
	require.NoError(t, err)
	batch := unwrapOk(t, getManyResult)
	require.Len(t, batch, 1, "the nonexistent id must simply be absent, not error the whole batch: got %d", len(batch))
	require.Equal(t, id, batch[0].ID, "got %v", batch[0].ID)

	// transition
	transitionResult, err := backend.Transition(ctx, "test.grpc_backend_orders", id, "activate", version, nil, rc, nil)
	require.NoError(t, err)
	transitioned := unwrapOk(t, transitionResult)
	require.NotNil(t, transitioned.Status, "got %v", transitioned.Status)
	require.Equal(t, "active", *transitioned.Status, "got %v", transitioned.Status)
	version = transitioned.Version

	// update with a stale version surfaces as a 409 ServiceResult::Err, not a Go error
	staleResult, err := backend.Update(ctx, "test.grpc_backend_orders", id, version-1, rawObject(t, map[string]any{"name": "Stale"}), rc, nil)
	require.NoError(t, err)
	require.False(t, staleResult.IsOk, "expected a 409 version-conflict error, got %+v", staleResult)
	require.EqualValues(t, 409, staleResult.Status, "expected a 409 version-conflict error, got %+v", staleResult)

	// update with the correct version succeeds
	updateResult, err := backend.Update(ctx, "test.grpc_backend_orders", id, version, rawObject(t, map[string]any{"name": "Renamed"}), rc, nil)
	require.NoError(t, err)
	updated := unwrapOk(t, updateResult)
	var updatedName string
	_ = json.Unmarshal(updated.Data["name"], &updatedName)
	require.Equal(t, "Renamed", updatedName, "got %v", updated.Data["name"])
	version = updated.Version

	// list finds the record
	listResult, err := backend.List(ctx, "test.grpc_backend_orders", query.ListInput{Limit: 30}, rc)
	require.NoError(t, err)
	listed := unwrapOk(t, listResult)
	found := false
	for _, r := range listed {
		if r.ID == id {
			found = true
		}
	}
	require.True(t, found, "expected the created record in the list result")

	// delete
	deleteResult, err := backend.Delete(ctx, "test.grpc_backend_orders", id, version, rc, nil)
	require.NoError(t, err)
	require.True(t, deleteResult.IsOk, "expected delete to succeed, got %+v", deleteResult)

	afterDelete, err := backend.Get(ctx, "test.grpc_backend_orders", id, rc)
	require.NoError(t, err)
	require.False(t, afterDelete.IsOk, "expected the record to be gone after delete, got %+v", afterDelete)
	require.EqualValues(t, 404, afterDelete.Status, "expected the record to be gone after delete, got %+v", afterDelete)
}

func rawObject(t *testing.T, m map[string]any) crud.JsonObject {
	t.Helper()
	out := make(crud.JsonObject, len(m))
	for k, v := range m {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		out[k] = b
	}
	return out
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func grpcListen(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return lis
}
