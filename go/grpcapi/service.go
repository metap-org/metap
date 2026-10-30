// RecordServiceServer's handler implementation — generic, metadata-driven CRUD over gRPC, one of
// the two transports entity access goes through now alongside REST. Every RPC follows the same
// three steps: authenticate, convert the request's Struct payload to JSON, call the matching
// crud.RecordBackend method (normally CrudService, or a binary's own decorator around it), then
// convert the result back. No entity-specific code anywhere. Ported from
// crates/metap-grpc/src/service.rs.
package grpcapi

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/grpcapi/pb"
	"github.com/metap-org/metap-go/query"
)

// RecordService implements pb.RecordServiceServer by delegating every RPC to a
// crud.RecordBackend — normally a *crud.CrudService, or a binary's own decorator around one
// (the same local-vs-remote/decoration seam the Rust GrpcRecordService::new documents).
type RecordService struct {
	pb.UnimplementedRecordServiceServer
	backend crud.RecordBackend
	auth    AuthConfig
}

// NewRecordService constructs a RecordService. Takes crud.RecordBackend, not a concrete
// *crud.CrudService, so a caller can hand this a decorator instead of only ever the raw
// service — see this file's own doc comment.
func NewRecordService(backend crud.RecordBackend, auth AuthConfig) *RecordService {
	return &RecordService{backend: backend, auth: auth}
}

func parseRecordID(id string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return uuid.UUID{}, status.Error(codes.InvalidArgument, "`id` must be a UUID")
	}
	return parsed, nil
}

func internalError(err error) error {
	return status.Error(codes.Internal, "internal server error")
}

// recordErr builds the (error, trailer) pair for a non-OK ServiceResult and reports it on resp's
// trailer, then returns the error itself — the one call site every RPC handler below uses.
func sendServiceError[T any](ctx context.Context, r crud.ServiceResult[T]) error {
	err, md := errorToStatus(r.Status, r.ErrorCode, r.Message, r.FieldErrors)
	if md != nil {
		_ = grpc.SetTrailer(ctx, md)
	}
	return err
}

func (s *RecordService) List(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	query_ := StructToJSON(req.GetQuery())
	input, err := listInputFromQuery(query_)
	if err != nil {
		return nil, err
	}

	result, ierr := s.backend.List(ctx, req.GetEntityName(), input, rc)
	if ierr != nil {
		return nil, internalError(ierr)
	}
	if !result.IsOk {
		return nil, sendServiceError(ctx, result)
	}

	records := make([]*structpb.Struct, len(result.Data))
	for i, rec := range result.Data {
		st, serr := ValueToStruct(rec)
		if serr != nil {
			return nil, internalError(serr)
		}
		records[i] = st
	}
	nextCursor := ""
	hasMore := false
	if result.Page != nil && result.Page.NextCursor != nil {
		nextCursor = *result.Page.NextCursor
		hasMore = true
	}
	return &pb.ListResponse{Records: records, NextCursor: nextCursor, HasMore: hasMore}, nil
}

func (s *RecordService) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	id, err := parseRecordID(req.GetId())
	if err != nil {
		return nil, err
	}

	result, ierr := s.backend.Get(ctx, req.GetEntityName(), id, rc)
	if ierr != nil {
		return nil, internalError(ierr)
	}
	if !result.IsOk {
		return nil, sendServiceError(ctx, result)
	}

	recordStruct, serr := ValueToStruct(result.Data.Record)
	if serr != nil {
		return nil, internalError(serr)
	}
	capsStruct, serr := ValueToStruct(result.Data.Capabilities)
	if serr != nil {
		return nil, internalError(serr)
	}
	return &pb.GetResponse{Record: recordStruct, Capabilities: capsStruct}, nil
}

