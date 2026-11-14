//go:build integration

// E2E tests running Reconcile against a real Postgres.
package reconciler

import (
	"context"
	"fmt"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/metadata"
)

func reconcilerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t)
	return pool
}

// dropReconcilerTableIfExists: table is a bare name — every table-per-entity table lives in
// EntitySchema, never public.
func dropReconcilerTableIfExists(t *testing.T, pool *pgxpool.Pool, table string) {
	_, err := pool.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS %q.%q CASCADE`, EntitySchema, table))
	require.NoError(t, err)
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
	require.NoError(t, err)
	require.NotEqual(t, 0, first.OpsApplied, "first reconcile must actually do work")
	require.Equal(t, "entities.test_reconciler_convergence", first.Table, "got %q", first.Table)

	second, err := Reconcile(ctx, pool, tenantID, def, nil)
	require.NoError(t, err)
	require.Equal(t, 0, second.OpsApplied, "second reconcile against the same desired state must be a no-op, got %d ops", second.OpsApplied)

	third, err := Reconcile(ctx, pool, tenantID, def, nil)
	require.NoError(t, err)
	require.Equal(t, 0, third.OpsApplied, "convergence must hold indefinitely, got %d ops", third.OpsApplied)
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
	def := testEntity(entityName, []metadata.EntityField{sku})

	// Pass 1: reconcile for real, producing today's correct shape — a partial unique index.
	first, err := Reconcile(ctx, pool, tenantID, def, nil)
	require.NoError(t, err)
	require.NotEqual(t, 0, first.OpsApplied, "expected ops")

	var indexName string
	err = pool.QueryRow(ctx,
		`SELECT indexname FROM pg_indexes WHERE schemaname = 'entities'
		 AND tablename = 'test_reconciler_unique_transition' AND indexname LIKE '%sku%'`).Scan(&indexName)
	require.NoError(t, err)

	// Revert it to the legacy shape a pre-fix compile() produced: a blanket UNIQUE constraint
	// under the *same* name, no partial predicate.
	_, err = pool.Exec(ctx, fmt.Sprintf(`DROP INDEX CONCURRENTLY "entities".%q`, indexName))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE entities.test_reconciler_unique_transition ADD CONSTRAINT %q UNIQUE (sku)`, indexName))
	require.NoError(t, err)

	// Pass 2: reconcile against the same desired state again.
	_, err = Reconcile(ctx, pool, tenantID, def, nil)
	require.NoError(t, err)

	third, err := Reconcile(ctx, pool, tenantID, def, nil)
	require.NoError(t, err)
	require.Equal(t, 0, third.OpsApplied, "must converge within 1 corrective pass, got %d ops", third.OpsApplied)

	var isPartial bool
	err = pool.QueryRow(ctx,
		`SELECT indpred IS NOT NULL FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1`,
		indexName).Scan(&isPartial)
	require.NoError(t, err)
	require.True(t, isPartial, "must end up a partial index, not still the blanket constraint")
}

// createLegacyTable builds a table the way the code before Phase 106 did: framework columns plus
// a data jsonb payload, and (for "amount") a real column kept in sync by the old trigger.
func createLegacyTable(t *testing.T, pool *pgxpool.Pool, bare string) {
	t.Helper()
	ctx := context.Background()
	dropReconcilerTableIfExists(t, pool, bare)
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, bare) })
	for _, stmt := range []string{
		`CREATE SCHEMA IF NOT EXISTS entities`,
		fmt.Sprintf(`CREATE TABLE entities.%s (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL, code varchar(120), status varchar(80),
			data jsonb NOT NULL DEFAULT '{}'::jsonb, version integer NOT NULL DEFAULT 1, deleted boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), created_by uuid, updated_by uuid,
			amount numeric(18,4))`, bare),
		fmt.Sprintf(`CREATE FUNCTION entities.sync_%[1]s_amount() RETURNS trigger AS $$ BEGIN NEW.amount := (NEW.data ->> 'amount')::numeric(18,4); RETURN NEW; END; $$ LANGUAGE plpgsql`, bare),
		fmt.Sprintf(`CREATE TRIGGER trg_sync_%[1]s_amount BEFORE INSERT OR UPDATE ON entities.%[1]s FOR EACH ROW EXECUTE FUNCTION entities.sync_%[1]s_amount()`, bare),
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
}

func legacyEntity(entityName string) metadata.EntityDefinition {
	amount := plainField("amount", metadata.KindMoney)
	amount.Indexed = boolp(true)
	return testEntity(entityName, []metadata.EntityField{
		amount, plainField("title", metadata.KindString), plainField("due", metadata.KindDate),
		plainField("done", metadata.KindBoolean), plainField("extra", metadata.KindJSON),
	})
}

