package crud

import (
	"errors"
	"fmt"
)

// PageInfo is the pagination state List returns alongside its records.
type PageInfo struct {
	Limit      int64   `json:"limit"`
	NextCursor *string `json:"nextCursor,omitempty"`
	// Offset is the number of rows skipped (numbered paging); Total is the count of rows matching
	// the filters, set only when the caller asked for it and did not page by cursor.
	Offset int64  `json:"offset,omitempty"`
	Total  *int64 `json:"total,omitempty"`
}

// EntityCapabilities: entity-level permission, the same checks the write path makes first.
type EntityCapabilities struct {
	CanCreate bool `json:"canCreate"`
	CanUpdate bool `json:"canUpdate"`
	CanDelete bool `json:"canDelete"`
}

// ListResult is what List returns: one page of records and where the next one starts.
type ListResult struct {
	// Capabilities is what this caller may do with the entity as a whole (entity-level policy
	// only; per-record answers stay on Get), so a UI can hide "New"/"Delete" before any row exists.
	Capabilities EntityCapabilities
	Data         []RecordDto
	Page         PageInfo
}

// ServiceError is a failure the caller can act on: an HTTP status, a stable machine-readable code, an optional human message and optional per-field
// errors. Every CrudService/RecordBackend method returns it as the error of its (T, error)
// result, so a transport maps it once (errors.As) and a plain error — a database or network
// failure — stays a plain error, an internal one.
type ServiceError struct {
	Status      uint16
	Code        string
	Message     string
	FieldErrors map[string][]string
	// Details is structured data about the failure for a client that renders it itself (a failed
	// guard's field, operator, expected and actual values); carried as extensions.details.
	Details map[string]any
}

func (e *ServiceError) Error() string {
	msg := e.Code
	if e.Message != "" {
		msg = e.Code + ": " + e.Message
	}
	if len(e.FieldErrors) > 0 {
		msg = fmt.Sprintf("%s (field errors: %v)", msg, e.FieldErrors)
	}
	return fmt.Sprintf("%d %s", e.Status, msg)
}

// NewServiceError builds a ServiceError with only a status and code.
func NewServiceError(status uint16, code string) *ServiceError {
	return &ServiceError{Status: status, Code: code}
}

// NewServiceErrorMsg adds a human-readable message.
func NewServiceErrorMsg(status uint16, code, message string) *ServiceError {
	return &ServiceError{Status: status, Code: code, Message: message}
}

// NewServiceErrorFields adds per-field errors.
func NewServiceErrorFields(status uint16, code string, fieldErrors map[string][]string) *ServiceError {
	return &ServiceError{Status: status, Code: code, FieldErrors: fieldErrors}
}

// NewServiceErrorMsgFields carries both a message (for a client that just shows it) and
// per-field errors (for one that wants more) — used by delete()'s record_referenced guard.
func NewServiceErrorMsgFields(status uint16, code, message string, fieldErrors map[string][]string) *ServiceError {
	return &ServiceError{Status: status, Code: code, Message: message, FieldErrors: fieldErrors}
}

// AsServiceError reports whether err is (or wraps) a ServiceError.
func AsServiceError(err error) (*ServiceError, bool) {
	var se *ServiceError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
