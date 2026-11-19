package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var sessionType = jsonObject("Session", map[string]graphql.Output{
	"id": nn(graphql.ID), "current": nn(graphql.Boolean), "tenantId": nn(graphql.ID), "tenantName": nn(graphql.String),
	"createdAt": nn(graphql.String), "lastSeenAt": nn(graphql.String), "expiresAt": nn(graphql.String),
	"ip": nn(graphql.String), "userAgent": nn(graphql.String),
})

// addSessionFields exposes the caller's own login sessions (roadmap 115 M4).
func addSessionFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	query["mySessions"] = &graphql.Field{
		Type: nnList(sessionType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListSessionsResponse, error) {
				return c.Account.ListMySessions(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetSessions()))
			for i, s := range res.GetSessions() {
				out[i] = map[string]interface{}{"id": s.GetId(), "current": s.GetCurrent(), "tenantId": s.GetTenantId(),
					"tenantName": s.GetTenantName(), "createdAt": s.GetCreatedAt(), "lastSeenAt": s.GetLastSeenAt(),
					"expiresAt": s.GetExpiresAt(), "ip": s.GetIp(), "userAgent": s.GetUserAgent()}
			}
			return out, nil
		},
	}
	mutation["revokeSession"] = &graphql.Field{
		Type: nn(graphql.Boolean), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			if _, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.Account.RevokeMySession(ctx, &pb.SessionIdRequest{Id: argString(p.Args, "id")}, o...)
			}); err != nil {
				return nil, err
			}
			return true, nil
		},
	}
	mutation["revokeOtherSessions"] = &graphql.Field{
		Type: nn(graphql.Int),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.RevokedSessions, error) {
				return c.Account.RevokeOtherSessions(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			return int(res.GetCount()), nil
		},
	}
}
