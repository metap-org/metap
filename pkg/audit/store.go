package audit

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Store is the swappable audit-trail sink — same shape as this codebase's other pluggable
// backends (SecretStore, ObjectStore, Cache): one interface, one impl per backend, wired in once
// at a binary's own composition root.
//
// Owns its own client/pool rather than borrowing the caller's open transaction — a Store that can
// point at a genuinely different database is structurally incompatible with sharing the caller's
// open transaction. The direct consequence: the CRUD layer calls Record only after its own
// business transaction has committed, never before or as part of it — an audit row must never
// claim a write happened that then rolled back. This makes the audit write best-effort (a crash
// in the narrow window between commit and this call drops that one entry), accepted for this
// iteration.
type Store interface {
	// Record also takes tenantID explicitly (also present on entry.TenantID) for the same
	// tenant-safety discipline ObjectStore/Cache establish — an impl is expected to assert the
	// two match rather than trust the caller silently.
	Record(ctx context.Context, tenantID uuid.UUID, entry Entry) error

	// Query is the read side: one record's entries (and, with ChildEntities, its children's),
	// newest first, one page at a time.
	Query(ctx context.Context, tenantID uuid.UUID, q Query) ([]Row, error)

	// Search is the tenant-wide read side: by person, object and time (roadmap 115 M9).
	Search(ctx context.Context, tenantID uuid.UUID, q SearchQuery) ([]Row, error)

	// VerifyChain recomputes the tenant's hash chain and reports the first broken link.
	VerifyChain(ctx context.Context, tenantID uuid.UUID) (ChainReport, error)
}

// Query selects one page of a record's audit history.
type Query struct {
	Entity   string
	RecordID uuid.UUID
	// ChildEntities also includes entries whose parent is this record, for these entities only
	// (the caller decides which child entities the reader may see). Empty: the record alone.
	ChildEntities []string
	// Entities, when set, keeps only entries of these entities (the record's own and children's).
	Entities []string
	// Actions, when set, keeps only these actions ("create", "update", "delete", "transition").
	Actions     []string
	ActorUserID *uuid.UUID
	From, To    *time.Time // occurred_at >= From, < To
	// After continues a previous page: entries strictly older than this (occurredAt, id) key.
	After *Cursor
	// Limit is the page size; the store returns up to Limit+1 rows so the caller can tell
	// whether another page exists.
	Limit int
}

// Cursor is the keyset position of the last entry on a page.
type Cursor struct {
	OccurredAt time.Time
	ID         uuid.UUID
}
