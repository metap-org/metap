package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	return s.record(ctx, tenantID, uuid.New(), entry, false)
}

// RecordOnce writes entry under id, or does nothing when an entry with that id (and time) is
// already in the chain: the drain calls it with the id of the outbox row the entry arrived in, so
// a row delivered twice (a crash between the write and marking the row done) lands once. The check
// runs under the tenant's chain lock, so two drains of the same row cannot both pass it.
func (s *PostgresStore) RecordOnce(ctx context.Context, tenantID, id uuid.UUID, entry Entry) error {
	return s.record(ctx, tenantID, id, entry, true)
}

func (s *PostgresStore) record(ctx context.Context, tenantID, id uuid.UUID, entry Entry, once bool) error {
	if tenantID != entry.TenantID {
		return fmt.Errorf("audit.Store.Record called with tenantID %s but entry.TenantID is %s", tenantID, entry.TenantID)
	}
	diffJSON, err := json.Marshal(entry.Diff)
	if err != nil {
		return err
	}
	// Postgres stores microseconds; hash what is stored, not the nanoseconds Go carries.
	occurred := entry.OccurredAt.UTC().Truncate(time.Microsecond)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Make sure the tenant's head exists, then lock it: the chain is written one entry at a time
	// per tenant (see migration 0050).
	if _, err := tx.Exec(ctx,
		`INSERT INTO metadata.audit_chain_heads (tenant_id, seq, hash) VALUES ($1, 0, '\x00')
		 ON CONFLICT (tenant_id) DO NOTHING`, tenantID); err != nil {
		return err
	}
	var prevSeq int64
	var prevHash []byte
	if err := tx.QueryRow(ctx,
		`SELECT seq, hash FROM metadata.audit_chain_heads WHERE tenant_id = $1 FOR UPDATE`, tenantID).
		Scan(&prevSeq, &prevHash); err != nil {
		return err
	}
	if once {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND id = $2 AND occurred_at = $3)`,
			tenantID, id, occurred).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	seq := prevSeq + 1
	var hash []byte
	if err := tx.QueryRow(ctx,
		`INSERT INTO metadata.audit_trail_entries
		 (id, tenant_id, entity, record_id, action, transition_action, actor_user_id, reason, diff, version_after, occurred_at,
		  parent_entity, parent_id, seq, prev_hash, hash)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
		  metadata.audit_entry_hash($15, $14, $2, $1, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13))
		 RETURNING hash`,
		id, entry.TenantID, entry.Entity, entry.RecordID, entry.Action.String(), entry.TransitionAction,
		entry.ActorUserID, entry.Reason, diffJSON, entry.VersionAfter, occurred,
		entry.ParentEntity, entry.ParentID, seq, prevHash).Scan(&hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE metadata.audit_chain_heads SET seq = $2, hash = $3 WHERE tenant_id = $1`, tenantID, seq, hash); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
