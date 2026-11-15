package audit

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsurePartitions makes sure metadata.audit_trail_entries has a month partition for the current
// month through monthsAhead months ahead (migration 0038). It only ever creates partitions and
// moves rows out of the default partition into their month; it never deletes audit history.
// Returns how many partitions it created. Safe to call repeatedly.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, monthsAhead int) (int, error) {
	var created int
	err := pool.QueryRow(ctx,
		`SELECT metadata.ensure_audit_partitions($1::date, $2)`,
		time.Now().UTC().Format("2006-01-02"), monthsAhead).Scan(&created)
	return created, err
}

// PartitioningPresent reports whether this database has the partitioned audit table (migration
// 0038 applied), so a periodic caller can stay idle instead of failing every interval.
func PartitioningPresent(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var present bool
	err := pool.QueryRow(ctx,
		`SELECT to_regprocedure('metadata.ensure_audit_partitions(date, integer)') IS NOT NULL`).Scan(&present)
	return present, err
}

// BackfillParents fills parent_entity/parent_id on entries written before an entity declared
// EntityAuditConfig.Parent (or before migration 0039), from the parent reference column of each
// entry's record in table. Entries whose record no longer exists, or has no parent, stay NULL.
// Only touches rows with parent_id IS NULL, so it is safe to re-run. Returns rows updated.
func BackfillParents(ctx context.Context, pool *pgxpool.Pool, entity, table, parentField, parentEntity string) (int64, error) {
	tableIdent := pgx.Identifier(strings.Split(table, ".")).Sanitize()
	fieldIdent := pgx.Identifier{parentField}.Sanitize()
	tag, err := pool.Exec(ctx, `UPDATE metadata.audit_trail_entries a
		SET parent_entity = $1, parent_id = t.`+fieldIdent+`
		FROM `+tableIdent+` t
		WHERE a.entity = $2 AND a.record_id = t.id AND a.tenant_id = t.tenant_id
		  AND a.parent_id IS NULL AND t.`+fieldIdent+` IS NOT NULL`,
		parentEntity, entity)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
