//go:build integration

package app

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/migrations"
	"github.com/metap-org/metap/pkg/db/migrate"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/reconciler"
	"github.com/metap-org/metap/pkg/testkit"
)

func yes() *bool { v := true; return &v }

func field(name string, kind metadata.FieldKind) metadata.EntityField {
	return metadata.EntityField{Name: name, Label: name, Kind: kind}
}

// Two entities, the child declared first: generation must still create the parent's table before
// the foreign key that points at it.
func shop(itemFields []metadata.EntityField) []metadata.EntityDefinition {
	parent := "mig.shops"
	ref := field("shopId", metadata.KindReference)
	ref.RefEntity = &parent
	return []metadata.EntityDefinition{
		{Name: "mig.items", Label: "Item", TableName: "migt.mig_items", Fields: append([]metadata.EntityField{ref}, itemFields...)},
		{Name: parent, Label: "Shop", TableName: "migt.mig_shops", Fields: []metadata.EntityField{field("name", metadata.KindString)}},
	}
}

func firstItemFields() []metadata.EntityField {
	sku := field("sku", metadata.KindString)
	sku.Unique = yes()
	title := field("title", metadata.KindString)
	title.Searchable = yes()
	return []metadata.EntityField{sku, title, field("qty", metadata.KindString)}
}

// secondItemFields adds a column and an index, renames title, drops sku's uniqueness and turns
// qty into a number: every kind of change on a table that already has rows.
func secondItemFields() []metadata.EntityField {
	sku := field("sku", metadata.KindString)
	sku.Indexed = yes()
	name := field("name", metadata.KindString)
	name.Searchable = yes()
	colour := field("colour", metadata.KindString)
	colour.Indexed = yes()
	return []metadata.EntityField{sku, name, field("qty", metadata.KindNumber), colour}
}

func serviceOptions(dir string, entities []metadata.EntityDefinition) Options {
	return Options{Name: "mig-service", Entities: entities, Migrations: os.DirFS(dir), MigrationsDir: dir}
}

func generate(t *testing.T, dir, name string, entities []metadata.EntityDefinition, opts reconciler.GenerateOptions) {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, generateMigration(dir, name, entities, opts, false, &out), out.String())
}

func problems(t *testing.T, pool *pgxpool.Pool, opts Options) []string {
	t.Helper()
	r, err := inspectSchema(context.Background(), pool, opts, opts.Entities)
	require.NoError(t, err)
	return r.problems
}

func columns(t *testing.T, pool *pgxpool.Pool, table string) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'migt' AND table_name = $1", table)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, ty string
		require.NoError(t, rows.Scan(&n, &ty))
		out[n] = ty
	}
	require.NoError(t, rows.Err())
	return out
}

