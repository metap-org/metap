// GrpcBackend implements crud.RecordBackend by calling a remote RecordService over gRPC — the
// client-side counterpart to service.go's server implementation, and the concrete "remote" arm
// of the local-vs-remote dispatch seam crud.RecordBackend exists for. Ported from
// crates/metap-grpc/src/client.rs.
//
// Auth prefers the caller's own forwarded identity, falling back to a self-refreshing service
// account token per upstream — see pickToken's doc comment.
//
// No batch RPC exists on the wire (RecordService's proto has no GetMany) — GetMany is
// implemented as one Get call per id, sequentially, same as the Rust original.
package grpcapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/grpcapi/pb"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/query"
	"github.com/metap-org/metap-go/runtime"
)

// GrpcBackend implements crud.RecordBackend by calling a remote RecordService over gRPC.
type GrpcBackend struct {
	client       pb.RecordServiceClient
	conn         *grpc.ClientConn
	serviceToken *runtime.ServiceTokenSource
}

var _ crud.RecordBackend = (*GrpcBackend)(nil)

// ConnectGrpcBackend dials addr (a plain "host:port" target — this package's own Serve/
// OptionalServe only ever run plaintext gRPC, matching the Rust original's TLS-optional default;
// a deployment that terminates TLS between services passes grpc.WithTransportCredentials
// accordingly by building its own grpc.ClientConn and calling NewGrpcBackend instead).
func ConnectGrpcBackend(addr string, serviceToken *runtime.ServiceTokenSource, dialOpts ...grpc.DialOption) (*GrpcBackend, error) {
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to gRPC upstream at %s: %w", addr, err)
	}
	return NewGrpcBackend(conn, serviceToken), nil
}

// NewGrpcBackend wraps an already-built *grpc.ClientConn — the escape hatch for a caller that
// needs its own dial options (TLS, keepalive, ...) not covered by ConnectGrpcBackend.
func NewGrpcBackend(conn *grpc.ClientConn, serviceToken *runtime.ServiceTokenSource) *GrpcBackend {
	return &GrpcBackend{client: pb.NewRecordServiceClient(conn), conn: conn, serviceToken: serviceToken}
}

func (b *GrpcBackend) Close() error {
	return b.conn.Close()
}

// pickToken picks which bearer token an outbound RPC authenticates with: the caller's own
// forwarded token when rc carries one, else serviceToken's current value. A free function, not
// inlined into outgoingContext, so this precedence is unit-testable without a real connection.
func pickToken(rc *permission.RequestContext, serviceToken *runtime.ServiceTokenSource) string {
	if rc.ForwardedBearerToken != "" {
		return rc.ForwardedBearerToken
	}
	// Audit 04 finding A#5: this fallback used to be silent — a caller with no forwarded
	// identity (a boot-time schema-discovery call, or a real bug upstream that dropped the
	// header) authenticated as the shared service account with no trace of it having happened
	// at all.
	slog.Debug("no forwarded bearer token, falling back to service account token", "tenant_id", rc.TenantID)
	return serviceToken.Current()
}

// outgoingContext attaches the bearer token (via pickToken) and, if ctx is running inside a
// traced call (runtime.CurrentTrace), the W3C traceparent — the one choke point every outbound
// RPC goes through, so a caller running inside an incoming request automatically propagates its
// trace context to whichever upstream microservice this backend calls.
func (b *GrpcBackend) outgoingContext(ctx context.Context, rc *permission.RequestContext) context.Context {
	token := pickToken(rc, b.serviceToken)
	md := grpcmd.Pairs("authorization", "Bearer "+token)
	if tc, ok := runtime.CurrentTrace(ctx); ok {
		md.Append("traceparent", tc.ToTraceparentHeader())
	}
	return grpcmd.NewOutgoingContext(ctx, md)
}