func seedLegacyRows(t *testing.T, pool *pgxpool.Pool, bare string, tenants ...uuid.UUID) {
	t.Helper()
	for i, tenant := range tenants {
		_, err := pool.Exec(context.Background(), fmt.Sprintf(
			`INSERT INTO entities.%s (tenant_id, data) VALUES ($1, $2::jsonb)`, bare),
			tenant, fmt.Sprintf(`{"amount":"%d.50","title":"row %d","due":"2026-01-%02d","done":true,"extra":{"a":[1,2]}}`, 10+i, i, i+1))
		require.NoError(t, err)
	}
	// A row with most fields absent and a JSON null: neither may become a value.
	_, err := pool.Exec(context.Background(), fmt.Sprintf(
		`INSERT INTO entities.%s (tenant_id, data) VALUES ($1, '{"title":"sparse","due":null}'::jsonb)`, bare), tenants[0])
	require.NoError(t, err)
}

// Phase 106 end to end on a legacy table: refused by a normal reconcile, then moved to native
// columns across every tenant sharing the table (sentinel tenant id, AllTenants scope), verified,
// and only then is the data column dropped.
func TestLegacyTableMovesToNativeColumnsAcrossTenantsThenDropsData(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	sentinel := uuid.New()
	entityName, bare := "test.reconciler_legacy_move", "test_reconciler_legacy_move"
	table := "entities." + bare
	createLegacyTable(t, pool, bare)
	tenantA, tenantB := uuid.New(), uuid.New()
	seedLegacyRows(t, pool, bare, tenantA, tenantB)
	def := legacyEntity(entityName)

	_, err := Reconcile(ctx, pool, sentinel, def, nil)
	require.ErrorIs(t, err, ErrLegacyDataColumn, "a normal reconcile must refuse a table that still has data")

	out, err := MigrateLegacy(ctx, pool, sentinel, def, ScopeAllTenants)
	require.NoError(t, err)
	require.NotZero(t, out.OpsApplied)

	var title *string
	var amount *float64
	var done *bool
	var due *string
	var extra *string
	require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT title, amount::float8, done, due::text, extra::text FROM %s WHERE tenant_id = $1 AND title = 'row 1'`, table), tenantB).
		Scan(&title, &amount, &done, &due, &extra))
	require.Equal(t, "row 1", *title)
	require.Equal(t, 11.5, *amount)
	require.True(t, *done)
	require.Equal(t, "2026-01-02", *due)
	require.JSONEq(t, `{"a":[1,2]}`, *extra)

	var nullDue bool
	require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(`SELECT due IS NULL AND amount IS NULL FROM %s WHERE title = 'sparse'`, table)).Scan(&nullDue))
	require.True(t, nullDue, "an absent field and a JSON null must both stay NULL")

	var triggers int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_trigger WHERE tgrelid = $1::regclass AND NOT tgisinternal`, table).Scan(&triggers))
	require.Zero(t, triggers, "the old sync trigger must be gone, or it would overwrite native writes with NULL")

	report, err := VerifyLegacy(ctx, pool, sentinel, def)
	require.NoError(t, err)
	require.Equal(t, int64(3), report.Rows)
	require.True(t, report.Ok(), "%+v", report)

	require.NoError(t, DropLegacyData(ctx, pool, sentinel, def))
	var hasData bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'entities' AND table_name = $1 AND column_name = 'data')`, bare).Scan(&hasData))
	require.False(t, hasData)

	converged, err := Reconcile(ctx, pool, sentinel, def, nil)
	require.NoError(t, err)
	require.Zero(t, converged.OpsApplied, "a migrated table must converge to zero ops, got %d", converged.OpsApplied)

	var count int
	require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, table)).Scan(&count))
	require.Equal(t, 3, count, "no row may be lost by the move")
}

// SingleTenant scope under a sentinel owns zero rows, so it must not be mistaken for a finished
// migration: the report keeps blocking the drop.
func TestDropLegacyDataRefusesWhileAValueExistsOnlyInData(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	sentinel := uuid.New()
	entityName, bare := "test.reconciler_legacy_refuse", "test_reconciler_legacy_refuse"
	createLegacyTable(t, pool, bare)
	seedLegacyRows(t, pool, bare, uuid.New())
	def := legacyEntity(entityName)

	_, err := MigrateLegacy(ctx, pool, sentinel, def, ScopeSingleTenant)
	require.NoError(t, err)

	report, err := VerifyLegacy(ctx, pool, sentinel, def)
	require.NoError(t, err)
	require.False(t, report.Ok(), "rows the sentinel does not own were never copied: %+v", report)
	require.Error(t, DropLegacyData(ctx, pool, sentinel, def))

	var hasData bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'entities' AND table_name = $1 AND column_name = 'data')`, bare).Scan(&hasData))
	require.True(t, hasData, "the data column must survive a refused drop")
}

