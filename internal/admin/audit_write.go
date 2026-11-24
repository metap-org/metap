package admin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

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
	if store == nil {
		return
	}
	obj := audit.JsonObject{}
	for field, ba := range diff {
		raw, err := json.Marshal(map[string]any{"before": ba[0], "after": ba[1]})
		if err == nil {
			obj[field] = raw
		}
	}
	entry := audit.Entry{TenantID: tenant, Entity: entity, RecordID: id, Action: action, Diff: obj, OccurredAt: now}
	if actor != uuid.Nil {
		entry.ActorUserID = &actor
	}
	_ = store.Record(ctx, tenant, entry)
}

// namedID is a stable id for something an audit entry is about that has no uuid of its own (a
// configuration key, the policy set of one entity): the same name always gives the same id, so its
// history reads as one record's.
func namedID(kind, name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(kind+":"+name))
}