// statusToServiceErr is the reverse of errorToStatus. Lossless when the upstream speaks this
// platform's error envelope (the trailer set by sendServiceError): the original numeric HTTP
// status, the stable error code, and the whole fieldErrors map are restored exactly as
// CrudService produced them. The Code-based reconstruction below is the fallback for a status
// with no details — an older metap build, or an error raised by something that isn't a metap
// service at all (a mesh sidecar, a proxy) — where Code::Internal/Unavailable/anything else maps
// to 502 (Bad Gateway) rather than 500, since that failure genuinely originates upstream.
func statusToServiceErr[T any](err error, trailer grpcmd.MD) crud.ServiceResult[T] {
	st, _ := status.FromError(err)
	if details, ok := errorDetailsFromTrailer(trailer); ok {
		if details.FieldErrors != nil {
			return crud.ResultErrWithMessageAndFieldErrors[T](details.Status, details.Error,
				derefOr(details.Message, details.Error), details.FieldErrors)
		}
		if details.Message != nil {
			return crud.ResultErrWithMessage[T](details.Status, details.Error, *details.Message)
		}
		return crud.ResultErr[T](details.Status, details.Error)
	}
	httpStatus := uint16(502)
	switch st.Code() {
	case codes.InvalidArgument:
		httpStatus = 400
	case codes.Unauthenticated:
		httpStatus = 401
	case codes.PermissionDenied:
		httpStatus = 403
	case codes.NotFound:
		httpStatus = 404
	case codes.Aborted:
		httpStatus = 409
	}
	return crud.ResultErrWithMessage[T](httpStatus, "upstream_error", st.Message())
}

func derefOr(s *string, def string) string {
	if s != nil {
		return *s
	}
	return def
}

func deserializeRecord(s *structpb.Struct) (crud.RecordDto, error) {
	var dto crud.RecordDto
	if err := StructToValue(s, &dto); err != nil {
		return crud.RecordDto{}, fmt.Errorf("upstream record did not match RecordDto shape: %w", err)
	}
	return dto, nil
}

func deserializeCapabilities(s *structpb.Struct) (crud.RecordCapabilities, error) {
	var caps crud.RecordCapabilities
	if err := StructToValue(s, &caps); err != nil {
		return crud.RecordCapabilities{}, fmt.Errorf("upstream capabilities did not match RecordCapabilities shape: %w", err)
	}
	return caps, nil
}

// listInputToQuery mirrors listInputFromQuery's expected shape exactly (filter/sort/cursor/
// limit/listView/jql) — the two functions are inverses of each other.
func listInputToQuery(input query.ListInput) map[string]any {
	filter := map[string]any{}
	for _, kv := range input.Filters {
		filter[kv[0]] = kv[1]
	}
	q := map[string]any{"limit": input.Limit, "filter": filter}
	if input.Sort != nil {
		q["sort"] = *input.Sort
	}
	if input.Cursor != nil {
		q["cursor"] = *input.Cursor
	}
	if input.ListView != nil {
		q["listView"] = *input.ListView
	}
	if input.JQL != nil {
		q["jql"] = *input.JQL
	}
	return q
}

func (b *GrpcBackend) List(ctx context.Context, entity string, input query.ListInput, rc *permission.RequestContext) (crud.ServiceResult[[]crud.RecordDto], error) {
	req := &pb.ListRequest{EntityName: entity, Query: JSONToStruct(listInputToQuery(input))}
	var trailer grpcmd.MD
	resp, err := b.client.List(b.outgoingContext(ctx, rc), req, grpc.Trailer(&trailer))
	if err != nil {
		return statusToServiceErr[[]crud.RecordDto](err, trailer), nil
	}
	records := make([]crud.RecordDto, len(resp.GetRecords()))
	for i, r := range resp.GetRecords() {
		dto, derr := deserializeRecord(r)
		if derr != nil {
			return crud.ServiceResult[[]crud.RecordDto]{}, derr
		}
		records[i] = dto
	}
	var nextCursor *string
	if resp.GetNextCursor() != "" {
		nc := resp.GetNextCursor()
		nextCursor = &nc
	}
	return crud.ResultOkWithPage(records, crud.PageInfo{Limit: input.Limit, NextCursor: nextCursor}), nil
}