// A new entity is created native from the start: no data column at all.
func TestNewEntityTableHasNoDataColumn(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	entityName, bare := "test.reconciler_native_new", "test_reconciler_native_new"
	dropReconcilerTableIfExists(t, pool, bare)
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, bare) })

	def := legacyEntity(entityName)
	_, err := Reconcile(ctx, pool, uuid.New(), def, nil)
	require.NoError(t, err)

	var cols []string
	rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'entities' AND table_name = $1`, bare)
	require.NoError(t, err)
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	rows.Close()
	require.NotContains(t, cols, "data")
	require.Subset(t, cols, []string{"amount", "title", "due", "done", "extra", "id", "tenant_id", "version", "deleted"})

	again, err := Reconcile(ctx, pool, uuid.New(), def, nil)
	require.NoError(t, err)
	require.Zero(t, again.OpsApplied)
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
		require.FailNow(t, fmt.Sprint(err))
	}

	deptRef := plainField("departmentId", metadata.KindReference)
	refEntity := "test.reconciler_departments"
	deptRef.RefEntity = &refEntity
	employees := testEntity("test.reconciler_employees", []metadata.EntityField{deptRef})
	outcome, err := Reconcile(ctx, pool, tenantID, employees, nil)
	require.NoError(t, err)
	require.NotEqual(t, 0, outcome.OpsApplied, "expected ops")

	var fkExists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid
		 WHERE t.relname = 'test_reconciler_employees' AND c.contype = 'f' AND c.convalidated)`).Scan(&fkExists)
	require.NoError(t, err)
	require.True(t, fkExists, "FK to test_reconciler_departments must exist and be validated")
}

// insertRawRow writes one row straight into a reconciled table, bypassing CrudService, so a test
// can probe what the physical unique indexes allow.
func insertRawRow(ctx context.Context, pool *pgxpool.Pool, table string, tenantID uuid.UUID, data string) error {
	// data is a JSON object keyed by column name; jsonb_populate_record spreads it over the table's
	// own columns, so the helper does not care which fields a test entity declares.
	_, err := pool.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %[1]s SELECT r.* FROM jsonb_populate_record(NULL::%[1]s, $3::jsonb || jsonb_build_object('id', $1::text, 'tenant_id', $2::text, 'version', 1, 'deleted', false, 'created_at', now()::text, 'updated_at', now()::text)) r`, table),
		uuid.New(), tenantID, data)
	return err
}

// Unique scope end to end: a tenant-scoped unique field allows the same value in two tenants
// sharing the table and rejects it twice within one tenant; a global-scoped field rejects it
// across tenants. Also covers the upgrade path: a table whose unique index still has the old
// table-wide shape converges to the tenant-scoped shape in one corrective pass.
func TestUniqueScopeTenantAndGlobalOnASharedTable(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	entityName := "test.reconciler_unique_scope"
	bare := "test_reconciler_unique_scope"
	table := "entities." + bare
	dropReconcilerTableIfExists(t, pool, bare)
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, bare) })

	slug := plainField("slug", metadata.KindString)
	slug.Unique = boolp(true)
	host := plainField("host", metadata.KindString)
	host.Unique = boolp(true)
	global := metadata.UniqueScopeGlobal
	host.UniqueScope = &global
	def := testEntity(entityName, []metadata.EntityField{slug, host})

	if _, err := Reconcile(ctx, pool, uuid.Nil, def, nil); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}

	// Simulate a table reconciled before unique scopes existed: the slug index is table-wide.
	slugIdx := indexName(entityName, "slug", "uniq")
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP INDEX "entities".%q`, slugIdx)); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX %q ON %s (slug) WHERE deleted = false`, slugIdx, table)); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	if _, err := Reconcile(ctx, pool, uuid.Nil, def, nil); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	again, err := Reconcile(ctx, pool, uuid.Nil, def, nil)
	require.NoError(t, err)
	require.Equal(t, 0, again.OpsApplied, "must converge within 1 corrective pass, got %d ops", again.OpsApplied)

	tenantA, tenantB := uuid.New(), uuid.New()
	require.NoError(t, insertRawRow(ctx, pool, table, tenantA, `{"slug":"web","host":"a.example.com"}`))
	require.NoError(t, insertRawRow(ctx, pool, table, tenantB, `{"slug":"web","host":"b.example.com"}`), "same slug in another tenant must be allowed")
	err = insertRawRow(ctx, pool, table, tenantA, `{"slug":"web","host":"c.example.com"}`)
	require.Error(t, err, "same slug twice in one tenant must be rejected")
	err = insertRawRow(ctx, pool, table, tenantB, `{"slug":"api","host":"a.example.com"}`)
	require.Error(t, err, "global-scoped host must be rejected across tenants")

	var tenantIndexes int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE schemaname = 'entities' AND tablename = $1 AND indexname = $2`,
		bare, indexName(entityName, "tenant_id", "idx")).Scan(&tenantIndexes))
	require.Equal(t, 1, tenantIndexes, "tenant_id index must exist")
}

