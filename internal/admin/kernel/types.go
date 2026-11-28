package kernel

import "encoding/json"

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

type AuditEvent struct {
	Id string `json:"id" required:"false"`
	// "create", "update", "delete".
	Action      string `json:"action" required:"false"`
	ActorUserId string `json:"actorUserId" required:"false"`
	OccurredAt  string `json:"occurredAt" required:"false"`
	// JSON object {field: {"before": ..., "after": ...}}.
	Diff json.RawMessage `json:"diff" required:"false"`
}

type ListUserAuditResponse struct {
	Events     []*AuditEvent `json:"events" required:"false"`
	NextCursor string        `json:"nextCursor" required:"false"`
}

type Empty struct {
}
