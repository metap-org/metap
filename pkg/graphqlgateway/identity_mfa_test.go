package graphqlgateway

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

type fakeMFA struct {
	pb.UnimplementedAccountServiceServer
	pb.UnimplementedUserAdminServiceServer
	code, reset string
}

func (f *fakeMFA) GetMfaStatus(context.Context, *pb.Empty) (*pb.MfaStatus, error) {
	return &pb.MfaStatus{Enabled: true, RecoveryCodesLeft: 7}, nil
}
func (f *fakeMFA) BeginMfaEnrollment(context.Context, *pb.Empty) (*pb.MfaEnrollment, error) {
	return &pb.MfaEnrollment{Secret: "SEC", OtpauthUri: "otpauth://totp/x"}, nil
}
func (f *fakeMFA) ConfirmMfaEnrollment(_ context.Context, r *pb.MfaCodeRequest) (*pb.MfaRecoveryCodes, error) {
	f.code = r.GetCode()
	return &pb.MfaRecoveryCodes{Codes: []string{"aaaaa-bbbbb"}}, nil
}
func (f *fakeMFA) DisableMfa(_ context.Context, r *pb.MfaCodeRequest) (*pb.Empty, error) {
	f.code = r.GetCode()
	return &pb.Empty{}, nil
}
func (f *fakeMFA) ResetMfa(_ context.Context, r *pb.UserIdRequest) (*pb.Empty, error) {
	f.reset = r.GetId()
	return &pb.Empty{}, nil
}

func TestMFAFieldsCallGRPC(t *testing.T) {
	fake := &fakeMFA{}
	srv := grpc.NewServer()
	pb.RegisterAccountServiceServer(srv, fake)
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
	assert.JSONEq(t, `{"mfaStatus":{"enabled":true,"recoveryCodesLeft":7}}`, run(`{ mfaStatus { enabled recoveryCodesLeft } }`))
	assert.JSONEq(t, `{"beginMfaEnrollment":{"secret":"SEC","otpauthUri":"otpauth://totp/x"}}`, run(`mutation { beginMfaEnrollment { secret otpauthUri } }`))
	assert.JSONEq(t, `{"confirmMfaEnrollment":["aaaaa-bbbbb"]}`, run(`mutation { confirmMfaEnrollment(code: "123456") }`))
	assert.Equal(t, "123456", fake.code)
	assert.JSONEq(t, `{"disableMfa":true}`, run(`mutation { disableMfa(code: "654321") }`))
	assert.JSONEq(t, `{"resetUserMfa":true}`, run(`mutation { resetUserMfa(id: "u1") }`))
	assert.Equal(t, "u1", fake.reset)
}
