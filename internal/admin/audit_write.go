package admin

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
)

// writeAdminAudit records one audit entry for an administrative change: who (actor, uuid.Nil for
// none), on which administered thing (entity, id), what kind of change, and a before/after pair per
// field. Best-effort like the CRUD layer's: the change itself has already committed, and a store
// error must not turn a successful change into a failed response. A nil store records nothing.
//
// Every administrative write goes through here (identityDeps.record and recordFor, the policy and
// config services), so what an admin changes is in one trail whatever service made the change.
func writeAdminAudit(ctx context.Context, store audit.Store, tenant, actor uuid.UUID, entity string, id uuid.UUID,
	action audit.AuditAction, diff map[string][2]any, now time.Time) {
	kernel.WriteAudit(ctx, store, tenant, actor, entity, id, action, diff, now)
}

// namedID is a stable id for something an audit entry is about that has no uuid of its own (a
// configuration key, the policy set of one entity): the same name always gives the same id, so its
// history reads as one record's.
func namedID(kind, name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(kind+":"+name))
}
