package audit

import (
	"context"
	"time"

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
