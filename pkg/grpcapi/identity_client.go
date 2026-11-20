package grpcapi

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpcmd "google.golang.org/grpc/metadata"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/kit/standard"
)

// IdentityClient calls UserAdminService, RoleAdminService and AccountService on the token issuer.
type IdentityClient struct {
	Admin   pb.UserAdminServiceClient
	Roles   pb.RoleAdminServiceClient
	Account pb.AccountServiceClient
	Tenants pb.TenantAdminServiceClient
	Audit   pb.AuditAdminServiceClient
	Support pb.SupportAccessServiceClient
	Owner   pb.OwnerServiceClient
	conn    *grpc.ClientConn
}

// ConnectIdentity dials the issuer's gRPC listener (plaintext unless dialOpts say otherwise, like
// ConnectGrpcBackend). grpc.NewClient connects lazily, so the issuer need not be up yet.
func ConnectIdentity(addr string, dialOpts ...grpc.DialOption) (*IdentityClient, error) {
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, dialOpts...)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to identity service at %s: %w", addr, err)
	}
	return &IdentityClient{Admin: pb.NewUserAdminServiceClient(conn), Roles: pb.NewRoleAdminServiceClient(conn),
		Account: pb.NewAccountServiceClient(conn), Tenants: pb.NewTenantAdminServiceClient(conn), Audit: pb.NewAuditAdminServiceClient(conn), Support: pb.NewSupportAccessServiceClient(conn), Owner: pb.NewOwnerServiceClient(conn), conn: conn}, nil
}

func (c *IdentityClient) Close() error { return c.conn.Close() }

// CallContext is ctx carrying bearerToken plus this service's name and the request/trace ids,
// for one call made on the token owner's behalf.
func CallContext(ctx context.Context, bearerToken string) context.Context {
	md := grpcmd.Pairs("authorization", standard.BearerPrefix+bearerToken)
	outgoingIDs(ctx, md)
	return grpcmd.NewOutgoingContext(ctx, md)
}

// CallError turns a failed call's error and trailer (grpc.Trailer) back into the ServiceError the
// server raised.
func CallError(err error, trailer grpcmd.MD) *crud.ServiceError {
	return statusToServiceErr(err, trailer)
}

// SendServiceError reports se the way every handler here does (gRPC status plus the error
// envelope on the trailer), for a gRPC service built outside this package.
func SendServiceError(ctx context.Context, se *crud.ServiceError) error {
	return sendServiceError(ctx, se)
}
