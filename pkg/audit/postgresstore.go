package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxPageSize caps one page of Query, the same bound QueryPlanner enforces per list view: a
// record with a long edit history must not turn one response into an unbounded payload.
const MaxPageSize = 200

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
		 (tenant_id, entity, record_id, action, transition_action, actor_user_id, reason, diff, version_after, occurred_at,
		  parent_entity, parent_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		entry.TenantID, entry.Entity, entry.RecordID, entry.Action.String(), entry.TransitionAction,
		entry.ActorUserID, entry.Reason, diffJSON, entry.VersionAfter, entry.OccurredAt,
		entry.ParentEntity, entry.ParentID)
	return err
}

func (s *PostgresStore) Query(ctx context.Context, tenantID uuid.UUID, q Query) ([]Row, error) {
	limit := q.Limit
	if limit <= 0 || limit > MaxPageSize {
		limit = MaxPageSize
	}
	args := []any{tenantID, q.Entity, q.RecordID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	owner := "(entity = $2 AND record_id = $3)"
	if len(q.ChildEntities) > 0 {
		owner = fmt.Sprintf("(%s OR (parent_entity = $2 AND parent_id = $3 AND entity = ANY(%s)))", owner, arg(q.ChildEntities))
	}
	where := []string{"tenant_id = $1", owner}
	if len(q.Entities) > 0 {
		where = append(where, "entity = ANY("+arg(q.Entities)+")")
	}
	if len(q.Actions) > 0 {
		where = append(where, "action = ANY("+arg(q.Actions)+")")
	}
	if q.ActorUserID != nil {
		where = append(where, "actor_user_id = "+arg(*q.ActorUserID))
	}
	if q.From != nil {
		where = append(where, "occurred_at >= "+arg(*q.From))
	}
	if q.To != nil {
		where = append(where, "occurred_at < "+arg(*q.To))
	}
	if q.After != nil {
		at, id := arg(q.After.OccurredAt), arg(q.After.ID)
		where = append(where, fmt.Sprintf("(occurred_at, id) < (%s, %s)", at, id))
	}

	sql := `SELECT id, tenant_id, entity, record_id, action, transition_action, actor_user_id,
		 reason, diff, version_after, occurred_at, parent_entity, parent_id
		 FROM metadata.audit_trail_entries
		 WHERE ` + strings.Join(where, " AND ") + `
		 ORDER BY occurred_at DESC, id DESC LIMIT ` + arg(limit+1)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// make(..., 0), not var out []Row — see peripherals.ListTenantUsers's own comment on why
	// this matters for the wire shape of an empty result.
	out := make([]Row, 0)
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Entity, &r.RecordID, &r.Action, &r.TransitionAction,
			&r.ActorUserID, &r.Reason, &r.Diff, &r.VersionAfter, &r.OccurredAt, &r.ParentEntity, &r.ParentID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