func (s *RecordService) Create(ctx context.Context, req *pb.CreateRequest) (*pb.RecordResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	data, derr := StructToJSONObject(req.GetData())
	if derr != nil {
		return nil, status.Error(codes.InvalidArgument, "`data` must be an object")
	}

	result, ierr := s.backend.Create(ctx, req.GetEntityName(), data, rc, optionalReason(req.Reason))
	if ierr != nil {
		return nil, internalError(ierr)
	}
	if !result.IsOk {
		return nil, sendServiceError(ctx, result)
	}
	recordStruct, serr := ValueToStruct(result.Data)
	if serr != nil {
		return nil, internalError(serr)
	}
	return &pb.RecordResponse{Record: recordStruct}, nil
}

func (s *RecordService) Update(ctx context.Context, req *pb.UpdateRequest) (*pb.RecordResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	id, err := parseRecordID(req.GetId())
	if err != nil {
		return nil, err
	}
	data, derr := StructToJSONObject(req.GetData())
	if derr != nil {
		return nil, status.Error(codes.InvalidArgument, "`data` must be an object")
	}

	result, ierr := s.backend.Update(ctx, req.GetEntityName(), id, req.GetExpectedVersion(), data, rc, optionalReason(req.Reason))
	if ierr != nil {
		return nil, internalError(ierr)
	}
	if !result.IsOk {
		return nil, sendServiceError(ctx, result)
	}
	recordStruct, serr := ValueToStruct(result.Data)
	if serr != nil {
		return nil, internalError(serr)
	}
	return &pb.RecordResponse{Record: recordStruct}, nil
}

func (s *RecordService) Transition(ctx context.Context, req *pb.TransitionRequest) (*pb.RecordResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	id, err := parseRecordID(req.GetId())
	if err != nil {
		return nil, err
	}
	var data crud.JsonObject
	if req.GetData() != nil {
		data, _ = StructToJSONObject(req.GetData())
	}

	result, ierr := s.backend.Transition(ctx, req.GetEntityName(), id, req.GetAction(), req.GetExpectedVersion(), data, rc, optionalReason(req.Reason))
	if ierr != nil {
		return nil, internalError(ierr)
	}
	if !result.IsOk {
		return nil, sendServiceError(ctx, result)
	}
	recordStruct, serr := ValueToStruct(result.Data)
	if serr != nil {
		return nil, internalError(serr)
	}
	return &pb.RecordResponse{Record: recordStruct}, nil
}

func (s *RecordService) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.RecordResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	id, err := parseRecordID(req.GetId())
	if err != nil {
		return nil, err
	}

	result, ierr := s.backend.Delete(ctx, req.GetEntityName(), id, req.GetExpectedVersion(), rc, optionalReason(req.Reason))
	if ierr != nil {
		return nil, internalError(ierr)
	}
	if !result.IsOk {
		return nil, sendServiceError(ctx, result)
	}
	recordStruct, serr := ValueToStruct(result.Data)
	if serr != nil {
		return nil, internalError(serr)
	}
	return &pb.RecordResponse{Record: recordStruct}, nil
}

func (s *RecordService) Aggregate(ctx context.Context, req *pb.AggregateRequest) (*pb.AggregateResponse, error) {
	rc, err := authenticate(ctx, s.auth)
	if err != nil {
		return nil, err
	}
	var spec query.AggregateSpec
	if req.GetSpec() != nil {
		if serr := StructToValue(req.GetSpec(), &spec); serr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid aggregate spec: %v", serr)
		}
	}

	result, ierr := s.backend.Aggregate(ctx, req.GetEntityName(), spec, rc)
	if ierr != nil {
		return nil, internalError(ierr)
	}
	if !result.IsOk {
		return nil, sendServiceError(ctx, result)
	}
	rows := make([]*structpb.Struct, len(result.Data))
	for i, raw := range result.Data {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, internalError(err)
		}
		rows[i] = JSONToStruct(normalizeForStruct(v))
	}
	return &pb.AggregateResponse{Rows: rows}, nil
}

// normalizeForStruct re-decodes a json.RawMessage-sourced value with UseNumber semantics so an
// integer row value (a "count") round-trips through jsonToPbValue's json.Number branch the same
// way ValueToStruct's own callers do, rather than arriving here as a plain float64 already.
func normalizeForStruct(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return v
	}
	return out
}

func optionalReason(reason *string) *string {
	return reason
}
