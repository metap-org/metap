package runtime

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// The {"error":{"code":...,"message":...}} response shape every metap-go service returns on
// failure — ported from metap-runtime::http_error so a from-scratch router can return the same
// shape without depending on a heavier HTTP package.

func defaultMessage(errorCode string) string {
	switch errorCode {
	case "entity_not_found":
		return "Entity not found."
	case "forbidden":
		return "You do not have permission to perform this action."
	case "validation_failed":
		return "Request validation failed."
	case "insert_failed":
		return "Failed to create the record."
	case "record_not_found":
		return "Record not found."
	case "version_conflict":
		return "The record was modified by someone else. Reload and try again."
	case "no_workflow":
		return "This entity has no workflow."
	case "invalid_transition":
		return "This transition is not valid from the record's current state."
	case "guard_failed":
		return "This transition is not allowed."
	case "invalid_cursor":
		return "The pagination cursor is invalid."
	default:
		return "Request failed."
	}
}

// ErrorBody is the decoded shape of a service error response body, exported so middleware (e.g.
// RequestContext) can inject fields into it before it reaches the client.
type ErrorBody struct {
	Status      int                 `json:"-"`
	Code        string              `json:"code"`
	Message     string              `json:"message"`
	FieldErrors map[string][]string `json:"fieldErrors,omitempty"`
	RequestID   string              `json:"requestId,omitempty"`
	TraceID     string              `json:"traceId,omitempty"`
}

func (b ErrorBody) MarshalEnvelope() []byte {
	out, err := json.Marshal(map[string]ErrorBody{"error": b})
	if err != nil {
		// b is always a plain, already-valid-UTF8 struct of strings/maps — this cannot fail in
		// practice; fall back to an empty object rather than panicking a request handler.
		return []byte(`{"error":{"code":"internal_error","message":"Internal server error."}}`)
	}
	return out
}

// BuildServiceError constructs the error body for a known, expected failure. message == "" uses
// the code's default message (mirrors the Rust Option<&str> default-message behavior).
func BuildServiceError(status int, errorCode string, message string, fieldErrors map[string][]string) ErrorBody {
	if message == "" {
		message = defaultMessage(errorCode)
	}
	return ErrorBody{Status: status, Code: errorCode, Message: message, FieldErrors: fieldErrors}
}

// ServiceErrorResponse writes a known, expected failure directly to w.
func ServiceErrorResponse(w http.ResponseWriter, status int, errorCode string, message string, fieldErrors map[string][]string) {
	body := BuildServiceError(status, errorCode, message, fieldErrors)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body.MarshalEnvelope())
}

// InternalErrorResponse logs the real error and writes a body that hides its details from the
// client — same posture as the Rust internal_error_response.
func InternalErrorResponse(w http.ResponseWriter, err error) {
	slog.Error("internal error", "error", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"Internal server error."}}`))
}
