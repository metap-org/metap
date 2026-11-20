package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var ssoConfigType = jsonObject("SsoConfig", map[string]graphql.Output{
	"configured": nn(graphql.Boolean), "enabled": nn(graphql.Boolean), "issuerUrl": nn(graphql.String),
	"clientId": nn(graphql.String), "hasClientSecret": nn(graphql.Boolean), "scopes": nnList(graphql.String),
	"emailDomains": nnList(graphql.String), "defaultRole": nn(graphql.String), "enforced": nn(graphql.Boolean),
	"postLoginRedirect": nn(graphql.String),
	// Register this redirect URI at the identity provider.
	"redirectUri": nn(graphql.String),
})

func ssoConfigMap(c *pb.SsoConfig) map[string]interface{} {
	scopes, domains := c.GetScopes(), c.GetEmailDomains()
	if scopes == nil {
		scopes = []string{}
	}
	if domains == nil {
		domains = []string{}
	}
	return map[string]interface{}{"configured": c.GetConfigured(), "enabled": c.GetEnabled(), "issuerUrl": c.GetIssuerUrl(),
		"clientId": c.GetClientId(), "hasClientSecret": c.GetHasClientSecret(), "scopes": scopes, "emailDomains": domains,
		"defaultRole": c.GetDefaultRole(), "enforced": c.GetEnforced(), "postLoginRedirect": c.GetPostLoginRedirect(),
		"redirectUri": c.GetRedirectUri()}
}

func stringListArg(args map[string]interface{}, name string) []string {
	var out []string
	if raw, ok := args[name].([]interface{}); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// addSSOFields exposes the tenant's single sign-on settings (roadmap 115 M7). The sign-in itself
// is REST (/auth/sso/*): a browser redirect flow.
func addSSOFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	query["ssoConfig"] = &graphql.Field{Type: nn(ssoConfigType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			cfg, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.SsoConfig, error) {
				return c.SSO.GetSsoConfig(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			return ssoConfigMap(cfg), nil
		}}
	strList := &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))}
	mutation["setSsoConfig"] = &graphql.Field{Type: nn(ssoConfigType),
		Args: graphql.FieldConfigArgument{
			"issuerUrl": strArg(), "clientId": strArg(), "clientSecret": optStrArg(), "scopes": strList, "emailDomains": strList,
			"defaultRole": optStrArg(), "enabled": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Boolean)},
			"enforced": &graphql.ArgumentConfig{Type: graphql.Boolean}, "postLoginRedirect": optStrArg(),
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			enabled, _ := p.Args["enabled"].(bool)
			enforced, _ := p.Args["enforced"].(bool)
			cfg, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.SsoConfig, error) {
				return c.SSO.SetSsoConfig(ctx, &pb.SetSsoConfigRequest{IssuerUrl: argString(p.Args, "issuerUrl"),
					ClientId: argString(p.Args, "clientId"), ClientSecret: argString(p.Args, "clientSecret"),
					Scopes: stringListArg(p.Args, "scopes"), EmailDomains: stringListArg(p.Args, "emailDomains"),
					DefaultRole: argString(p.Args, "defaultRole"), Enabled: enabled, Enforced: enforced,
					PostLoginRedirect: argString(p.Args, "postLoginRedirect")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return ssoConfigMap(cfg), nil
		}}
	mutation["deleteSsoConfig"] = &graphql.Field{Type: nn(graphql.Boolean),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			if _, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.SSO.DeleteSsoConfig(ctx, &pb.Empty{}, o...)
			}); err != nil {
				return nil, err
			}
			return true, nil
		}}
}
