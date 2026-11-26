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

func (x *ListUserAuditResponse) GetEvents() []*AuditEvent {
	if x != nil {
		return x.Events
	}
	return nil
}

func (x *ListUserAuditResponse) GetNextCursor() string {
	if x != nil {
		return x.NextCursor
	}
	return ""
}

func (x *AuditEvent) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *AuditEvent) GetAction() string {
	if x != nil {
		return x.Action
	}
	return ""
}

func (x *AuditEvent) GetActorUserId() string {
	if x != nil {
		return x.ActorUserId
	}
	return ""
}

func (x *AuditEvent) GetOccurredAt() string {
	if x != nil {
		return x.OccurredAt
	}
	return ""
}

func (x *AuditEvent) GetDiff() json.RawMessage {
	if x != nil {
		return x.Diff
	}
	return nil
}

type Empty struct {
}