func (b *GrpcBackend) Get(ctx context.Context, entity string, id uuid.UUID, rc *permission.RequestContext) (crud.ServiceResult[crud.GetResult], error) {
	req := &pb.GetRequest{EntityName: entity, Id: id.String()}
	var trailer grpcmd.MD
	resp, err := b.client.Get(b.outgoingContext(ctx, rc), req, grpc.Trailer(&trailer))
	if err != nil {
		return statusToServiceErr[crud.GetResult](err, trailer), nil
	}
	if resp.GetRecord() == nil {
		return crud.ServiceResult[crud.GetResult]{}, fmt.Errorf("upstream get response missing record")
	}
	if resp.GetCapabilities() == nil {
		return crud.ServiceResult[crud.GetResult]{}, fmt.Errorf("upstream get response missing capabilities")
	}
	record, err := deserializeRecord(resp.GetRecord())
	if err != nil {
		return crud.ServiceResult[crud.GetResult]{}, err
	}
	capabilities, err := deserializeCapabilities(resp.GetCapabilities())
	if err != nil {
		return crud.ServiceResult[crud.GetResult]{}, err
	}
	return crud.ResultOk(crud.GetResult{Record: record, Capabilities: capabilities}), nil
}

// GetMany: same convention RecordLoader already expects — a denied/missing id is simply absent
// from the batch result rather than failing every other id in it.
func (b *GrpcBackend) GetMany(ctx context.Context, entity string, ids []uuid.UUID, rc *permission.RequestContext) (crud.ServiceResult[[]crud.GetManyResult], error) {
	results := make([]crud.GetManyResult, 0, len(ids))
	for _, id := range ids {
		result, err := b.Get(ctx, entity, id, rc)
		if err != nil {
			return crud.ServiceResult[[]crud.GetManyResult]{}, err
		}
		if result.IsOk {
			results = append(results, crud.GetManyResult{ID: id, Record: result.Data.Record, Capabilities: result.Data.Capabilities})
		}
	}
	return crud.ResultOk(results), nil
}

func (b *GrpcBackend) Create(ctx context.Context, entity string, data crud.JsonObject, rc *permission.RequestContext, reason *string) (crud.ServiceResult[crud.RecordDto], error) {
	req := &pb.CreateRequest{EntityName: entity, Data: JSONToStruct(rawObjectToAny(data)), Reason: reason}
	var trailer grpcmd.MD
	resp, err := b.client.Create(b.outgoingContext(ctx, rc), req, grpc.Trailer(&trailer))
	if err != nil {
		return statusToServiceErr[crud.RecordDto](err, trailer), nil
	}
	if resp.GetRecord() == nil {
		return crud.ServiceResult[crud.RecordDto]{}, fmt.Errorf("upstream create response missing record")
	}
	dto, derr := deserializeRecord(resp.GetRecord())
	if derr != nil {
		return crud.ServiceResult[crud.RecordDto]{}, derr
	}
	return crud.ResultOk(dto), nil
}

func (b *GrpcBackend) Update(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, data crud.JsonObject, rc *permission.RequestContext, reason *string) (crud.ServiceResult[crud.RecordDto], error) {
	req := &pb.UpdateRequest{EntityName: entity, Id: id.String(), ExpectedVersion: expectedVersion, Data: JSONToStruct(rawObjectToAny(data)), Reason: reason}
	var trailer grpcmd.MD
	resp, err := b.client.Update(b.outgoingContext(ctx, rc), req, grpc.Trailer(&trailer))
	if err != nil {
		return statusToServiceErr[crud.RecordDto](err, trailer), nil
	}
	if resp.GetRecord() == nil {
		return crud.ServiceResult[crud.RecordDto]{}, fmt.Errorf("upstream update response missing record")
	}
	dto, derr := deserializeRecord(resp.GetRecord())
	if derr != nil {
		return crud.ServiceResult[crud.RecordDto]{}, derr
	}
	return crud.ResultOk(dto), nil
}

