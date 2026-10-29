package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxEntriesPerRecord matches the cap QueryPlanner enforces per entity list view — a record with
// an unusually long edit history must not turn one HTTP response into an unbounded payload.
const maxEntriesPerRecord = 200

// PostgresStore is the default Store — one shared table (metadata.audit_trail_entries), matching
// this codebase's own convention for every other framework-level append-only log (workflow_events,
// outbox_events are all single shared tables too — table-per-entity is reserved for
// business-entity data, a different concept).
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) Record(ctx context.Context, tenantID uuid.UUID, entry Entry) error {
	if tenantID != entry.TenantID {
		return fmt.Errorf("audit.Store.Record called with tenantID %s but entry.TenantID is %s", tenantID, entry.TenantID)
	}
	diffJSON, err := json.Marshal(entry.Diff)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO metadata.audit_trail_entries
		 (tenant_id, entity, record_id, action, transition_action, actor_user_id, reason, diff, version_after, occurred_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		entry.TenantID, entry.Entity, entry.RecordID, entry.Action.String(), entry.TransitionAction,
		entry.ActorUserID, entry.Reason, diffJSON, entry.VersionAfter, entry.OccurredAt)
	return err
}

func (s *PostgresStore) ListForRecord(ctx context.Context, tenantID uuid.UUID, entity string, recordID uuid.UUID) ([]Row, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, entity, record_id, action, transition_action, actor_user_id,
		 reason, diff, version_after, occurred_at
		 FROM metadata.audit_trail_entries
		 WHERE tenant_id = $1 AND entity = $2 AND record_id = $3
		 ORDER BY occurred_at DESC LIMIT $4`,
		tenantID, entity, recordID, maxEntriesPerRecord)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Entity, &r.RecordID, &r.Action, &r.TransitionAction,
			&r.ActorUserID, &r.Reason, &r.Diff, &r.VersionAfter, &r.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