func indexes(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	var names []string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT coalesce(array_agg(indexname || ' ' || indexdef ORDER BY indexname), '{}') FROM pg_indexes WHERE schemaname = 'migt'`).Scan(&names))
	return names
}

func TestAFreshDatabaseTakesTheGeneratedSQLAndGivesItBack(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	dir := t.TempDir()
	entities := shop(firstItemFields())
	opts := serviceOptions(dir, entities)

	err := requireSchema(ctx, pool, opts, entities)
	require.ErrorIs(t, err, ErrSchemaNotReady, "an entity with no table and no migration stops the boot")
	assert.Contains(t, err.Error(), "mig.items: missing table migt.mig_items")
	assert.Contains(t, err.Error(), "migrate generate")
	assert.Empty(t, columns(t, pool, "mig_items"), "the boot check changes nothing")

	generate(t, dir, "init", entities, reconciler.GenerateOptions{})
	up, err := os.ReadFile(filepath.Join(dir, "0001_init.up.sql"))
	require.NoError(t, err)
	assert.False(t, migrate.NoTransaction(string(up)), "new tables are created in one transaction")
	assert.NotContains(t, string(up), "CONCURRENTLY")

	assert.Contains(t, problems(t, pool, opts), "migration not applied: 0001_init.up.sql")
	require.NoError(t, migrateUp(ctx, pool, opts, false, &bytes.Buffer{}))
	require.NoError(t, requireSchema(ctx, pool, opts, entities))
	for _, e := range entities {
		out, err := reconciler.Reconcile(ctx, pool, uuid.Nil, e, nil)
		require.NoError(t, err)
		assert.Zero(t, out.OpsApplied, "%s: the files leave exactly what reconcile would", e.Name)
	}

	var again bytes.Buffer
	require.NoError(t, generateMigration(dir, "again", entities, reconciler.GenerateOptions{}, true, &again))
	assert.Contains(t, again.String(), "no changes")

	_, err = opts.product().Down(ctx, pool, 1, nil)
	require.NoError(t, err)
	assert.Empty(t, columns(t, pool, "mig_items"))
	assert.Empty(t, columns(t, pool, "mig_shops"))
	assert.Contains(t, problems(t, pool, opts), "migration not applied: 0001_init.up.sql")
	require.NoError(t, migrateUp(ctx, pool, opts, false, &bytes.Buffer{}), "up again after down")
	require.NoError(t, requireSchema(ctx, pool, opts, entities))
}

func TestAChangeToALiveTableAppliesAndRollsBackKeepingItsRows(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	dir := t.TempDir()
	before := shop(firstItemFields())
	generate(t, dir, "init", before, reconciler.GenerateOptions{})
	require.NoError(t, migrateUp(ctx, pool, serviceOptions(dir, before), false, &bytes.Buffer{}))
	_, err := pool.Exec(ctx, `INSERT INTO migt.mig_items (tenant_id, sku, title, qty) VALUES (gen_random_uuid(), 'a', 'first', '7')`)
	require.NoError(t, err)
	columnsBefore, indexesBefore := columns(t, pool, "mig_items"), indexes(t, pool)

	after := shop(secondItemFields())
	var out bytes.Buffer
	require.ErrorIs(t, generateMigration(dir, "change", after, reconciler.GenerateOptions{}, false, &out), reconciler.ErrColumnTypeChanged)
	generate(t, dir, "change", after, reconciler.GenerateOptions{AllowTypeChange: true,
		Renames: []reconciler.Rename{{Table: "migt.mig_items", From: "title", To: "name"}}})
	opts := serviceOptions(dir, after)
	up, err := os.ReadFile(filepath.Join(dir, "0002_change.up.sql"))
	require.NoError(t, err)
	assert.True(t, migrate.NoTransaction(string(up)), "indexes on a live table are built concurrently")

	require.ErrorIs(t, requireSchema(ctx, pool, opts, after), ErrSchemaNotReady)
	require.NoError(t, migrateUp(ctx, pool, opts, false, &bytes.Buffer{}), string(up))
	require.NoError(t, requireSchema(ctx, pool, opts, after))
	out2, err := reconciler.Reconcile(ctx, pool, uuid.Nil, after[0], nil)
	require.NoError(t, err)
	assert.Zero(t, out2.OpsApplied)
	var name string
	var qty float64
	require.NoError(t, pool.QueryRow(ctx, "SELECT name, qty FROM migt.mig_items WHERE sku = 'a'").Scan(&name, &qty))
	assert.Equal(t, "first", name)
	assert.EqualValues(t, 7, qty)

	// The previous build against the newer database: what it needs and no longer finds stops it.
	assert.NotEmpty(t, problems(t, pool, serviceOptions(dir, before)))

	// A rollback that stops part-way (here: a view blocks changing the type back) leaves the
	// migration marked part-way, and runs again once the cause is gone.
	_, err = pool.Exec(ctx, "CREATE VIEW migt.blocker AS SELECT qty FROM migt.mig_items")
	require.NoError(t, err)
	_, err = opts.product().Down(ctx, pool, 1, nil)
	require.ErrorContains(t, err, "rolling back 0002_change.up.sql")
	state, err := opts.product().State(ctx, pool)
	require.NoError(t, err)
	require.Len(t, state.Pending, 1, "no longer counted as applied")
	require.ErrorIs(t, requireSchema(ctx, pool, opts, after), ErrSchemaNotReady)
	_, err = pool.Exec(ctx, "DROP VIEW migt.blocker")
	require.NoError(t, err)

	_, err = opts.product().Down(ctx, pool, 1, nil)
	require.NoError(t, err)
	assert.Equal(t, columnsBefore, columns(t, pool, "mig_items"))
	assert.Equal(t, indexesBefore, indexes(t, pool))
	var title, qtyText string
	require.NoError(t, pool.QueryRow(ctx, "SELECT title, qty FROM migt.mig_items WHERE sku = 'a'").Scan(&title, &qtyText))
	assert.Equal(t, "first", title)
	previous := serviceOptions(dir, before)
	previous.Migrations = only(t, dir, "0001_")
	require.NoError(t, requireSchema(ctx, pool, previous, before), "the previous build runs on the rolled-back database")

	require.NoError(t, migrateUp(ctx, pool, opts, false, &bytes.Buffer{}), "and the change applies again")
	require.NoError(t, requireSchema(ctx, pool, opts, after))
}

func TestAMigrationOutsideATransactionResumesWhereItStopped(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	files := fstest.MapFS{
		"0001_two.up.sql": {Data: []byte(migrate.NoTransactionDirective + "\nCREATE TABLE public.mig_a (id int);\n" +
			migrate.StatementBreak + "\nINSERT INTO public.mig_a SELECT id FROM public.mig_source;\n")},
		"0001_two.down.sql": {Data: []byte("DROP TABLE IF EXISTS public.mig_a;\n")},
	}
	product := migrate.Product{Name: "mig-resume", FS: files}

	_, err := product.Up(ctx, pool)
	require.ErrorContains(t, err, "statement 2 of 2")
	state, err := product.State(ctx, pool)
	require.NoError(t, err)
	require.Len(t, state.Pending, 1)
	assert.Equal(t, 1, state.PartialDone)

	_, err = pool.Exec(ctx, "CREATE TABLE public.mig_source (id int); INSERT INTO public.mig_source VALUES (1)")
	require.NoError(t, err)
	applied, err := product.Up(ctx, pool)
	require.NoError(t, err, "statement 1 would fail if it ran again")
	assert.Equal(t, []int64{1}, applied)
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM public.mig_a").Scan(&n))
	assert.Equal(t, 1, n)

	files["0001_two.up.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE public.mig_a (id bigint);")}
	_, err = product.State(ctx, pool)
	assert.ErrorContains(t, err, "checksum mismatch", "an applied file must not change")
}

func TestAFailedTransactionalMigrationLeavesNothing(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	product := migrate.Product{Name: "mig-atomic", FS: fstest.MapFS{
		"0001_bad.up.sql": {Data: []byte("CREATE TABLE public.mig_kept (id int);\nSELECT * FROM public.does_not_exist;")},
	}}
	_, err := product.Up(ctx, pool)
	require.Error(t, err)
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('public.mig_kept') IS NOT NULL").Scan(&exists))
	assert.False(t, exists)
	_, err = product.Down(ctx, pool, 1, nil)
	assert.NoError(t, err, "nothing is recorded, so there is nothing to undo")
}

func TestADatabaseReconciledAtBootIsAdoptedByBaseline(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	dir := t.TempDir()
	entities := shop(firstItemFields())
	for _, e := range []metadata.EntityDefinition{entities[1], entities[0]} {
		_, err := reconciler.Reconcile(ctx, pool, uuid.Nil, e, nil)
		require.NoError(t, err)
	}
	generate(t, dir, "init", entities, reconciler.GenerateOptions{})
	opts := serviceOptions(dir, entities)
	require.ErrorIs(t, requireSchema(ctx, pool, opts, entities), ErrSchemaNotReady, "the tables exist but nothing is recorded")

	_, err := pool.Exec(ctx, "ALTER TABLE migt.mig_items DROP COLUMN qty")
	require.NoError(t, err)
	check := func(context.Context) error {
		if p := problems(t, pool, opts); strings.Contains(strings.Join(p, "\n"), "missing") {
			return ErrSchemaNotReady
		}
		return nil
	}
	_, err = opts.product().Baseline(ctx, pool, check)
	require.ErrorIs(t, err, ErrSchemaNotReady, "a database that lacks something cannot be adopted")

	_, err = pool.Exec(ctx, "ALTER TABLE migt.mig_items ADD COLUMN qty text")
	require.NoError(t, err)
	recorded, err := opts.product().Baseline(ctx, pool, check)
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, recorded)
	require.NoError(t, requireSchema(ctx, pool, opts, entities))
}

func TestPlatformMigrationsUpToTheBaselineCannotBeRolledBack(t *testing.T) {
	pool := testkit.NewPostgres(t)
	_, err := migrate.Rollback(context.Background(), pool, platformFiles(), 1, nil)
	require.ErrorContains(t, err, "has no down script")
}

func platformFiles() fs.FS { return migrations.FS }

// only is the files of dir a build from before the later ones would embed.
func only(t *testing.T, dir, prefix string) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, err)
			out[e.Name()] = &fstest.MapFile{Data: raw}
		}
	}
	return out
}
