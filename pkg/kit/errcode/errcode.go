// Package errcode holds the stable machine-readable error codes every metap transport returns
// (the "code" in the REST error body, ServiceError.Code, the GraphQL error extensions and the gRPC
// error details). A client switches on these, so each is declared once here and never retyped as a
// string literal. Standard library only, like the rest of pkg/kit, so non-metap services can use it.
package errcode

const (
	ValidationFailed = "validation_failed" // 400: input failed validation, field errors say which
	Unauthorized     = "unauthorized"      // 401: no or invalid credentials
	Forbidden        = "forbidden"         // 403: authenticated but not permitted
	NotFound         = "not_found"         // 404: a route or non-record resource does not exist
	RecordNotFound   = "record_not_found"  // 404: the record does not exist (or is not visible)
	VersionConflict  = "version_conflict"  // 409: optimistic lock, expectedVersion is stale
	UniqueViolation  = "unique_violation"  // 409: a unique field or constraint collided
	RecordReferenced = "record_referenced" // 409: delete blocked, other records still reference it
	PayloadTooLarge  = "payload_too_large" // 413: request body over the limit
	InternalError    = "internal_error"    // 500: unexpected failure, details are in the server log
)
