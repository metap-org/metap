// Maps a crud.ServiceResult's error onto a grpc.Status and back — the gRPC counterpart to
// metap-http's writeServiceError, so the same CrudService failure reads the same regardless of
// which transport a caller used. Ported from crates/metap-grpc/src/status.rs.
//
// The Code alone is not the contract; ErrorDetails is. A grpc Status carries only a Code plus
// free text — this mapping would otherwise throw away the exact numeric HTTP status (400 and 422
// both collapse into InvalidArgument, 500/503 both into Internal) and, worst, fieldErrors. So the
// full envelope rides in the "grpc-status-details-bin" trailer as raw JSON bytes — the same wire
// convention tonic::Status::with_details uses (a caller that ignores it, or an older/non-metap
// server that never sets it, still sees the same Code+message). JSON rather than a
// google.rpc.Status/protobuf Any deliberately: the envelope is already the exact shape REST's
// error body and ServiceResult use, so one representation covers every transport without a
// second proto to keep in sync, and it stays byte-compatible with any Rust metap-grpc service
// still running during a mixed-language deployment.
package grpcapi

import (
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// statusDetailsTrailer is the trailer metadata key carrying ErrorDetails' JSON bytes. Keys
// ending in "-bin" are base64-encoded on the wire automatically by grpc-go (and by tonic on the
// Rust side), so application code here only ever handles the raw bytes.
const statusDetailsTrailer = "grpc-status-details-bin"

// ErrorDetails is the ServiceResult error payload, carried verbatim in a Status's details.
// Field names are camelCase to match the REST error body.
type ErrorDetails struct {
	Status      uint16              `json:"status"`
	Error       string              `json:"error"`
	Message     *string             `json:"message,omitempty"`
	FieldErrors map[string][]string `json:"fieldErrors,omitempty"`
}

// codeForStatus maps a numeric HTTP status to the semantically closest gRPC code. Lossy by
// nature, which is exactly why ErrorDetails carries the original alongside it.
func codeForStatus(httpStatus uint16) codes.Code {
	switch httpStatus {
	case 400, 422:
		return codes.InvalidArgument
	case 401:
		return codes.Unauthenticated
	case 403:
		return codes.PermissionDenied
	case 404:
		return codes.NotFound
	case 409:
		return codes.Aborted // optimistic-lock version conflicts land here (REST: 409)
	default:
		return codes.Internal
	}
}

// errorDetailsTrailerBytes builds the trailer metadata.MD carrying details' JSON — a handler
// sets it via grpc.SetTrailer(ctx, md) before returning the matching status error.
func errorDetailsTrailerBytes(details ErrorDetails) (metadata.MD, bool) {
	b, err := json.Marshal(details)
	if err != nil {
		// Serializing a struct of plain string/map fields cannot realistically fail; degrade to
		// a detail-free status rather than turning a business error into an internal one.
		return nil, false
	}
	return metadata.Pairs(statusDetailsTrailer, string(b)), true
}

// errorToStatus builds the (grpc error, trailer metadata) pair for a ServiceResult::Err shape.
// The caller must call grpc.SetTrailer(ctx, md) (server side) before returning err, or pass
// grpc.Trailer(&md) as a CallOption and read it back (client side) — see service.go/client.go.
func errorToStatus(httpStatus uint16, errorCode string, message *string, fieldErrors map[string][]string) (error, metadata.MD) {
	code := codeForStatus(httpStatus)
	text := errorCode
	if message != nil {
		text = *message
	}
	details := ErrorDetails{Status: httpStatus, Error: errorCode, Message: message, FieldErrors: fieldErrors}
	md, ok := errorDetailsTrailerBytes(details)
	if !ok {
		return status.Error(code, text), nil
	}
	return status.Error(code, text), md
}

// errorDetailsFromTrailer reads ErrorDetails back off a trailer metadata.MD, if the peer set it.
// Returns (nil, false) for a trailer from anything that doesn't speak this envelope (an older
// metap build, a proxy-generated Unavailable, a mesh sidecar's own error) — callers fall back to
// Code-based reconstruction.
func errorDetailsFromTrailer(md metadata.MD) (*ErrorDetails, bool) {
	values := md.Get(statusDetailsTrailer)
	if len(values) == 0 {
		return nil, false
	}
	var details ErrorDetails
	if err := json.Unmarshal([]byte(values[0]), &details); err != nil {
		return nil, false
	}
	return &details, true
}
