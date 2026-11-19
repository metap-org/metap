package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var (
	apiKeyType = jsonObject("ApiKey", map[string]graphql.Output{
		"id": nn(graphql.ID), "userId": nn(graphql.ID), "name": nn(graphql.String), "prefix": nn(graphql.String),
		"expiresAt": graphql.String, "revokedAt": graphql.String, "lastUsedAt": graphql.String, "createdAt": nn(graphql.String),
	})
	createApiKeyResultType = jsonObject("CreateApiKeyResult", map[string]graphql.Output{
		"apiKey": nn(apiKeyType),
		// The secret: shown once, only its hash is stored.
		"token": nn(graphql.String),
	})
)

func apiKeyMap(k *pb.ApiKey) map[string]interface{} {
	return map[string]interface{}{"id": k.GetId(), "userId": k.GetUserId(), "name": k.GetName(), "prefix": k.GetPrefix(),
		"expiresAt": nullIfEmpty(k.GetExpiresAt()), "revokedAt": nullIfEmpty(k.GetRevokedAt()),
		"lastUsedAt": nullIfEmpty(k.GetLastUsedAt()), "createdAt": k.GetCreatedAt()}
}

// addAPIKeyFields exposes service accounts and API keys (roadmap 115 M5); every call is the
// caller's own, so the service enforces who may manage them.
func addAPIKeyFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	query["serviceAccounts"] = &graphql.Field{
		Type: nnList(userType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListUsersResponse, error) {
				return c.Admin.ListServiceAccounts(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetUsers()))
			for i, u := range res.GetUsers() {
				out[i] = userMap(u)
			}
			return out, nil
		},
	}
	query["apiKeys"] = &graphql.Field{
		Type: nnList(apiKeyType), Args: graphql.FieldConfigArgument{"userId": requiredIDArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListApiKeysResponse, error) {
				return c.Admin.ListApiKeys(ctx, &pb.UserIdRequest{Id: argString(p.Args, "userId")}, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetApiKeys()))
			for i, k := range res.GetApiKeys() {
				out[i] = apiKeyMap(k)
			}
			return out, nil
		},
	}
	mutation["createServiceAccount"] = &graphql.Field{
		Type: nn(userType),
		Args: graphql.FieldConfigArgument{"name": &graphql.ArgumentConfig{Type: nn(graphql.String)},
			"roles": &graphql.ArgumentConfig{Type: graphql.NewList(nn(graphql.String))}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			var roles []string
			if raw, ok := p.Args["roles"].([]interface{}); ok {
				for _, r := range raw {
					if s, ok := r.(string); ok {
						roles = append(roles, s)
					}
				}
			}
			u, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.User, error) {
				return c.Admin.CreateServiceAccount(ctx, &pb.CreateServiceAccountRequest{Name: argString(p.Args, "name"), Roles: roles}, o...)
			})
			if err != nil {
				return nil, err
			}
			return userMap(u), nil
		},
	}
	mutation["createApiKey"] = &graphql.Field{
		Type: nn(createApiKeyResultType),
		Args: graphql.FieldConfigArgument{"userId": requiredIDArg(), "name": &graphql.ArgumentConfig{Type: nn(graphql.String)},
			"expiresInDays": &graphql.ArgumentConfig{Type: graphql.Int}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.CreateApiKeyResponse, error) {
				return c.Admin.CreateApiKey(ctx, &pb.CreateApiKeyRequest{UserId: argString(p.Args, "userId"),
					Name: argString(p.Args, "name"), ExpiresInDays: int32Arg(p.Args, "expiresInDays")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"apiKey": apiKeyMap(res.GetApiKey()), "token": res.GetToken()}, nil
		},
	}
	mutation["revokeApiKey"] = &graphql.Field{
		Type: nn(graphql.Boolean), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			if _, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.Admin.RevokeApiKey(ctx, &pb.ApiKeyIdRequest{Id: argString(p.Args, "id")}, o...)
			}); err != nil {
				return nil, err
			}
			return true, nil
		},
	}
}
