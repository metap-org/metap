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

type fakeKeyAdmin struct {
	pb.UnimplementedUserAdminServiceServer
	tokens  []string
	created *pb.CreateApiKeyRequest
	sa      *pb.CreateServiceAccountRequest
	revoked string
}

func (f *fakeKeyAdmin) token(ctx context.Context) {
	md, _ := grpcmd.FromIncomingContext(ctx)
	f.tokens = append(f.tokens, md.Get("authorization")...)
}

func (f *fakeKeyAdmin) CreateServiceAccount(ctx context.Context, r *pb.CreateServiceAccountRequest) (*pb.User, error) {
	f.token(ctx)
	f.sa = r
	return &pb.User{Id: "sa1", DisplayName: r.GetName(), Status: "active", AuthProvider: "service_account", Roles: r.GetRoles(), CreatedAt: "x"}, nil
}

func (f *fakeKeyAdmin) ListServiceAccounts(ctx context.Context, _ *pb.Empty) (*pb.ListUsersResponse, error) {
	f.token(ctx)
	return &pb.ListUsersResponse{Users: []*pb.User{{Id: "sa1", DisplayName: "ci", Status: "active", AuthProvider: "service_account", CreatedAt: "x"}}}, nil
}

func (f *fakeKeyAdmin) CreateApiKey(ctx context.Context, r *pb.CreateApiKeyRequest) (*pb.CreateApiKeyResponse, error) {
	f.token(ctx)
	f.created = r
	return &pb.CreateApiKeyResponse{Token: "mk_secret", ApiKey: &pb.ApiKey{Id: "k1", UserId: r.GetUserId(), Name: r.GetName(), Prefix: "mk_abcde", CreatedAt: "x"}}, nil
}

func (f *fakeKeyAdmin) ListApiKeys(ctx context.Context, r *pb.UserIdRequest) (*pb.ListApiKeysResponse, error) {
	f.token(ctx)
	return &pb.ListApiKeysResponse{ApiKeys: []*pb.ApiKey{{Id: "k1", UserId: r.GetId(), Name: "n", Prefix: "mk_abcde", CreatedAt: "x", RevokedAt: "y"}}}, nil
}

func (f *fakeKeyAdmin) RevokeApiKey(ctx context.Context, r *pb.ApiKeyIdRequest) (*pb.Empty, error) {
	f.token(ctx)
	f.revoked = r.GetId()
	return &pb.Empty{}, nil
}

func TestAPIKeyFieldsCallGRPCAsTheCaller(t *testing.T) {
	fake := &fakeKeyAdmin{}
	srv := grpc.NewServer()
	pb.RegisterUserAdminServiceServer(srv, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	client, err := grpcapi.ConnectIdentity(lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	schema, err := BuildSchema(shopRegistry(t), map[string]crud.RecordBackend{"shop.customers": &fakeBackend{}}, WithIdentity(client))
	require.NoError(t, err)
	run := func(q string) string {
		r := exec(schema, q, nil)
		require.Empty(t, r.Errors)
		data, _ := json.Marshal(r.Data)
		return string(data)
	}

	assert.JSONEq(t, `{"createServiceAccount":{"id":"sa1","displayName":"ci-bot","roles":["admin"],"authProvider":"service_account"}}`,
		run(`mutation { createServiceAccount(name: "ci-bot", roles: ["admin"]) { id displayName roles authProvider } }`))
	assert.Equal(t, []string{"admin"}, fake.sa.GetRoles())

	assert.JSONEq(t, `{"createApiKey":{"token":"mk_secret","apiKey":{"id":"k1","name":"pipe","expiresAt":null}}}`,
		run(`mutation { createApiKey(userId: "sa1", name: "pipe", expiresInDays: 30) { token apiKey { id name expiresAt } } }`))
	assert.Equal(t, int32(30), fake.created.GetExpiresInDays())

	assert.JSONEq(t, `{"apiKeys":[{"id":"k1","prefix":"mk_abcde","revokedAt":"y","lastUsedAt":null}]}`,
		run(`{ apiKeys(userId: "sa1") { id prefix revokedAt lastUsedAt } }`))
	assert.JSONEq(t, `{"serviceAccounts":[{"id":"sa1","displayName":"ci"}]}`, run(`{ serviceAccounts { id displayName } }`))
	assert.JSONEq(t, `{"revokeApiKey":true}`, run(`mutation { revokeApiKey(id: "k1") }`))
	assert.Equal(t, "k1", fake.revoked)

	for _, tok := range fake.tokens {
		assert.Equal(t, "Bearer caller-token", tok)
	}
}
