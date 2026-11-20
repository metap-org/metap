package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var (
	ownerRecoveryType = jsonObject("OwnerRecovery", map[string]graphql.Output{
		"id": nn(graphql.ID), "newOwnerMemberId": nn(graphql.ID), "requestedByEmail": nn(graphql.String),
		"reason": nn(graphql.String), "effectiveAt": nn(graphql.String), "createdAt": nn(graphql.String),
		"completedAt": nn(graphql.String),
	})
	ownerStatusType = jsonObject("OwnerStatus", map[string]graphql.Output{
		"ownerMemberId": nn(graphql.String), "ownerEmail": nn(graphql.String), "pendingRecovery": ownerRecoveryType,
	})
)

func ownerRecoveryMap(r *pb.OwnerRecovery) map[string]interface{} {
	return map[string]interface{}{"id": r.GetId(), "newOwnerMemberId": r.GetNewOwnerMemberId(), "requestedByEmail": r.GetRequestedByEmail(),
		"reason": r.GetReason(), "effectiveAt": r.GetEffectiveAt(), "createdAt": r.GetCreatedAt(), "completedAt": r.GetCompletedAt()}
}

func ownerStatusMap(s *pb.OwnerStatus) map[string]interface{} {
	out := map[string]interface{}{"ownerMemberId": s.GetOwnerMemberId(), "ownerEmail": s.GetOwnerEmail()}
	if r := s.GetPendingRecovery(); r != nil {
		out["pendingRecovery"] = ownerRecoveryMap(r)
	}
	return out
}

// addOwnerFields exposes the tenant owner and its recovery (roadmap 115 M8).
func addOwnerFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	status := func(call func(ctx context.Context, o ...grpc.CallOption) (*pb.OwnerStatus, error)) graphql.FieldResolveFn {
		return func(p graphql.ResolveParams) (interface{}, error) {
			s, err := identityCall(p.Context, call)
			if err != nil {
				return nil, err
			}
			return ownerStatusMap(s), nil
		}
	}
	query["ownerStatus"] = &graphql.Field{Type: nn(ownerStatusType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return status(func(ctx context.Context, o ...grpc.CallOption) (*pb.OwnerStatus, error) {
				return c.Owner.GetOwnerStatus(ctx, &pb.Empty{}, o...)
			})(p)
		}}
	mutation["transferOwnership"] = &graphql.Field{Type: nn(ownerStatusType), Args: graphql.FieldConfigArgument{"memberId": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return status(func(ctx context.Context, o ...grpc.CallOption) (*pb.OwnerStatus, error) {
				return c.Owner.TransferOwnership(ctx, &pb.TransferOwnershipRequest{MemberId: argString(p.Args, "memberId")}, o...)
			})(p)
		}}
	mutation["cancelOwnerRecovery"] = &graphql.Field{Type: nn(ownerStatusType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return status(func(ctx context.Context, o ...grpc.CallOption) (*pb.OwnerStatus, error) {
				return c.Owner.CancelOwnerRecovery(ctx, &pb.Empty{}, o...)
			})(p)
		}}
	mutation["requestOwnerRecovery"] = &graphql.Field{Type: nn(ownerRecoveryType),
		Args: graphql.FieldConfigArgument{"tenantId": strArg(), "newOwnerMemberId": strArg(), "reason": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			r, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.OwnerRecovery, error) {
				return c.Owner.RequestOwnerRecovery(ctx, &pb.RequestOwnerRecoveryRequest{TenantId: argString(p.Args, "tenantId"),
					NewOwnerMemberId: argString(p.Args, "newOwnerMemberId"), Reason: argString(p.Args, "reason")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return ownerRecoveryMap(r), nil
		}}
	mutation["completeOwnerRecovery"] = &graphql.Field{Type: nn(ownerRecoveryType), Args: graphql.FieldConfigArgument{"tenantId": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			r, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.OwnerRecovery, error) {
				return c.Owner.CompleteOwnerRecovery(ctx, &pb.TenantIdOnlyRequest{TenantId: argString(p.Args, "tenantId")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return ownerRecoveryMap(r), nil
		}}
}