func (b *GrpcBackend) Transition(ctx context.Context, entity string, id uuid.UUID, action string, expectedVersion int32, data crud.JsonObject, rc *permission.RequestContext, reason *string) (crud.ServiceResult[crud.RecordDto], error) {
	req := &pb.TransitionRequest{EntityName: entity, Id: id.String(), Action: action, ExpectedVersion: expectedVersion, Reason: reason}
	if data != nil {
		req.Data = JSONToStruct(rawObjectToAny(data))
	}
	var trailer grpcmd.MD
	resp, err := b.client.Transition(b.outgoingContext(ctx, rc), req, grpc.Trailer(&trailer))
	if err != nil {
		return statusToServiceErr[crud.RecordDto](err, trailer), nil
	}
	if resp.GetRecord() == nil {
		return crud.ServiceResult[crud.RecordDto]{}, fmt.Errorf("upstream transition response missing record")
	}
	dto, derr := deserializeRecord(resp.GetRecord())
	if derr != nil {
		return crud.ServiceResult[crud.RecordDto]{}, derr
	}
	return crud.ResultOk(dto), nil
}

func (b *GrpcBackend) Delete(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, rc *permission.RequestContext, reason *string) (crud.ServiceResult[crud.RecordDto], error) {
	req := &pb.DeleteRequest{EntityName: entity, Id: id.String(), ExpectedVersion: expectedVersion, Reason: reason}
	var trailer grpcmd.MD
	resp, err := b.client.Delete(b.outgoingContext(ctx, rc), req, grpc.Trailer(&trailer))
	if err != nil {
		return statusToServiceErr[crud.RecordDto](err, trailer), nil
	}
	if resp.GetRecord() == nil {
		return crud.ServiceResult[crud.RecordDto]{}, fmt.Errorf("upstream delete response missing record")
	}
	dto, derr := deserializeRecord(resp.GetRecord())
	if derr != nil {
		return crud.ServiceResult[crud.RecordDto]{}, derr
	}
	return crud.ResultOk(dto), nil
}

func (b *GrpcBackend) Aggregate(ctx context.Context, entity string, spec query.AggregateSpec, rc *permission.RequestContext) (crud.ServiceResult[[]json.RawMessage], error) {
	specStruct, err := ValueToStruct(spec)
	if err != nil {
		return crud.ServiceResult[[]json.RawMessage]{}, err
	}
	req := &pb.AggregateRequest{EntityName: entity, Spec: specStruct}
	var trailer grpcmd.MD
	resp, cerr := b.client.Aggregate(b.outgoingContext(ctx, rc), req, grpc.Trailer(&trailer))
	if cerr != nil {
		return statusToServiceErr[[]json.RawMessage](cerr, trailer), nil
	}
	rows := make([]json.RawMessage, len(resp.GetRows()))
	for i, r := range resp.GetRows() {
		b, merr := json.Marshal(StructToJSON(r))
		if merr != nil {
			return crud.ServiceResult[[]json.RawMessage]{}, merr
		}
		rows[i] = b
	}
	return crud.ResultOk(rows), nil
}

// rawObjectToAny converts a crud.JsonObject (map[string]json.RawMessage) into map[string]any via
// a JSON round trip (with UseNumber semantics, through JSONToStruct's own caller ValueToStruct —
// here inlined since the input is already raw JSON bytes per field) so JSONToStruct can build the
// outbound Struct the same way ValueToStruct does for a typed Go value.
func rawObjectToAny(data crud.JsonObject) map[string]any {
	out := make(map[string]any, len(data))
	for k, raw := range data {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			continue
		}
		out[k] = v
	}
	return out
}
