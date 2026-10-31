package audit

import (
	"context"

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

	// ListForRecord is the read side — every entry recorded for one (tenant, entity, record),
	// newest first, capped the same way QueryPlanner enforces a max limit everywhere else.
	ListForRecord(ctx context.Context, tenantID uuid.UUID, entity string, recordID uuid.UUID) ([]Row, error)
}
