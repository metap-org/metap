package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var (
	permissionResourceActionType = jsonObject("PermissionResourceAction", map[string]graphql.Output{
		"name": nn(graphql.String), "description": nn(graphql.String),
	})
	permissionResourceType = jsonObject("PermissionResource", map[string]graphql.Output{
		"name": nn(graphql.String), "description": nn(graphql.String),
		// The value to put in a grant's `entity` to grant one of this resource's actions.
		"key": nn(graphql.String), "actions": nnList(permissionResourceActionType),
	})
)

// addResourceFields lists the virtual resources (roadmap 115 M6); granting one goes through the
// ordinary role-permission mutations with entity = the resource's key.
func addResourceFields(query graphql.Fields, c *grpcapi.IdentityClient) {
	query["permissionResources"] = &graphql.Field{
		Type: nnList(permissionResourceType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListPermissionResourcesResponse, error) {
				return c.Roles.ListPermissionResources(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetResources()))
			for i, r := range res.GetResources() {
				actions := make([]interface{}, len(r.GetActions()))
				for j, a := range r.GetActions() {
					actions[j] = map[string]interface{}{"name": a.GetName(), "description": a.GetDescription()}
				}
				out[i] = map[string]interface{}{"name": r.GetName(), "description": r.GetDescription(), "key": r.GetKey(), "actions": actions}
			}
			return out, nil
		},
	}
}
