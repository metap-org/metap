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

// fakeUserAdmin records the bearer token of each call and answers from canned data.
type fakeUserAdmin struct {
	pb.UnimplementedUserAdminServiceServer
	pb.UnimplementedRoleAdminServiceServer
	pb.UnimplementedAccountServiceServer
	tokens []string
	last   *pb.CreateUserRequest
}

func (f *fakeUserAdmin) token(ctx context.Context) {
	md, _ := grpcmd.FromIncomingContext(ctx)
	f.tokens = append(f.tokens, md.Get("authorization")...)
}

func (f *fakeUserAdmin) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	f.token(ctx)
	return &pb.ListUsersResponse{Total: 7, Users: []*pb.User{{Id: "u1", Email: "a@x.io", Status: "active", Roles: []string{"admin"}, CreatedAt: "2026-11-16T00:00:00Z"}}}, nil
}

func (f *fakeUserAdmin) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	f.token(ctx)
	f.last = req
	return &pb.CreateUserResponse{User: &pb.User{Id: "u2", Email: req.GetEmail(), Status: "active", CreatedAt: "x"}, CreatedIdentity: true}, nil
}

func (f *fakeUserAdmin) DisableUser(ctx context.Context, _ *pb.UserIdRequest) (*pb.User, error) {
	f.token(ctx)
	// What the real service does for a self-disable: the error envelope rides on the trailer.
	return nil, grpcapiServiceError(ctx, 409, "self_modification", "You cannot disable yourself.")
}

func (f *fakeUserAdmin) ListUserAudit(ctx context.Context, _ *pb.ListUserAuditRequest) (*pb.ListUserAuditResponse, error) {
	f.token(ctx)
	return &pb.ListUserAuditResponse{Events: []*pb.AuditEvent{{Id: "e1", Action: "update", OccurredAt: "t",
		DiffJson: `{"status":{"before":"active","after":"disabled"}}`}}}, nil
}

func (f *fakeUserAdmin) ListMyTenants(ctx context.Context, _ *pb.Empty) (*pb.ListMyTenantsResponse, error) {
	f.token(ctx)
	return &pb.ListMyTenantsResponse{Tenants: []*pb.MyTenant{{TenantId: "t1", Name: "Acme", Current: true}, {TenantId: "t2"}}}, nil
}

func (f *fakeUserAdmin) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
	f.token(ctx)
	return &pb.ListRolesResponse{Roles: []*pb.Role{
		{Id: "r1", Name: "admin", Builtin: true, UserCount: 2},
		{Id: "r2", Name: "analyst", Label: "Analyst", UserCount: 1, PolicyCount: 3},
	}}, nil
}

func (f *fakeUserAdmin) DeleteRole(ctx context.Context, _ *pb.RoleNameRequest) (*pb.Empty, error) {
	f.token(ctx)
	return nil, grpcapi.SendServiceError(ctx, &crud.ServiceError{Status: 409, Code: "role_in_use",
		Message: "in use", FieldErrors: map[string][]string{"members": {"a@x.io"}}})
}

// grpcapiServiceError raises a ServiceError the way grpcapi's handlers do, through a real server
// so the trailer actually crosses the wire.
func grpcapiServiceError(ctx context.Context, status uint16, code, message string) error {
	return grpcapi.SendServiceError(ctx, crud.NewServiceErrorMsg(status, code, message))
}

func identitySchema(t *testing.T) (*fakeUserAdmin, func(q string, vars map[string]interface{}) *resultJSON) {
	t.Helper()
	fake := &fakeUserAdmin{}
	srv := grpc.NewServer()
	pb.RegisterUserAdminServiceServer(srv, fake)
	pb.RegisterRoleAdminServiceServer(srv, fake)
	pb.RegisterAccountServiceServer(srv, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	client, err := grpcapi.ConnectIdentity(lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })

	schema, err := BuildSchema(shopRegistry(t), map[string]crud.RecordBackend{"shop.customers": &fakeBackend{}}, WithIdentity(client))
	require.NoError(t, err)
	return fake, func(q string, vars map[string]interface{}) *resultJSON {
		r := exec(schema, q, vars)
		data, _ := json.Marshal(r.Data)
		out := &resultJSON{data: string(data)}
		for _, e := range r.Errors {
			out.errors = append(out.errors, e.Extensions)
		}
		return out
	}
}

type resultJSON struct {
	data   string
	errors []map[string]interface{}
}

func TestIdentityFieldsCallGRPCAsTheCaller(t *testing.T) {
	fake, run := identitySchema(t)

	r := run(`{ users(search: "a", limit: 10) { total users { id email roles lastLoginAt sharedIdentity } } }`, nil)
	require.Empty(t, r.errors)
	assert.JSONEq(t, `{"users":{"total":7,"users":[{"id":"u1","email":"a@x.io","roles":["admin"],"lastLoginAt":null,"sharedIdentity":false}]}}`, r.data)

	r = run(`mutation($in: CreateUserInput!) { createUser(input: $in) { createdIdentity user { id email } } }`,
		map[string]interface{}{"in": map[string]interface{}{"email": "b@x.io", "password": "pw-123456", "roles": []interface{}{"analyst"}}})
	require.Empty(t, r.errors)
	assert.JSONEq(t, `{"createUser":{"createdIdentity":true,"user":{"id":"u2","email":"b@x.io"}}}`, r.data)
	assert.Equal(t, []string{"analyst"}, fake.last.GetRoles())

	r = run(`mutation { disableUser(id: "u1") { id } }`, nil)
	require.Len(t, r.errors, 1)
	assert.Equal(t, "self_modification", r.errors[0]["code"], "the service's error code reaches the client")
	assert.EqualValues(t, 409, r.errors[0]["status"])

	r = run(`{ userAudit(id: "u1") { events { action diff } nextCursor } }`, nil)
	require.Empty(t, r.errors)
	assert.JSONEq(t, `{"userAudit":{"events":[{"action":"update","diff":{"status":{"before":"active","after":"disabled"}}}],"nextCursor":null}}`, r.data)

	r = run(`{ myTenants { tenantId name current } }`, nil)
	require.Empty(t, r.errors)
	assert.JSONEq(t, `{"myTenants":[{"tenantId":"t1","name":"Acme","current":true},{"tenantId":"t2","name":null,"current":false}]}`, r.data)

	r = run(`{ roles { name label builtin userCount policyCount } }`, nil)
	require.Empty(t, r.errors)
	assert.JSONEq(t, `{"roles":[{"name":"admin","label":null,"builtin":true,"userCount":2,"policyCount":0},`+
		`{"name":"analyst","label":"Analyst","builtin":false,"userCount":1,"policyCount":3}]}`, r.data)

	r = run(`mutation { deleteRole(name: "analyst") }`, nil)
	require.Len(t, r.errors, 1)
	assert.Equal(t, "role_in_use", r.errors[0]["code"])
	assert.NotNil(t, r.errors[0]["fieldErrors"], "what blocks the delete reaches the client")

	for _, tok := range fake.tokens {
		assert.Equal(t, "Bearer caller-token", tok)
	}
	assert.Len(t, fake.tokens, 7)
}
