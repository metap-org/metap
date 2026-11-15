// Package standard holds the literals every service of this platform agrees on: wire timestamp
// layouts, HTTP header and media-type names, the bearer scheme and the Postgres schema names.
// Each used to be retyped wherever it was needed, in slightly different forms (a millisecond
// layout here, a nanosecond one there), and a difference only showed up when two services met. A
// new shared literal goes here first. Standard library only, like the rest of pkg/kit, so
// services that are not built on the platform can use it too.
package standard

import "time"

// Timestamp layouts for values that cross a service boundary. All are UTC when written with
// UTCNow/Format below.
const (
	// TimestampMillis is how a record's createdAt/updatedAt and event times appear on the wire.
	TimestampMillis = "2006-01-02T15:04:05.000Z07:00"
	// TimestampNanos keeps every digit a Postgres timestamptz carries (trailing zeros trimmed).
	TimestampNanos = "2006-01-02T15:04:05.999999999Z07:00"
	// TimestampSeconds is plain RFC 3339, for audit stamps and compiled-at markers.
	TimestampSeconds = time.RFC3339
)

// NowRFC3339 is the current UTC time as RFC 3339 (second precision).
func NowRFC3339() string { return time.Now().UTC().Format(TimestampSeconds) }

// FormatMillis formats t in UTC with millisecond precision.
func FormatMillis(t time.Time) string { return t.UTC().Format(TimestampMillis) }

// HTTP header names (canonical form; Go canonicalises on use anyway) and values.
const (
	HeaderContentType   = "Content-Type"
	HeaderAuthorization = "Authorization"
	HeaderTraceparent   = "traceparent"
	HeaderRequestID     = "x-request-id"
	// HeaderLegacyTraceID is the opaque per-call-chain id that predates W3C traceparent.
	HeaderLegacyTraceID = "x-trace-id"
	// HeaderCaller names the calling service on an internal (service-to-service) call.
	HeaderCaller    = "x-metap-caller"
	HeaderCSRFToken = "X-CSRF-Token"

	MediaTypeJSON = "application/json"
	MediaTypeHTML = "text/html; charset=utf-8"

	// BearerPrefix is the Authorization scheme including its separating space.
	BearerPrefix = "Bearer "
)

// Postgres schema names the platform creates and routes by.
const (
	SchemaMetadata = "metadata" // framework tables: users, policies, outbox, audit, ...
	SchemaControl  = "control"  // global control plane: tenants, tenant hostnames
	SchemaPublic   = "public"
)

// Structured-log attribute keys used across services, so a query on one key finds every service's
// lines (slog.Error("...", standard.LogError, err)).
const (
	LogError       = "error"
	LogTenantID    = "tenant_id"
	LogRunID       = "run_id"
	LogJobID       = "job_id"
	LogQueue       = "queue"
	LogRoutingKey  = "routing_key"
	LogAttempt     = "attempt"
	LogTriggerKind = "trigger_kind"
	LogStepIndex   = "step_index"
	LogLatencyMs   = "latency_ms"
	LogUserID      = "user_id"
	LogRequestID   = "request_id"
	LogTraceID     = "trace_id"
	LogSpanID      = "span_id"
	LogEntity      = "entity"
	LogRecordID    = "record_id"
	LogAction      = "action"
	LogCaller      = "caller"
)
