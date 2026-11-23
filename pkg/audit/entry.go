package audit

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AuditAction is which of CrudService's 4 writes, or which declared action, produced an entry.
type AuditAction int

const (
	ActionCreate AuditAction = iota
	ActionUpdate
	ActionDelete
	ActionTransition
	// ActionCustom is a declared entity action run by an application's own handler (pkg/action);
	// TransitionAction carries the action's name.
	ActionCustom
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
	case ActionCustom:
		return "action"
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
	TransitionAction *string // Some only when Action == ActionTransition or ActionCustom (the declared action's name)
	ActorUserID      *uuid.UUID
	Reason           *string
	Diff             JsonObject // {field: {"before": ..., "after": ...}}, changed keys only
	VersionAfter     *int32
	OccurredAt       time.Time
	// ParentEntity/ParentID: the record that owns this one (EntityAuditConfig.Parent), so the
	// parent's history can include this entry. Both nil when the entity declares no parent or
	// this record has none (e.g. a tenant-wide rule with no zone).
	ParentEntity *string
	ParentID     *uuid.UUID
}

// Row is one row read back from metadata.audit_trail_entries — the read side of Store.Record's
// write.
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
	ParentEntity     *string         `json:"parentEntity,omitempty"`
	ParentID         *uuid.UUID      `json:"parentId,omitempty"`
	// Seq/Hash place the entry in its tenant's tamper-evident chain (hex hash). Zero/empty for
	// history written before the chain existed. Only filled by Search.
	Seq  *int64 `json:"seq,omitempty"`
	Hash string `json:"hash,omitempty"`
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
