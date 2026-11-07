//go:build integration

// E2E test: a real gRPC server, bound to a real socket, hit with a real gRPC client over a real
// network stack — auth included (a real RS256 JWT, minted and verified, not stubbed). Ported
// from crates/metap-grpc/tests/grpc_crud_postgres.rs's
// full_grpc_lifecycle_over_a_real_server_and_a_real_jwt. Needs a running Postgres
// (DATABASE_URL, falling back to the dev default) — skips rather than fails when none is
// reachable, same convention as httpserver's own e2e test.
package grpcapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
)

func grpcTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t)
	return pool
}

func grpcGenRSAKeyPair(t *testing.T) (privatePEM string, publicKey *rsa.PublicKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})
	return string(privPEM), &key.PublicKey
}

func grpcTestRouterFor(pool *pgxpool.Pool) *control.Router {
	registry := control.NewPostgresTenantRegistry(pool)
	return control.NewRouter(pool, control.NewRegistryCache(registry), control.EnvStore{})
}

const grpcTestOrdersTable = "entities.test_grpc_orders"

func grpcTestEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.grpc_orders", Label: "Order", TableName: grpcTestOrdersTable,
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

func boolPtr(b bool) *bool { return &b }

func createGrpcTestTable(t *testing.T, pool *pgxpool.Pool, table string) {
	ctx := context.Background()
	_, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS entities")
	require.NoError(t, err)
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
	)`, table)
	_, err = pool.Exec(ctx, createTableSQL)
	require.NoError(t, err)
}

func authedContext(token string) context.Context {
	return grpcmd.NewOutgoingContext(context.Background(), grpcmd.Pairs("authorization", "Bearer "+token))
}

func grpcStatusCode(t *testing.T, err error) codes.Code {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "expected a grpc status error, got %v", err)
	return st.Code()
}

func TestFullGRPCLifecycleOverARealServerAndARealJWT(t *testing.T) {
	pool := grpcTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.grpc_orders'")
		pool.Exec(context.Background(), "DELETE FROM metadata.workflow_events WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", grpcTestOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})
	createGrpcTestTable(t, pool, grpcTestOrdersTable)

	privPEM, pubKey := grpcGenRSAKeyPair(t)
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)

	reg := metadata.NewMetadataRegistry()
	require.NoError(t, reg.Register(grpcTestEntity()))
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

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := NewServer(service)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	time.Sleep(200 * time.Millisecond)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	client := pb.NewRecordServiceClient(conn)

	// unauthenticated call is rejected
	_, err = client.Get(context.Background(), &pb.GetRequest{EntityName: "test.grpc_orders", Id: uuid.New().String()})
	require.Error(t, err, "expected Unauthenticated, got %v", err)
	require.Equal(t, codes.Unauthenticated, grpcStatusCode(t, err), "expected Unauthenticated, got %v", err)

	// create
	createResp, err := client.Create(authedContext(token), &pb.CreateRequest{
		EntityName: "test.grpc_orders",
		Data:       JSONToStruct(map[string]any{"name": "First"}),
	})
	require.NoError(t, err)
	record := StructToJSON(createResp.GetRecord())
	require.Equal(t, "draft", record["status"], "got %v", record["status"])
	id, _ := GetStr(record, "id")
	version, _ := GetI64(record, "version")

	// get
	getResp, err := client.Get(authedContext(token), &pb.GetRequest{EntityName: "test.grpc_orders", Id: id})
	require.NoError(t, err)
	fetched := StructToJSON(getResp.GetRecord())
	require.Equal(t, id, fetched["id"], "got %v", fetched["id"])

	// An OAuth2-scoped token for the same admin user is narrowed by its scope, over gRPC too.
	readOnly, err := peripherals.MintOAuthAccessToken(privPEM, tenantID, userID, 3600, "read:test.grpc_orders", "client-1")
	require.NoError(t, err)
	_, err = client.Get(authedContext(readOnly), &pb.GetRequest{EntityName: "test.grpc_orders", Id: id})
	require.NoError(t, err, "the granted read scope must allow get")
	_, scopedCreateErr := client.Create(authedContext(readOnly), &pb.CreateRequest{
		EntityName: "test.grpc_orders",
		Data:       JSONToStruct(map[string]any{"name": "Nope"}),
	})
	require.Error(t, scopedCreateErr, "an admin user's read-scoped OAuth token must not be able to create, got %v", scopedCreateErr)
	require.Equal(t, codes.PermissionDenied, grpcStatusCode(t, scopedCreateErr), "an admin user's read-scoped OAuth token must not be able to create, got %v", scopedCreateErr)
	otherEntity, err := peripherals.MintOAuthAccessToken(privPEM, tenantID, userID, 3600, "read:some.other_entity", "client-1")
	require.NoError(t, err)
	_, wrongEntityErr := client.Get(authedContext(otherEntity), &pb.GetRequest{EntityName: "test.grpc_orders", Id: id})
	require.Error(t, wrongEntityErr, "got %v", wrongEntityErr)
	require.Equal(t, codes.PermissionDenied, grpcStatusCode(t, wrongEntityErr), "got %v", wrongEntityErr)

	// transition
	transitionResp, err := client.Transition(authedContext(token), &pb.TransitionRequest{
		EntityName: "test.grpc_orders", Id: id, Action: "activate", ExpectedVersion: int32(version),
	})
	require.NoError(t, err)
	transitionedRecord := StructToJSON(transitionResp.GetRecord())
	require.Equal(t, "active", transitionedRecord["status"], "got %v", transitionedRecord["status"])
	version, _ = GetI64(transitionedRecord, "version")

	// update with a stale version is rejected as a version conflict
	_, staleErr := client.Update(authedContext(token), &pb.UpdateRequest{
		EntityName: "test.grpc_orders", Id: id, ExpectedVersion: int32(version) - 1,
		Data: JSONToStruct(map[string]any{"name": "Stale"}),
	})
	require.Error(t, staleErr, "got %v", staleErr)
	require.Equal(t, codes.Aborted, grpcStatusCode(t, staleErr), "got %v", staleErr)

	// update with the correct version succeeds
	updateResp, err := client.Update(authedContext(token), &pb.UpdateRequest{
		EntityName: "test.grpc_orders", Id: id, ExpectedVersion: int32(version),
		Data: JSONToStruct(map[string]any{"name": "Renamed"}),
	})
	require.NoError(t, err)
	updatedRecord := StructToJSON(updateResp.GetRecord())
	updatedData, _ := updatedRecord["data"].(map[string]any)
	require.Equal(t, "Renamed", updatedData["name"], "got %v", updatedRecord["data"])
	version, _ = GetI64(updatedRecord, "version")

	// list finds the record
	listResp, err := client.List(authedContext(token), &pb.ListRequest{EntityName: "test.grpc_orders"})
	require.NoError(t, err)
	found := false
	for _, r := range listResp.GetRecords() {
		if StructToJSON(r)["id"] == id {
			found = true
		}
	}
	require.True(t, found, "expected the created record in the list result")

	// delete
	_, err = client.Delete(authedContext(token), &pb.DeleteRequest{EntityName: "test.grpc_orders", Id: id, ExpectedVersion: int32(version)})
	require.NoError(t, err)

	_, afterDeleteErr := client.Get(authedContext(token), &pb.GetRequest{EntityName: "test.grpc_orders", Id: id})
	require.Error(t, afterDeleteErr, "got %v", afterDeleteErr)
	require.Equal(t, codes.NotFound, grpcStatusCode(t, afterDeleteErr), "got %v", afterDeleteErr)
}
