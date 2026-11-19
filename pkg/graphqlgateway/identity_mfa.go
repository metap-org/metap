package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var (
	mfaStatusType = jsonObject("MfaStatus", map[string]graphql.Output{
		"enabled": nn(graphql.Boolean), "recoveryCodesLeft": nn(graphql.Int),
	})
	mfaEnrollmentType = jsonObject("MfaEnrollment", map[string]graphql.Output{
		"secret": nn(graphql.String), "otpauthUri": nn(graphql.String),
	})
)

// addMFAFields exposes two-factor authentication (roadmap 115 M3). The caller's own account is
// always the one acted on; resetUserMfa is the admin recovery path.
func addMFAFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	codeArg := graphql.FieldConfigArgument{"code": &graphql.ArgumentConfig{Type: nn(graphql.String)}}
	codes := func(call func(ctx context.Context, o ...grpc.CallOption) (*pb.MfaRecoveryCodes, error), ctx context.Context) (interface{}, error) {
		res, err := identityCall(ctx, call)
		if err != nil {
			return nil, err
		}
		out := make([]interface{}, len(res.GetCodes()))
		for i, s := range res.GetCodes() {
			out[i] = s
		}
		return out, nil
	}
	query["mfaStatus"] = &graphql.Field{
		Type: nn(mfaStatusType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			s, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.MfaStatus, error) {
				return c.Account.GetMfaStatus(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"enabled": s.GetEnabled(), "recoveryCodesLeft": int(s.GetRecoveryCodesLeft())}, nil
		},
	}
	mutation["beginMfaEnrollment"] = &graphql.Field{
		Type: nn(mfaEnrollmentType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			e, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.MfaEnrollment, error) {
				return c.Account.BeginMfaEnrollment(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"secret": e.GetSecret(), "otpauthUri": e.GetOtpauthUri()}, nil
		},
	}
	// Recovery codes come back once, here; they are never listed again.
	mutation["confirmMfaEnrollment"] = &graphql.Field{
		Type: nnList(graphql.String), Args: codeArg,
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return codes(func(ctx context.Context, o ...grpc.CallOption) (*pb.MfaRecoveryCodes, error) {
				return c.Account.ConfirmMfaEnrollment(ctx, &pb.MfaCodeRequest{Code: argString(p.Args, "code")}, o...)
			}, p.Context)
		},
	}
	mutation["regenerateRecoveryCodes"] = &graphql.Field{
		Type: nnList(graphql.String), Args: codeArg,
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return codes(func(ctx context.Context, o ...grpc.CallOption) (*pb.MfaRecoveryCodes, error) {
				return c.Account.RegenerateRecoveryCodes(ctx, &pb.MfaCodeRequest{Code: argString(p.Args, "code")}, o...)
			}, p.Context)
		},
	}
	mutation["disableMfa"] = &graphql.Field{
		Type: nn(graphql.Boolean), Args: codeArg,
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			if _, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.Account.DisableMfa(ctx, &pb.MfaCodeRequest{Code: argString(p.Args, "code")}, o...)
			}); err != nil {
				return nil, err
			}
			return true, nil
		},
	}
	mutation["resetUserMfa"] = &graphql.Field{
		Type: nn(graphql.Boolean), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			if _, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.Admin.ResetMfa(ctx, &pb.UserIdRequest{Id: argString(p.Args, "id")}, o...)
			}); err != nil {
				return nil, err
			}
			return true, nil
		},
	}
}
