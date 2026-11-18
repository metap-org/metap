// Tenant administration (roadmap 113 P3) over the token issuer's TenantAdminService gRPC. Platform
// admins only; the service decides, these resolvers translate.
package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var (
	tenantType = jsonObject("Tenant", map[string]graphql.Output{
		"id": nn(graphql.ID), "name": nn(graphql.String), "tier": nn(graphql.String), "strategy": nn(graphql.String),
		"status": nn(graphql.String), "product": graphql.String, "createdAt": nn(graphql.String),
		"trialExpiresAt": graphql.String, "deletionRequestedAt": graphql.String, "deletionEffectiveAt": graphql.String,
		"memberCount": nn(graphql.Int),
	})
	tenantPageType = jsonObject("TenantPage", map[string]graphql.Output{
		"tenants": nnList(tenantType), "total": nn(graphql.Int),
	})
	createTenantResultType = jsonObject("CreateTenantResult", map[string]graphql.Output{
		"tenant": nn(tenantType), "adminMemberId": nn(graphql.ID), "expiresAt": nn(graphql.String),
		"emailSent": nn(graphql.Boolean), "acceptUrl": graphql.String,
	})
	createTenantInput = graphql.NewInputObject(graphql.InputObjectConfig{
		Name: "CreateTenantInput",
		Fields: graphql.InputObjectConfigFieldMap{
			"name":             &graphql.InputObjectFieldConfig{Type: nn(graphql.String)},
			"tier":             &graphql.InputObjectFieldConfig{Type: graphql.String},
			"adminEmail":       &graphql.InputObjectFieldConfig{Type: nn(graphql.String)},
			"adminDisplayName": &graphql.InputObjectFieldConfig{Type: graphql.String},
			"product":          &graphql.InputObjectFieldConfig{Type: graphql.String},
		},
	})
)

func tenantMap(t *pb.Tenant) map[string]interface{} {
	return map[string]interface{}{
		"id": t.GetId(), "name": t.GetName(), "tier": t.GetTier(), "strategy": t.GetStrategy(), "status": t.GetStatus(),
		"product": nullIfEmpty(t.GetProduct()), "createdAt": t.GetCreatedAt(),
		"trialExpiresAt": nullIfEmpty(t.GetTrialExpiresAt()), "deletionRequestedAt": nullIfEmpty(t.GetDeletionRequestedAt()),
		"deletionEffectiveAt": nullIfEmpty(t.GetDeletionEffectiveAt()), "memberCount": int(t.GetMemberCount()),
	}
}

func addTenantFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	asTenant := func(ctx context.Context, call func(ctx context.Context, o ...grpc.CallOption) (*pb.Tenant, error)) (interface{}, error) {
		t, err := identityCall(ctx, call)
		if err != nil {
			return nil, err
		}
		return tenantMap(t), nil
	}
	idOnly := func(rpc func(ctx context.Context, in *pb.TenantIdRequest, o ...grpc.CallOption) (*pb.Tenant, error)) graphql.FieldResolveFn {
		return func(p graphql.ResolveParams) (interface{}, error) {
			return asTenant(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Tenant, error) {
				return rpc(ctx, &pb.TenantIdRequest{Id: argString(p.Args, "id")}, o...)
			})
		}
	}

	query["tenants"] = &graphql.Field{
		Type: nn(tenantPageType),
		Args: graphql.FieldConfigArgument{
			"query": optStrArg(), "status": optStrArg(), "tier": optStrArg(),
			"offset": &graphql.ArgumentConfig{Type: graphql.Int}, "limit": &graphql.ArgumentConfig{Type: graphql.Int},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.ListTenantsRequest{Query: argString(p.Args, "query"), Status: argString(p.Args, "status"),
				Tier: argString(p.Args, "tier"), Offset: int32Arg(p.Args, "offset"), Limit: int32Arg(p.Args, "limit")}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListTenantsResponse, error) {
				return c.Tenants.ListTenants(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetTenants()))
			for i, t := range res.GetTenants() {
				out[i] = tenantMap(t)
			}
			return map[string]interface{}{"tenants": out, "total": int(res.GetTotal())}, nil
		},
	}
	query["tenant"] = &graphql.Field{Type: tenantType, Args: idArgs(), Resolve: idOnly(c.Tenants.GetTenant)}

	mutation["createTenant"] = &graphql.Field{
		Type: nn(createTenantResultType),
		Args: graphql.FieldConfigArgument{"input": &graphql.ArgumentConfig{Type: nn(createTenantInput)}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			in, _ := p.Args["input"].(map[string]interface{})
			req := &pb.CreateTenantRequest{Name: argString(in, "name"), Tier: argString(in, "tier"),
				AdminEmail: argString(in, "adminEmail"), AdminDisplayName: argString(in, "adminDisplayName"),
				Product: argString(in, "product")}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.CreateTenantResponse, error) {
				return c.Tenants.CreateTenant(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"tenant": tenantMap(res.GetTenant()), "adminMemberId": res.GetAdminMemberId(),
				"expiresAt": res.GetExpiresAt(), "emailSent": res.GetEmailSent(), "acceptUrl": nullIfEmpty(res.GetAcceptUrl())}, nil
		},
	}
	mutation["updateTenant"] = &graphql.Field{
		Type: nn(tenantType),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg(), "name": optStrArg(), "tier": optStrArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.UpdateTenantRequest{Id: argString(p.Args, "id")}
			// An absent argument leaves the field alone; only a provided one is sent.
			if v, ok := p.Args["name"].(string); ok {
				req.Name = &v
			}
			if v, ok := p.Args["tier"].(string); ok {
				req.Tier = &v
			}
			return asTenant(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Tenant, error) {
				return c.Tenants.UpdateTenant(ctx, req, o...)
			})
		},
	}
	mutation["suspendTenant"] = &graphql.Field{Type: nn(tenantType), Args: idArgs(), Resolve: idOnly(c.Tenants.SuspendTenant)}
	mutation["resumeTenant"] = &graphql.Field{Type: nn(tenantType), Args: idArgs(), Resolve: idOnly(c.Tenants.ResumeTenant)}
	mutation["cancelTenantDeletion"] = &graphql.Field{Type: nn(tenantType), Args: idArgs(), Resolve: idOnly(c.Tenants.CancelTenantDeletion)}
	mutation["requestTenantDeletion"] = &graphql.Field{
		Type: nn(tenantType),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg(), "retentionDays": &graphql.ArgumentConfig{Type: graphql.Int}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.RequestTenantDeletionRequest{Id: argString(p.Args, "id"), RetentionDays: int32Arg(p.Args, "retentionDays")}
			return asTenant(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Tenant, error) {
				return c.Tenants.RequestTenantDeletion(ctx, req, o...)
			})
		},
	}
}
