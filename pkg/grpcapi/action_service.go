package grpcapi

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/metadata"
)

// ActionService implements pb.ActionServiceServer over a crud.ActionInvoker. It authenticates
// exactly like RecordService and checks the call against the entity's declared actions before the
// handler runs, so only declared (entity, action) pairs are reachable, with the declared scope.
type ActionService struct {
	pb.UnimplementedActionServiceServer
	invoker crud.ActionInvoker
	auth    AuthConfig
}

func NewActionService(invoker crud.ActionInvoker, auth AuthConfig) *ActionService {
	return &ActionService{invoker: invoker, auth: auth}
}

func (s *ActionService) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	def, ok := s.declared(req.GetEntityName(), req.GetAction())
	if !ok {
		return nil, rpcError(ctx, crud.NewServiceErrorMsg(http.StatusNotFound, "action_not_found",
			"No action "+req.GetAction()+" on entity "+req.GetEntityName()+"."))
	}
	call := crud.ActionCall{Entity: req.GetEntityName(), Name: req.GetAction(), Input: crud.JsonObject{}, RC: rc}
	if def.Scope == metadata.ActionScopeRecord {
		id, err := uuid.Parse(req.GetId())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "`id` must be a UUID")
		}
		call.ID = id
	}
	if in := req.GetInput(); in != nil {
		if err := StructToValue(in, &call.Input); err != nil {
			return nil, status.Error(codes.InvalidArgument, "`input` must be a JSON object")
		}
	}
	out, ierr := s.invoker.Invoke(ctx, call)
	if ierr != nil {
		return nil, rpcError(ctx, ierr)
	}
	st, serr := ValueToStruct(out)
	if serr != nil {
		return nil, internalError(serr)
	}
	return &pb.InvokeResponse{Result: st}, nil
}

func (s *ActionService) declared(entity, action string) (metadata.EntityAction, bool) {
	if s.auth.Metadata == nil {
		return metadata.EntityAction{}, false
	}
	def, ok := s.auth.Metadata.Load().GetEntity(entity)
	if !ok {
		return metadata.EntityAction{}, false
	}
	for _, a := range def.Actions {
		if a.Name == action {
			return a, true
		}
	}
	return metadata.EntityAction{}, false
}

// Invoke implements crud.ActionInvoker by calling a remote ActionService, with the same token
// selection as every other outbound RPC (the caller's forwarded token, else the service account).
func (b *GrpcBackend) Invoke(ctx context.Context, call crud.ActionCall) (crud.JsonObject, error) {
	in, err := ValueToStruct(call.Input)
	if err != nil {
		return nil, err
	}
	id := ""
	if call.ID != uuid.Nil {
		id = call.ID.String()
	}
	var trailer grpcmd.MD
	resp, err := pb.NewActionServiceClient(b.conn).Invoke(b.outgoingContext(ctx, call.RC),
		&pb.InvokeRequest{EntityName: call.Entity, Action: call.Name, Id: id, Input: in}, grpc.Trailer(&trailer))
	if err != nil {
		return nil, statusToServiceErr(err, trailer)
	}
	var out crud.JsonObject
	if err := StructToValue(resp.GetResult(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

var _ crud.ActionInvoker = (*GrpcBackend)(nil)
