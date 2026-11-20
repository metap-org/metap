package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var (
	supportSessionInfoType = jsonObject("SupportSessionInfo", map[string]graphql.Output{
		"memberId": nn(graphql.ID), "startedByEmail": nn(graphql.String), "startedAt": nn(graphql.String),
	})
	supportGrantType = jsonObject("SupportGrant", map[string]graphql.Output{
		"id": nn(graphql.ID), "reason": nn(graphql.String), "role": nn(graphql.String), "grantedBy": nn(graphql.ID),
		"expiresAt": nn(graphql.String), "revokedAt": nn(graphql.String), "createdAt": nn(graphql.String),
		"active": nn(graphql.Boolean), "sessions": nnList(supportSessionInfoType),
	})
	supportSessionTokenType = jsonObject("SupportSessionToken", map[string]graphql.Output{
		"token": nn(graphql.String), "expiresAt": nn(graphql.String), "tenantId": nn(graphql.ID),
		"role": nn(graphql.String), "grantId": nn(graphql.ID),
	})
)

func supportGrantMap(g *pb.SupportGrant) map[string]interface{} {
	sessions := make([]interface{}, len(g.GetSessions()))
	for i, s := range g.GetSessions() {
		sessions[i] = map[string]interface{}{"memberId": s.GetMemberId(), "startedByEmail": s.GetStartedByEmail(), "startedAt": s.GetStartedAt()}
	}
	return map[string]interface{}{"id": g.GetId(), "reason": g.GetReason(), "role": g.GetRole(), "grantedBy": g.GetGrantedBy(),
		"expiresAt": g.GetExpiresAt(), "revokedAt": g.GetRevokedAt(), "createdAt": g.GetCreatedAt(), "active": g.GetActive(),
		"sessions": sessions}
}

// addSupportFields exposes support access (roadmap 115 M10): the tenant admin grants, lists and
// revokes; a platform admin starts a session under a grant.
func addSupportFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	query["supportGrants"] = &graphql.Field{
		Type: nnList(supportGrantType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListSupportGrantsResponse, error) {
				return c.Support.ListSupportGrants(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetGrants()))
			for i, g := range res.GetGrants() {
				out[i] = supportGrantMap(g)
			}
			return out, nil
		},
	}
	mutation["grantSupportAccess"] = &graphql.Field{
		Type: nn(supportGrantType),
		Args: graphql.FieldConfigArgument{"reason": strArg(), "role": strArg(), "hours": &graphql.ArgumentConfig{Type: graphql.Int}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			g, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.SupportGrant, error) {
				return c.Support.GrantSupportAccess(ctx, &pb.GrantSupportAccessRequest{Reason: argString(p.Args, "reason"),
					Role: argString(p.Args, "role"), Hours: int32Arg(p.Args, "hours")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return supportGrantMap(g), nil
		},
	}
	mutation["revokeSupportGrant"] = &graphql.Field{
		Type: nn(supportGrantType), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			g, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.SupportGrant, error) {
				return c.Support.RevokeSupportGrant(ctx, &pb.SupportGrantIdRequest{Id: argString(p.Args, "id")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return supportGrantMap(g), nil
		},
	}
	mutation["startSupportSession"] = &graphql.Field{
		Type: nn(supportSessionTokenType),
		Args: graphql.FieldConfigArgument{"tenantId": strArg(), "grantId": optStrArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			t, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.SupportSessionToken, error) {
				return c.Support.StartSupportSession(ctx, &pb.StartSupportSessionRequest{TenantId: argString(p.Args, "tenantId"),
					GrantId: argString(p.Args, "grantId")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"token": t.GetToken(), "expiresAt": t.GetExpiresAt(), "tenantId": t.GetTenantId(),
				"role": t.GetRole(), "grantId": t.GetGrantId()}, nil
		},
	}
}
