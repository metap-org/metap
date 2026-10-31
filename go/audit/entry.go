package audit

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AuditAction is which of CrudService's 4 writes produced an entry.
type AuditAction int

const (
	ActionCreate AuditAction = iota
	ActionUpdate
	ActionDelete
	ActionTransition
)

func (a AuditAction) String() string {
	switch a {
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	case ActionTransition:
		return "transition"
	default:
		return "unknown"
	}
}

// Entry is one audit-trail row — who changed what, on which record, when, and why. Built by the
// CRUD layer after a write's own transaction has already committed (see Store's doc comment for
// why this can't be inside that transaction).
//
// create: there is no real "before" state — Diff is built against an empty JsonObject, so every
// field reads as {"before": null, "after": value}. delete: there is no "after" state either —
// entries are action-based, not diff-based: Diff is left empty, the row's meaning carries
// entirely via Action. Never fabricate a synthetic "deleted" key here.
type Entry struct {
	TenantID         uuid.UUID
	Entity           string
	RecordID         uuid.UUID
	Action           AuditAction
	TransitionAction *string // Some only when Action == ActionTransition
	ActorUserID      *uuid.UUID
	Reason           *string
	Diff             JsonObject // {field: {"before": ..., "after": ...}}, changed keys only
	VersionAfter     *int32
	OccurredAt       time.Time
}

// Row is one row read back from metadata.audit_trail_entries — the read side of Store.Record's
// write. Mirrors the Rust AuditTrailEntryRow's #[serde(rename_all = "camelCase")] wire shape —
// JSON tags matter here, not just documentation, since this struct is what
// /api/{entity}/{id}/audit-events serializes directly.
type Row struct {
	ID               uuid.UUID       `json:"id"`
	TenantID         uuid.UUID       `json:"tenantId"`
	Entity           string          `json:"entity"`
	RecordID         uuid.UUID       `json:"recordId"`
	Action           string          `json:"action"`
	TransitionAction *string         `json:"transitionAction,omitempty"`
	ActorUserID      *uuid.UUID      `json:"actorUserId,omitempty"`
	Reason           *string         `json:"reason,omitempty"`
	Diff             json.RawMessage `json:"diff"`
	VersionAfter     *int32          `json:"versionAfter,omitempty"`
	OccurredAt       time.Time       `json:"occurredAt"`
}

// RedactedMarker is the key of the marker that stands in for a redacted field's
// {"before", "after"} pair.
const RedactedMarker = "redacted"

// RedactDiffFields replaces every fields entry already present in diff with {"redacted": true},
// dropping the values while keeping the fact that the field changed. Only rewrites keys the diff
// already has, never adds one — a field the write did not touch must stay absent.
func RedactDiffFields(diff JsonObject, fields []string) {
	marker, _ := json.Marshal(map[string]bool{RedactedMarker: true})
	for _, field := range fields {
		if _, ok := diff[field]; ok {
			diff[field] = marker
		}
	}
}
