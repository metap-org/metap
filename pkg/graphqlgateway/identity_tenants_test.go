package graphqlgateway

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	grpcmd "google.golang.org/grpc/metadata"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

type fakeTenantAdmin struct {
	pb.UnimplementedTenantAdminServiceServer
	tokens  []string
	created *pb.CreateTenantRequest
	updated *pb.UpdateTenantRequest
}

func (f *fakeTenantAdmin) token(ctx context.Context) {
	md, _ := grpcmd.FromIncomingContext(ctx)
	f.tokens = append(f.tokens, md.Get("authorization")...)
}

func (f *fakeTenantAdmin) ListTenants(ctx context.Context, req *pb.ListTenantsRequest) (*pb.ListTenantsResponse, error) {
	f.token(ctx)
	return &pb.ListTenantsResponse{Total: 4, Tenants: []*pb.Tenant{{Id: "t1", Name: "Acme", Tier: "trial", Strategy: "schema",
		Status: "active", CreatedAt: "2026-11-18T00:00:00Z", MemberCount: 3}}}, nil
}

func (f *fakeTenantAdmin) CreateTenant(ctx context.Context, req *pb.CreateTenantRequest) (*pb.CreateTenantResponse, error) {
	f.token(ctx)
	f.created = req
	return &pb.CreateTenantResponse{Tenant: &pb.Tenant{Id: "t2", Name: req.GetName(), Tier: "trial", Strategy: "schema", Status: "active",
		CreatedAt: "x", Product: req.GetProduct()}, AdminMemberId: "m1", ExpiresAt: "later", EmailSent: false, AcceptUrl: "http://x/accept"}, nil
}

func (f *fakeTenantAdmin) UpdateTenant(ctx context.Context, req *pb.UpdateTenantRequest) (*pb.Tenant, error) {
	f.token(ctx)
	f.updated = req
	return &pb.Tenant{Id: req.GetId(), Name: "n", Tier: "t", Strategy: "schema", Status: "active", CreatedAt: "x"}, nil
}

func (f *fakeTenantAdmin) SuspendTenant(ctx context.Context, _ *pb.TenantIdRequest) (*pb.Tenant, error) {
	f.token(ctx)
	return nil, grpcapi.SendServiceError(ctx, crud.NewServiceErrorMsg(409, "tenant_invalid_state", "no"))
}

func TestTenantFieldsCallGRPCAsTheCaller(t *testing.T) {
	fake := &fakeTenantAdmin{}
	srv := grpc.NewServer()
	pb.RegisterTenantAdminServiceServer(srv, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	client, err := grpcapi.ConnectIdentity(lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	schema, err := BuildSchema(shopRegistry(t), map[string]crud.RecordBackend{"shop.customers": &fakeBackend{}}, WithIdentity(client))
	require.NoError(t, err)
	run := func(q string, vars map[string]interface{}) (string, []map[string]interface{}) {
		r := exec(schema, q, vars)
		data, _ := json.Marshal(r.Data)
		var errs []map[string]interface{}
		for _, e := range r.Errors {
			errs = append(errs, e.Extensions)
		}
		return string(data), errs
	}

	data, errs := run(`{ tenants(query: "ac") { total tenants { id name status memberCount product deletionEffectiveAt } } }`, nil)
	require.Empty(t, errs)
	assert.JSONEq(t, `{"tenants":{"total":4,"tenants":[{"id":"t1","name":"Acme","status":"active","memberCount":3,"product":null,"deletionEffectiveAt":null}]}}`, data)

	data, errs = run(`mutation($in: CreateTenantInput!) { createTenant(input: $in) { adminMemberId emailSent acceptUrl tenant { id name product } } }`,
		map[string]interface{}{"in": map[string]interface{}{"name": "Globex", "adminEmail": "b@g.io", "product": "waap"}})
	require.Empty(t, errs)
	assert.JSONEq(t, `{"createTenant":{"adminMemberId":"m1","emailSent":false,"acceptUrl":"http://x/accept","tenant":{"id":"t2","name":"Globex","product":"waap"}}}`, data)
	assert.Equal(t, "b@g.io", fake.created.GetAdminEmail())

	// Only the arguments actually given are sent: an absent one means "leave alone".
	_, errs = run(`mutation { updateTenant(id: "t1", name: "New") { id } }`, nil)
	require.Empty(t, errs)
	require.NotNil(t, fake.updated.Name)
	assert.Equal(t, "New", *fake.updated.Name)
	assert.Nil(t, fake.updated.Tier)

	_, errs = run(`mutation { suspendTenant(id: "t1") { id } }`, nil)
	require.Len(t, errs, 1)
	assert.Equal(t, "tenant_invalid_state", errs[0]["code"])

	for _, tok := range fake.tokens {
		assert.Equal(t, "Bearer caller-token", tok)
	}
}
