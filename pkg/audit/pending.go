package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap/pkg/messaging/outbox"
)

// Topic is the outbox topic an audit entry is written under.
const Topic = outbox.AuditTopic

// pendingVersion is the version of the payload an entry travels in; adding a field is not a bump.
const pendingVersion = 1

// wireEntry is Entry as it travels through the outbox: the action by name, so reordering the
// AuditAction constants can never change what a stored row means.
type wireEntry struct {
	TenantID         uuid.UUID  `json:"tenantId"`
	Entity           string     `json:"entity"`
	RecordID         uuid.UUID  `json:"recordId"`
	Action           string     `json:"action"`
	TransitionAction *string    `json:"transitionAction,omitempty"`
	ActorUserID      *uuid.UUID `json:"actorUserId,omitempty"`
	Reason           *string    `json:"reason,omitempty"`
	Diff             JsonObject `json:"diff,omitempty"`
	VersionAfter     *int32     `json:"versionAfter,omitempty"`
	OccurredAt       time.Time  `json:"occurredAt"`
	ParentEntity     *string    `json:"parentEntity,omitempty"`
	ParentID         *uuid.UUID `json:"parentId,omitempty"`
}

type pending struct {
	Version int       `json:"v"`
	Entry   wireEntry `json:"entry"`
}

// Executor is what Enqueue writes through: the business transaction (a pgx.Tx) or anything with
// its Exec.
type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Enqueue writes entry to the outbox in the caller's transaction, so the entry exists exactly when
// the writes it describes do. The drain (Drain, RunDrain) moves it into the tenant's hash chain.
func Enqueue(ctx context.Context, exec Executor, entry Entry) error {
	raw, err := json.Marshal(pending{Version: pendingVersion, Entry: wireEntry{
		TenantID: entry.TenantID, Entity: entry.Entity, RecordID: entry.RecordID, Action: entry.Action.String(),
		TransitionAction: entry.TransitionAction, ActorUserID: entry.ActorUserID, Reason: entry.Reason,
		Diff: entry.Diff, VersionAfter: entry.VersionAfter, OccurredAt: entry.OccurredAt.UTC(),
		ParentEntity: entry.ParentEntity, ParentID: entry.ParentID,
	}})
	if err != nil {
		return fmt.Errorf("marshaling audit entry: %w", err)
	}
	return outbox.Enqueue(ctx, exec, &outbox.Event{
		Topic: Topic, AggregateType: entry.Entity, AggregateID: entry.RecordID, Payload: raw,
	})
}

// decodePending reads an outbox payload back into an Entry.
func decodePending(raw []byte) (Entry, error) {
	var p pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return Entry{}, err
	}
	action, ok := ParseAction(p.Entry.Action)
	if !ok {
		return Entry{}, fmt.Errorf("unknown audit action %q", p.Entry.Action)
	}
	w := p.Entry
	return Entry{
		TenantID: w.TenantID, Entity: w.Entity, RecordID: w.RecordID, Action: action,
		TransitionAction: w.TransitionAction, ActorUserID: w.ActorUserID, Reason: w.Reason,
		Diff: w.Diff, VersionAfter: w.VersionAfter, OccurredAt: w.OccurredAt,
		ParentEntity: w.ParentEntity, ParentID: w.ParentID,
	}, nil
}
