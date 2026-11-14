//go:build integration

package grpcapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
)

// ActionService over a real server, called through GrpcBackend.Invoke: declared actions reach their
// handler with the caller's identity, undeclared ones are a 404, and a ServiceError survives the wire.
func TestActionServiceRoundTrip(t *testing.T) {
	pool := grpcTestPool(t)
	tenantID, userID := uuid.New(), uuid.New()

	entity := grpcBackendTestEntity()
	entity.Actions = []metadata.EntityAction{
		{Name: "echo", Scope: metadata.ActionScopeRecord},
		{Name: "sweep", Scope: metadata.ActionScopeEntity},
		{Name: "busy", Scope: metadata.ActionScopeEntity},
	}
	privPEM, pubKey := grpcGenRSAKeyPair(t)
	serviceJWT, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)

	reg := metadata.NewMetadataRegistry()
	require.NoError(t, reg.Register(entity))
	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(reg)

	actions := crud.NewActionRegistry()
	require.NoError(t, actions.Register(entity.Name, "echo", func(_ context.Context, c crud.ActionCall) (crud.JsonObject, error) {
		return crud.ToObject(map[string]any{"id": c.ID.String(), "tenant": c.RC.TenantID, "mode": string(c.Input["mode"])}, nil)
	}))
	require.NoError(t, actions.Register(entity.Name, "sweep", func(_ context.Context, c crud.ActionCall) (crud.JsonObject, error) {
		return crud.ToObject(map[string]any{"hasID": c.ID != uuid.Nil}, nil)
	}))
	require.NoError(t, actions.Register(entity.Name, "busy", func(context.Context, crud.ActionCall) (crud.JsonObject, error) {
		return nil, crud.NewServiceErrorMsg(409, "scan_already_in_progress", "busy")
	}))

	auth := AuthConfig{
		Verifier:               &jwks.TokenVerifier{Kind: jwks.VerifierStatic, PublicKey: pubKey, Leeway: 20 * time.Second},
		Router:                 grpcTestRouterFor(pool),
		Metadata:               &mdPtr,
		ContextAttributesCache: control.NewContextAttributesCache(60 * time.Second),
	}
	srv := NewServer(NewRecordService(nil, auth), NewActionService(actions, auth))
	lis := grpcListen(t)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	backend := NewGrpcBackend(conn, httpx.NewStaticServiceTokenSource(serviceJWT))
	rc := adminRequestContext(tenantID)
	ctx := context.Background()

	id := uuid.New()
	out, err := backend.Invoke(ctx, crud.ActionCall{Entity: entity.Name, Name: "echo", ID: id, RC: rc,
		Input: crud.JsonObject{"mode": []byte(`"fast"`)}})
	require.NoError(t, err)
	assert.JSONEq(t, `"`+id.String()+`"`, string(out["id"]))
	assert.JSONEq(t, `"`+tenantID.String()+`"`, string(out["tenant"]), "handler sees the caller's tenant, from the token")
	assert.JSONEq(t, `"\"fast\""`, string(out["mode"]))

	out, err = backend.Invoke(ctx, crud.ActionCall{Entity: entity.Name, Name: "sweep", RC: rc})
	require.NoError(t, err)
	assert.JSONEq(t, `false`, string(out["hasID"]))

	_, err = backend.Invoke(ctx, crud.ActionCall{Entity: entity.Name, Name: "nope", RC: rc})
	se, ok := crud.AsServiceError(err)
	require.True(t, ok)
	assert.EqualValues(t, 404, se.Status)

	_, err = backend.Invoke(ctx, crud.ActionCall{Entity: entity.Name, Name: "busy", RC: rc})
	se, ok = crud.AsServiceError(err)
	require.True(t, ok)
	assert.EqualValues(t, 409, se.Status)
	assert.Equal(t, "scan_already_in_progress", se.Code)

	_, err = backend.Invoke(ctx, crud.ActionCall{Entity: entity.Name, Name: "echo", ID: uuid.Nil, RC: rc})
	require.Error(t, err, "a record-scoped action needs a real id")
}
