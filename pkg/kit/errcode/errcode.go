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

	EntityNotFound = "entity_not_found" // 404: no such entity in the metadata registry
	ActionNotFound = "action_not_found" // 404: the entity declares no such action
	NotTokenIssuer = "not_token_issuer" // 501: this service only verifies tokens, login goes to the issuer

	InvalidTransition          = "invalid_transition"   // 409/400: the workflow does not allow this action from the current state
	InvalidRequestBody         = "invalid_request_body" // 400: the body is not the JSON this route takes
	InvalidType                = "invalid_type"
	InvalidMultipart           = "invalid_multipart"
	InvalidAggregate           = "invalid_aggregate"
	InvalidCursor              = "invalid_cursor"
	InvalidCredentials         = "invalid_credentials"
	InvalidJQL                 = "invalid_jql"
	InvalidConfigValue         = "invalid_config_value"
	UnsupportedPolicyCondition = "unsupported_policy_condition"
)

// OAuth 2.0 error codes (RFC 6749 section 5.2 and 4.1.2.1): a third-party client library switches
// on these exact strings, so they are protocol values, not ours to rename.
const (
	OAuthInvalidRequest          = "invalid_request"
	OAuthInvalidClient           = "invalid_client"
	OAuthInvalidGrant            = "invalid_grant"
	OAuthInvalidScope            = "invalid_scope"
	OAuthUnauthorizedClient      = "unauthorized_client"
	OAuthUnsupportedGrantType    = "unsupported_grant_type"
	OAuthUnsupportedResponseType = "unsupported_response_type"
)
