// E2E tests running Reconcile against a real Postgres. A reduced-but-real subset of the Rust
// metap-reconciler crate's tests/reconcile_postgres.rs: basic convergence, the historic WAF
// unique-constraint-to-partial-index transition bug, storage:column backfill + sync trigger,
// and the BackfillScope (SingleTenant vs AllTenants) fix for a sentinel-tenant boot-time
// reconcile against a shared table. The Rust suite's remaining tests (orphan-reference-style
// multi-tenant isolation variants, composite-unique-with-reference, sync-trigger-dropped-
// externally recovery) are not ported here — each exercises the same already-covered mechanism
// (BackfillScope/introspect's sync-trigger re-check) from a different angle, not new code paths
// in this port.
package reconciler

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
)

func reconcilerTestPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// dropReconcilerTableIfExists: table is a bare name — every table-per-entity table lives in
// EntitySchema, never public.
func dropReconcilerTableIfExists(t *testing.T, pool *pgxpool.Pool, table string) {
	_, err := pool.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %q.%q CASCADE`, EntitySchema, table))
	if err != nil {
		t.Fatal(err)
	}
}

// The single most important correctness property of a level-triggered reconciler: reconciling
// an unchanged desired state twice in a row must produce zero ops the second time.
func TestReconcileConvergesToZeroOpsOnASecondPass(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	entityName := "test.reconciler_convergence"
	dropReconcilerTableIfExists(t, pool, "test_reconciler_convergence")
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, "test_reconciler_convergence") })

	indexed := plainField("state", metadata.KindEnum)
	indexed.Indexed = boolp(true)
	indexed.EnumValues = []string{"open", "closed"}
	searchable := plainField("title", metadata.KindString)
	searchable.Searchable = boolp(true)
	ftsField := plainField("description", metadata.KindString)
	ftsField.Searchable = boolp(true)
	ftsField.SearchMode = strp("fts")

	def := testEntity(entityName, []metadata.EntityField{indexed, searchable, ftsField})

	first, err := Reconcile(ctx, pool, tenantID, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.OpsApplied == 0 {
		t.Fatal("first reconcile must actually do work")
	}
	if first.Table != "entities.test_reconciler_convergence" {
		t.Fatalf("got %q", first.Table)
	}

	second, err := Reconcile(ctx, pool, tenantID, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.OpsApplied != 0 {
		t.Fatalf("second reconcile against the same desired state must be a no-op, got %d ops", second.OpsApplied)
	}

	third, err := Reconcile(ctx, pool, tenantID, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.OpsApplied != 0 {
		t.Fatalf("convergence must hold indefinitely, got %d ops", third.OpsApplied)
	}
}

// Regression for the real, twice-hit "transition-state" bug: a unique:true field still enforced
// by the *old* blanket UNIQUE constraint shape must converge to today's partial index in one
// corrective Reconcile pass, not stay stuck forever.
func TestUniqueFieldConvergesFromALegacyBlanketConstraintToAPartialIndexInOnePass(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	entityName := "test.reconciler_unique_transition"
	dropReconcilerTableIfExists(t, pool, "test_reconciler_unique_transition")
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, "test_reconciler_unique_transition") })

	sku := plainField("sku", metadata.KindString)
	sku.Unique = boolp(true)
	storageColumn := metadata.StorageColumn
	sku.Storage = &storageColumn
	def := testEntity(entityName, []metadata.EntityField{sku})

	// Pass 1: reconcile for real, producing today's correct shape — a partial unique index.
	first, err := Reconcile(ctx, pool, tenantID, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.OpsApplied == 0 {
		t.Fatal("expected ops")
	}

	var indexName string
	err = pool.QueryRow(ctx,
		`SELECT indexname FROM pg_indexes WHERE schemaname = 'entities'
		 AND tablename = 'test_reconciler_unique_transition' AND indexname LIKE '%sku%'`).Scan(&indexName)
	if err != nil {
		t.Fatal(err)
	}

	// Revert it to the legacy shape a pre-fix compile() produced: a blanket UNIQUE constraint
	// under the *same* name, no partial predicate.
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP INDEX CONCURRENTLY "entities".%q`, indexName)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE entities.test_reconciler_unique_transition ADD CONSTRAINT %q UNIQUE (sku)`, indexName)); err != nil {
		t.Fatal(err)
	}

	// Pass 2: reconcile against the same desired state again.
	if _, err := Reconcile(ctx, pool, tenantID, def, nil); err != nil {
		t.Fatal(err)
	}

	third, err := Reconcile(ctx, pool, tenantID, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.OpsApplied != 0 {
		t.Fatalf("must converge within 1 corrective pass, got %d ops", third.OpsApplied)
	}

	var isPartial bool
	err = pool.QueryRow(ctx,
		`SELECT indpred IS NOT NULL FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1`,
		indexName).Scan(&isPartial)
	if err != nil {
		t.Fatal(err)
	}
	if !isPartial {
		t.Fatal("must end up a partial index, not still the blanket constraint")
	}
}

// storage:column end to end: a field promoted to a real column on an entity that already has
// existing rows must have those rows correctly backfilled, and the sync trigger must keep a
// newly-written row consistent without needing another reconcile.
func TestStorageColumnBackfillsExistingRowsAndSyncsNewOnes(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	entityName := "test.reconciler_storage_column"
	dropReconcilerTableIfExists(t, pool, "test_reconciler_storage_column")
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, "test_reconciler_storage_column") })

	defV1 := testEntity(entityName, []metadata.EntityField{plainField("amount", metadata.KindMoney)})
	if _, err := Reconcile(ctx, pool, tenantID, defV1, nil); err != nil {
		t.Fatal(err)
	}

	for _, amount := range []string{"12.50", "7.25", "100.00"} {
		_, err := pool.Exec(ctx,
			`INSERT INTO entities.test_reconciler_storage_column (tenant_id, data) VALUES ($1, jsonb_build_object('amount', $2::text))`,
			tenantID, amount)
		if err != nil {
			t.Fatal(err)
		}
	}

	promoted := plainField("amount", metadata.KindMoney)
	promoted.Indexed = boolp(true)
	storageColumn := metadata.StorageColumn
	promoted.Storage = &storageColumn
	defV2 := testEntity(entityName, []metadata.EntityField{promoted})
	outcome, err := Reconcile(ctx, pool, tenantID, defV2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OpsApplied == 0 {
		t.Fatal("expected ops")
	}

	rows, err := pool.Query(ctx, `SELECT amount::float8 FROM entities.test_reconciler_storage_column ORDER BY amount`)
	if err != nil {
		t.Fatal(err)
	}
	var backfilled []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		backfilled = append(backfilled, v)
	}
	rows.Close()
	want := []float64{7.25, 12.50, 100.00}
	if len(backfilled) != len(want) {
		t.Fatalf("got %v, want %v", backfilled, want)
	}
	for i := range want {
		if backfilled[i] != want[i] {
			t.Fatalf("got %v, want %v", backfilled, want)
		}
	}

	// A new row written directly must still end up with the real column populated, via the
	// sync trigger.
	if _, err := pool.Exec(ctx,
		`INSERT INTO entities.test_reconciler_storage_column (tenant_id, data) VALUES ($1, jsonb_build_object('amount', '42.00'))`,
		tenantID); err != nil {
		t.Fatal(err)
	}
	var synced float64
	if err := pool.QueryRow(ctx, `SELECT amount::float8 FROM entities.test_reconciler_storage_column WHERE amount = 42.00`).Scan(&synced); err != nil {
		t.Fatal(err)
	}
	if synced != 42.00 {
		t.Fatalf("got %v", synced)
	}

	third, err := Reconcile(ctx, pool, tenantID, defV2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.OpsApplied != 0 {
		t.Fatalf("backfill must be recognized as already-done, not re-queued, got %d ops", third.OpsApplied)
	}
}

// Regression for the BackfillScope fix: a Schema-strategy service reconciling its own shared
// table at boot always passes a sentinel tenant id, never one of the real tenants whose rows
// actually live in the table. Confirms both halves: SingleTenant (the historical default)
// reproduces the bug live — touches nothing real — and AllTenants is the fix.
func TestAllTenantsScopeBackfillsASharedTableReconciledWithASentinelTenantID(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	sentinelTenantID := uuid.New() // stands in for control.PlatformTenantID
	realTenantA := uuid.New()
	realTenantB := uuid.New()
	entityName := "test.reconciler_backfill_scope"
	dropReconcilerTableIfExists(t, pool, "test_reconciler_backfill_scope")
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, "test_reconciler_backfill_scope") })

	defV1 := testEntity(entityName, []metadata.EntityField{plainField("amount", metadata.KindMoney)})
	if _, err := Reconcile(ctx, pool, sentinelTenantID, defV1, nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		tenant uuid.UUID
		amount string
	}{{realTenantA, "12.50"}, {realTenantB, "999.99"}} {
		_, err := pool.Exec(ctx,
			`INSERT INTO entities.test_reconciler_backfill_scope (tenant_id, data) VALUES ($1, jsonb_build_object('amount', $2::text))`,
			tc.tenant, tc.amount)
		if err != nil {
			t.Fatal(err)
		}
	}

	promoted := plainField("amount", metadata.KindMoney)
	promoted.Indexed = boolp(true)
	storageColumn := metadata.StorageColumn
	promoted.Storage = &storageColumn
	defV2 := testEntity(entityName, []metadata.EntityField{promoted})

	amounts := func() []*float64 {
		rows, err := pool.Query(ctx, `SELECT amount::float8 FROM entities.test_reconciler_backfill_scope ORDER BY amount`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []*float64
		for rows.Next() {
			var v *float64
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		return out
	}

	// The bug, reproduced live: SingleTenant scoped to the sentinel, which owns zero rows in
	// this table.
	buggy, err := Reconcile(ctx, pool, sentinelTenantID, defV2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if buggy.OpsApplied == 0 {
		t.Fatal("expected ops")
	}
	got := amounts()
	if len(got) != 2 || got[0] != nil || got[1] != nil {
		t.Fatalf("SingleTenant scoped to a sentinel must touch neither real tenant's row — this is the bug; got %v", got)
	}

	// Un-stick it: drop the backfill's own progress row so the next reconcile re-attempts it.
	if _, err := pool.Exec(ctx, `DELETE FROM metadata.reconciler_backfill_progress WHERE tenant_id = $1 AND entity_name = $2`, sentinelTenantID, entityName); err != nil {
		t.Fatal(err)
	}

	// The fix: same sentinel tenant id, but AllTenants scope.
	fixed, err := ReconcileWithScope(ctx, pool, sentinelTenantID, defV2, nil, ScopeAllTenants)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.OpsApplied == 0 {
		t.Fatal("expected ops")
	}
	got = amounts()
	if len(got) != 2 || got[0] == nil || got[1] == nil {
		t.Fatalf("AllTenants must backfill every real tenant's row in the shared table; got %v", got)
	}

	converged, err := ReconcileWithScope(ctx, pool, sentinelTenantID, defV2, nil, ScopeAllTenants)
	if err != nil {
		t.Fatal(err)
	}
	if converged.OpsApplied != 0 {
		t.Fatalf("got %d ops", converged.OpsApplied)
	}
}

// A Reference field whose RefEntity already has its own table gets a real FK.
func TestReferenceFieldGetsARealFkOnceBothEntitiesAreTables(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	dropReconcilerTableIfExists(t, pool, "test_reconciler_departments")
	dropReconcilerTableIfExists(t, pool, "test_reconciler_employees")
	t.Cleanup(func() {
		dropReconcilerTableIfExists(t, pool, "test_reconciler_employees")
		dropReconcilerTableIfExists(t, pool, "test_reconciler_departments")
	})

	departments := testEntity("test.reconciler_departments", []metadata.EntityField{plainField("name", metadata.KindString)})
	if _, err := Reconcile(ctx, pool, tenantID, departments, nil); err != nil {
		t.Fatal(err)
	}

	deptRef := plainField("departmentId", metadata.KindReference)
	refEntity := "test.reconciler_departments"
	deptRef.RefEntity = &refEntity
	employees := testEntity("test.reconciler_employees", []metadata.EntityField{deptRef})
	outcome, err := Reconcile(ctx, pool, tenantID, employees, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OpsApplied == 0 {
		t.Fatal("expected ops")
	}

	var fkExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid
		 WHERE t.relname = 'test_reconciler_employees' AND c.contype = 'f' AND c.convalidated)`).Scan(&fkExists)
	if err != nil {
		t.Fatal(err)
	}
	if !fkExists {
		t.Fatal("FK to test_reconciler_departments must exist and be validated")
	}
}
