package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SearchQuery is a tenant-wide audit search (roadmap 115 M9): by person, by object, by time —
// not tied to one record the way Query is. Every field narrows; an empty query is the whole
// tenant's history, newest first.
type SearchQuery struct {
	ActorUserID *uuid.UUID // who
	Entity      string     // which kind of object
	RecordID    *uuid.UUID // which object (with Entity, or alone)
	Actions     []string
	From, To    *time.Time // occurred_at >= From, < To
	After       *Cursor
	Limit       int
}

// Search returns up to Limit+1 rows (so the caller can tell whether another page follows),
// newest first.
func (s *PostgresStore) Search(ctx context.Context, tenantID uuid.UUID, q SearchQuery) ([]Row, error) {
	limit := q.Limit
	if limit <= 0 || limit > MaxPageSize {
		limit = MaxPageSize
	}
	args := []any{tenantID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	where := []string{"tenant_id = $1"}
	if q.ActorUserID != nil {
		where = append(where, "actor_user_id = "+arg(*q.ActorUserID))
	}
	if q.Entity != "" {
		where = append(where, "entity = "+arg(q.Entity))
	}
	if q.RecordID != nil {
		where = append(where, "record_id = "+arg(*q.RecordID))
	}
	if len(q.Actions) > 0 {
		where = append(where, "action = ANY("+arg(q.Actions)+")")
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
	rows, err := s.pool.Query(ctx, `SELECT id, tenant_id, entity, record_id, action, transition_action, actor_user_id,
		 reason, diff, version_after, occurred_at, parent_entity, parent_id, seq, hash
		 FROM metadata.audit_trail_entries
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY occurred_at DESC, id DESC LIMIT `+arg(limit+1), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Row, 0)
	for rows.Next() {
		var r Row
		var hash []byte
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Entity, &r.RecordID, &r.Action, &r.TransitionAction,
			&r.ActorUserID, &r.Reason, &r.Diff, &r.VersionAfter, &r.OccurredAt, &r.ParentEntity, &r.ParentID,
			&r.Seq, &hash); err != nil {
			return nil, err
		}
		if hash != nil {
			r.Hash = fmt.Sprintf("%x", hash)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChainReport is what VerifyChain found.
type ChainReport struct {
	// Checked is how many chained entries were walked.
	Checked int64
	OK      bool
	// BrokenSeq is the first position that does not verify (0 when OK).
	BrokenSeq int64
	// Reason says what was wrong there: "gap", "prev_hash", "hash" or "head".
	Reason string
	// HeadSeq/HeadHash are the chain's recorded end. Keeping HeadHash somewhere the database
	// owner cannot rewrite (a ticket, a log shipped off the host) turns the chain from
	// tamper-evident into checkable against an outside witness.
	HeadSeq  int64
	HeadHash string
}

const verifyBatch = 5000

// VerifyChain walks the tenant's whole chain in order and recomputes every hash. It reads every
// chained entry, so it is an audit job, not a request-path call.
func (s *PostgresStore) VerifyChain(ctx context.Context, tenantID uuid.UUID) (ChainReport, error) {
	var rep ChainReport
	var headHash []byte
	err := s.pool.QueryRow(ctx, `SELECT seq, hash FROM metadata.audit_chain_heads WHERE tenant_id = $1`, tenantID).
		Scan(&rep.HeadSeq, &headHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return rep, err
	}
	rep.HeadHash = fmt.Sprintf("%x", headHash)

	prev := []byte{0}
	next := int64(1)
	for {
		rows, err := s.pool.Query(ctx, `SELECT seq, prev_hash, hash,
			metadata.audit_entry_hash(prev_hash, seq, tenant_id, id, entity, record_id, action, transition_action,
			  actor_user_id, reason, diff, version_after, occurred_at, parent_entity, parent_id)
			FROM metadata.audit_trail_entries
			WHERE tenant_id = $1 AND seq >= $2 ORDER BY seq LIMIT $3`, tenantID, next, verifyBatch)
		if err != nil {
			return rep, err
		}
		n := 0
		for rows.Next() {
			var seq int64
			var prevHash, hash, computed []byte
			if err := rows.Scan(&seq, &prevHash, &hash, &computed); err != nil {
				rows.Close()
				return rep, err
			}
			n++
			bad := func(reason string) (ChainReport, error) {
				rows.Close()
				rep.OK, rep.BrokenSeq, rep.Reason = false, next, reason
				return rep, nil
			}
			switch {
			case seq != next:
				return bad("gap")
			case !bytes.Equal(prevHash, prev):
				return bad("prev_hash")
			case !bytes.Equal(hash, computed):
				return bad("hash")
			}
			prev, next = hash, next+1
			rep.Checked++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return rep, err
		}
		if n < verifyBatch {
			break
		}
	}
	if rep.HeadSeq != next-1 || (rep.HeadSeq > 0 && !bytes.Equal(headHash, prev)) {
		rep.OK, rep.BrokenSeq, rep.Reason = false, next, "head"
		return rep, nil
	}
	rep.OK = true
	return rep, nil
}