// The way back: after the move AND after data was dropped, RestoreLegacyData rebuilds data from the
// columns (including rows the new code wrote natively), recreates the old sync trigger, and a later
// MigrateLegacy copies forward again from scratch rather than trusting stale "done" marks.
func TestRestoreLegacyDataRebuildsDataAfterTheDropAndCanRollForwardAgain(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	sentinel := uuid.New()
	entityName, bare := "test.reconciler_legacy_restore", "test_reconciler_legacy_restore"
	table := "entities." + bare
	createLegacyTable(t, pool, bare)
	tenant := uuid.New()
	seedLegacyRows(t, pool, bare, tenant)
	def := legacyEntity(entityName)
	storage := metadata.StorageColumn
	for i := range def.Fields {
		if def.Fields[i].Name == "amount" {
			def.Fields[i].Storage = &storage // the old code kept a real, trigger-synced column for it
		}
	}

	_, err := MigrateLegacy(ctx, pool, sentinel, def, ScopeAllTenants)
	require.NoError(t, err)
	require.NoError(t, DropLegacyData(ctx, pool, sentinel, def))

	// The new code writes a row natively: columns only.
	_, err = pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (tenant_id, title, amount, done) VALUES ($1, 'native', 99.5, true)`, table), tenant)
	require.NoError(t, err)

	report, err := RestoreLegacyData(ctx, pool, sentinel, def, ScopeAllTenants)
	require.NoError(t, err)
	require.EqualValues(t, 3, report.Rows)
	for field, n := range report.MissingByField {
		require.Zero(t, n, "field %q has a column value missing from data", field)
	}

	var data string
	require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(`SELECT data::text FROM %s WHERE title = 'native'`, table)).Scan(&data))
	require.JSONEq(t, `{"title":"native","amount":99.5,"done":true}`, data)
	var richer string
	require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(`SELECT data::text FROM %s WHERE title = 'row 0'`, table)).Scan(&richer))
	require.Contains(t, richer, `"extra": {"a": [1, 2]}`, "a JSON field comes back whole")

	var triggers int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgrelid = $1::regclass AND NOT tgisinternal`, table).Scan(&triggers))
	require.Equal(t, 1, triggers, "the old sync trigger for the Storage: column field is back")

	// The old code now runs and writes data only; the trigger keeps its column in step.
	_, err = pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (tenant_id, data) VALUES ($1, '{"title":"old code","amount":"5.25"}'::jsonb)`, table), tenant)
	require.NoError(t, err)

	// Roll forward again: refused until migrated, then the old-code row reaches its columns.
	_, err = Reconcile(ctx, pool, sentinel, def, nil)
	require.ErrorIs(t, err, ErrLegacyDataColumn)
	_, err = MigrateLegacy(ctx, pool, sentinel, def, ScopeAllTenants)
	require.NoError(t, err)
	var title string
	var amount float64
	require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf(`SELECT title, amount::float8 FROM %s WHERE title = 'old code'`, table)).Scan(&title, &amount))
	require.Equal(t, 5.25, amount)
	require.NoError(t, DropLegacyData(ctx, pool, sentinel, def))
}

// A field whose kind changed no longer silently keeps its old column type.
func TestChangingAFieldsKindIsRefusedNotIgnored(t *testing.T) {
	pool := reconcilerTestPool(t)
	ctx := context.Background()
	entityName, bare := "test.reconciler_kind_change", "test_reconciler_kind_change"
	dropReconcilerTableIfExists(t, pool, bare)
	t.Cleanup(func() { dropReconcilerTableIfExists(t, pool, bare) })

	asText := testEntity(entityName, []metadata.EntityField{plainField("qty", metadata.KindString)})
	_, err := Reconcile(ctx, pool, uuid.New(), asText, nil)
	require.NoError(t, err)

	asNumber := testEntity(entityName, []metadata.EntityField{plainField("qty", metadata.KindNumber)})
	_, err = Reconcile(ctx, pool, uuid.New(), asNumber, nil)
	require.ErrorIs(t, err, ErrColumnTypeChanged)
	require.Contains(t, err.Error(), "qty")

	// With the explicit widen_type migration the type changes, and the same reconcile then converges.
	_, err = RunMigration(ctx, pool, uuid.New(), entityName, "entities."+bare,
		MigrationOp{Kind: MigWidenType, WidenField: "qty", WidenToSQLType: "double precision"}, QuarantinePolicy{Kind: PolicyBlock}, "kind-change")
	require.NoError(t, err)
	out, err := Reconcile(ctx, pool, uuid.New(), asNumber, nil)
	require.NoError(t, err)
	require.Zero(t, out.OpsApplied)
}
