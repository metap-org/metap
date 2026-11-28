// The schema is changed by a person running `<service> migrate`, never by a service booting. This
// file is that command and the check that keeps a service from serving a database it does not match.
// The workflow is docs/migrations.md.

package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/metap-org/metap/migrations"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/db"
	dbmigrate "github.com/metap-org/metap/pkg/db/migrate"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/reconciler"
)

// snapshotFile sits beside the generated migrations.
const snapshotFile = "schema.snapshot.json"

// storedEntities are the entities of this service that have tables.
func storedEntities(opts Options, registry *metadata.MetadataRegistry) []metadata.EntityDefinition {
	if len(opts.Entities) > 0 {
		return opts.Entities
	}
	return registry.Definitions()
}

func (o Options) product() dbmigrate.Product {
	return dbmigrate.Product{Name: o.Name, FS: o.Migrations}
}

// ErrSchemaNotReady: the database lacks something this build needs; nothing was changed.
var ErrSchemaNotReady = errors.New("the database schema is not ready for this build")

type schemaReport struct {
	// problems stop the service; warnings do not.
	problems, warnings []string
}

// inspectSchema reads where the database stands: the platform's migrations, the product's, and
// then every entity's real table. It changes nothing.
func inspectSchema(ctx context.Context, pool *pgxpool.Pool, opts Options, entities []metadata.EntityDefinition) (schemaReport, error) {
	var r schemaReport
	platform, err := dbmigrate.PlatformState(ctx, pool, migrations.FS)
	if err != nil {
		return r, fmt.Errorf("platform migrations: %w", err)
	}
	for _, m := range platform.Pending {
		r.problems = append(r.problems, "platform migration not applied: "+m.Filename)
	}
	if len(platform.Unknown) > 0 {
		r.warnings = append(r.warnings, fmt.Sprintf("the database has platform migrations this build does not: %v", platform.Unknown))
	}
	product, err := opts.product().State(ctx, pool)
	if err != nil {
		return r, fmt.Errorf("%s migrations: %w", opts.Name, err)
	}
	for _, m := range product.Pending {
		if m.Version == product.Partial.Version {
			r.problems = append(r.problems, fmt.Sprintf("migration stopped part-way: %s (%d of %d statements ran)", m.Filename, product.PartialDone, len(dbmigrate.Statements(m.SQL))))
			continue
		}
		r.problems = append(r.problems, "migration not applied: "+m.Filename)
	}
	if len(product.Unknown) > 0 {
		r.warnings = append(r.warnings, fmt.Sprintf("the database has %s migrations this build does not: %v", opts.Name, product.Unknown))
	}
	// Reading the tables themselves also catches an entity changed without a migration, and an
	// object dropped behind the ledger's back.
	for _, entity := range entities {
		gap, err := reconciler.Check(ctx, pool, entity)
		if err != nil {
			return r, fmt.Errorf("checking %s: %w", entity.Name, err)
		}
		for _, m := range gap.Missing {
			r.problems = append(r.problems, entity.Name+": missing "+m)
		}
		for _, e := range gap.Extra {
			r.warnings = append(r.warnings, entity.Name+": the database also has "+e)
		}
	}
	return r, nil
}

// requireSchema is the boot check: it logs the warnings, and with a problem returns
// ErrSchemaNotReady saying what is missing and what to run.
func requireSchema(ctx context.Context, pool *pgxpool.Pool, opts Options, entities []metadata.EntityDefinition) error {
	r, err := inspectSchema(ctx, pool, opts, entities)
	if err != nil {
		return err
	}
	for _, w := range r.warnings {
		slog.Warn("database schema: " + w)
	}
	if len(r.problems) == 0 {
		return nil
	}
	shown := r.problems
	if len(shown) > 20 {
		shown = append(append([]string(nil), shown[:20]...), fmt.Sprintf("... and %d more (see `migrate status`)", len(r.problems)-20))
	}
	return fmt.Errorf("%w (%s does not migrate on its own):\n  - %s\n"+
		"Run `<this binary> migrate status`, then `<this binary> migrate up`.\n"+
		"If an entity changed and no migration is pending, write one first: `migrate generate <name>`, review it, commit, rebuild.\n"+
		"A database whose tables this service used to create at boot is adopted once with `migrate baseline`.",
		ErrSchemaNotReady, opts.Name, strings.Join(shown, "\n  - "))
}

const migrateUsage = `Usage: <service> migrate <command>

  status                     what is applied, pending and missing (reads only)
  generate <name>            write the next migration for the entities' changes (no database)
      --check                fail if the entities need a migration that is not written (for CI)
      --rename table:old=new rename a column instead of adding one (repeatable)
      --allow-type-change    write ALTER COLUMN TYPE for a field whose kind changed
      --dir <dir>            where the migration files are
  new <name>                 write an empty up/down pair for a change written by hand
      --dir <dir>
  up                         apply the platform's pending migrations, then this service's
      --dry-run              print the SQL instead
  down                       undo this service's newest migration
      --steps N              undo N of them
      --platform             undo platform migrations instead
      --yes                  really run it; without this the SQL is only printed
  baseline                   adopt a database that already has the tables: record this
                             service's migrations as applied after checking nothing is missing
      --yes                  really record; without this it only checks

DATABASE_URL (or CONFIG_FILE) names the database. generate and new need none.`

// errNotConfirmed stops a rollback that was only asked to show itself.
var errNotConfirmed = errors.New("nothing was run: repeat with --yes to run the SQL above")

// MigrateCommand runs one `migrate` command; args are what follows the word. app.Main calls it,
// so every service has the command.
func MigrateCommand(ctx context.Context, opts Options, args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(out, migrateUsage)
		return errors.New("a migrate command is required")
	}
	command, rest := args[0], args[1:]
	flags := flag.NewFlagSet("migrate "+command, flag.ContinueOnError)
	flags.SetOutput(out)
	var renames renameFlags
	dir := flags.String("dir", opts.MigrationsDir, "migration files")
	check := flags.Bool("check", false, "")
	allowType := flags.Bool("allow-type-change", false, "")
	dryRun := flags.Bool("dry-run", false, "")
	steps := flags.Int("steps", 1, "")
	platform := flags.Bool("platform", false, "")
	yes := flags.Bool("yes", false, "")
	flags.Var(&renames, "rename", "")
	// The name may come before the flags.
	var name string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		name, rest = rest[0], rest[1:]
	}
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	registry := metadata.NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		return fmt.Errorf("registering entities: %w", err)
	}
	if err := registry.ValidateReferences(); err != nil {
		return fmt.Errorf("validating entity references: %w", err)
	}
	entities := storedEntities(opts, registry)

	switch command {
	case "generate":
		return generateMigration(*dir, name, entities, reconciler.GenerateOptions{Renames: renames, AllowTypeChange: *allowType}, *check, out)
	case "new":
		return newMigration(*dir, name, out)
	case "status", "up", "down", "baseline":
	default:
		fmt.Fprintln(out, migrateUsage)
		return fmt.Errorf("unknown migrate command %q", command)
	}

	_ = godotenv.Load()
	if err := config.LoadFileFromEnv(); err != nil {
		return err
	}
	databaseURL, err := env.Require("DATABASE_URL")
	if err != nil {
		return err
	}
	pool, err := db.ConnectForMigrate(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	switch command {
	case "status":
		r, err := inspectSchema(ctx, pool, opts, entities)
		if err != nil {
			return err
		}
		for _, w := range r.warnings {
			fmt.Fprintln(out, "warning: "+w)
		}
		if len(r.problems) == 0 {
			fmt.Fprintln(out, "up to date: the database has everything this build needs")
			return nil
		}
		for _, p := range r.problems {
			fmt.Fprintln(out, p)
		}
		return ErrSchemaNotReady
	case "up":
		return migrateUp(ctx, pool, opts, *dryRun, out)
	case "down":
		show := func(targets []dbmigrate.Migration) error {
			for _, m := range targets {
				fmt.Fprintf(out, "-- ===== %s =====\n%s\n", m.DownFilename, strings.TrimSpace(m.Down))
			}
			if !*yes {
				return errNotConfirmed
			}
			return nil
		}
		var done []int64
		if *platform {
			done, err = dbmigrate.Rollback(ctx, pool, migrations.FS, *steps, show)
		} else {
			done, err = opts.product().Down(ctx, pool, *steps, show)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "rolled back: %v\n", done)
		return nil
	default: // baseline
		verify := func(ctx context.Context) error {
			state, err := dbmigrate.PlatformState(ctx, pool, migrations.FS)
			if err != nil {
				return err
			}
			if len(state.Pending) > 0 {
				return fmt.Errorf("%d platform migrations are not applied: apply them first", len(state.Pending))
			}
			var missing []string
			for _, entity := range entities {
				gap, err := reconciler.Check(ctx, pool, entity)
				if err != nil {
					return err
				}
				for _, m := range gap.Missing {
					missing = append(missing, entity.Name+": "+m)
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("the database does not have everything, so its migrations cannot be recorded as applied:\n  - %s", strings.Join(missing, "\n  - "))
			}
			if !*yes {
				return errors.New("the database has every table, column, index and constraint. Nothing was recorded: repeat with --yes")
			}
			return nil
		}
		recorded, err := opts.product().Baseline(ctx, pool, verify)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "recorded as applied without running: %v\n", recorded)
		return nil
	}
}

func migrateUp(ctx context.Context, pool *pgxpool.Pool, opts Options, dryRun bool, out io.Writer) error {
	if dryRun {
		platform, err := dbmigrate.PlatformState(ctx, pool, migrations.FS)
		if err != nil {
			return err
		}
		product, err := opts.product().State(ctx, pool)
		if err != nil {
			return err
		}
		for _, m := range append(platform.Pending, product.Pending...) {
			fmt.Fprintf(out, "-- ===== %s =====\n%s\n", m.Filename, strings.TrimSpace(m.SQL))
		}
		if len(platform.Pending)+len(product.Pending) == 0 {
			fmt.Fprintln(out, "-- nothing is pending")
		}
		return nil
	}
	platform, err := dbmigrate.RunFS(ctx, pool, migrations.FS)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "platform: applied %v\n", platform.Applied)
	applied, err := opts.product().Up(ctx, pool)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: applied %v\n", opts.Name, applied)
	return nil
}

type renameFlags []reconciler.Rename

func (f *renameFlags) String() string { return "" }

func (f *renameFlags) Set(v string) error {
	table, pair, ok := strings.Cut(v, ":")
	from, to, ok2 := strings.Cut(pair, "=")
	if !ok || !ok2 || table == "" || from == "" || to == "" {
		return fmt.Errorf("want table:old=new, got %q", v)
	}
	*f = append(*f, reconciler.Rename{Table: table, From: from, To: to})
	return nil
}

var migrationName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,80}$`)

func migrationDir(dir, name string, needName bool) error {
	if dir == "" {
		return errors.New("no migrations directory: set app.Options.MigrationsDir or pass --dir")
	}
	if needName && !migrationName.MatchString(name) {
		return fmt.Errorf("a migration name is lower-case letters, digits and _, got %q", name)
	}
	return nil
}

func nextMigration(dir string) (int64, error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return 1, nil
	}
	existing, err := dbmigrate.LoadMigrations(dir)
	if err != nil {
		return 0, err
	}
	if len(existing) == 0 {
		return 1, nil
	}
	return existing[len(existing)-1].Version + 1, nil
}

func readSnapshot(dir string) (reconciler.Snapshot, error) {
	var s reconciler.Snapshot
	raw, err := os.ReadFile(filepath.Join(dir, snapshotFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%s: %w", snapshotFile, err)
	}
	return s, nil
}

func render(header string, statements []string, noTransaction bool) string {
	var b strings.Builder
	b.WriteString(header)
	if noTransaction {
		b.WriteString(dbmigrate.NoTransactionDirective + "\n")
	}
	for i, s := range statements {
		if noTransaction && i > 0 {
			b.WriteString("\n" + dbmigrate.StatementBreak)
		}
		b.WriteString("\n" + s + ";\n")
	}
	return b.String()
}

// writeNew creates a file and refuses to replace one.
func writeNew(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writePair(dir string, version int64, name, up, down string, out io.Writer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for suffix, content := range map[string]string{"up": up, "down": down} {
		path := filepath.Join(dir, fmt.Sprintf("%04d_%s.%s.sql", version, name, suffix))
		if err := writeNew(path, content); err != nil {
			return err
		}
		fmt.Fprintln(out, "wrote "+path)
	}
	return nil
}

func generateMigration(dir, name string, entities []metadata.EntityDefinition, opts reconciler.GenerateOptions, check bool, out io.Writer) error {
	if err := migrationDir(dir, name, !check); err != nil {
		return err
	}
	previous, err := readSnapshot(dir)
	if err != nil {
		return err
	}
	script, err := reconciler.Generate(entities, previous, opts)
	if err != nil {
		return err
	}
	if script.Empty() {
		fmt.Fprintln(out, "no changes: the migrations already cover the entities")
		return nil
	}
	if check {
		return fmt.Errorf("the entities need a migration that is not written (%d statements): run `migrate generate <name>`", len(script.Up))
	}
	version, err := nextMigration(dir)
	if err != nil {
		return err
	}
	header := "-- Generated by `migrate generate`. Review it. It must not change once it has been applied anywhere.\n"
	if err := writePair(dir, version, name,
		render(header, script.Up, script.UpNoTransaction),
		render(header+"-- Undoes the .up.sql of the same number.\n", script.Down, script.DownNoTransaction), out); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(script.Next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, snapshotFile), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(out, "updated "+filepath.Join(dir, snapshotFile)+"; review the SQL, commit the three files, rebuild, then `migrate up`")
	return nil
}

func newMigration(dir, name string, out io.Writer) error {
	if err := migrationDir(dir, name, true); err != nil {
		return err
	}
	version, err := nextMigration(dir)
	if err != nil {
		return err
	}
	return writePair(dir, version, name,
		"-- Written by hand. To run outside a transaction add the line \""+dbmigrate.NoTransactionDirective+"\" and\n-- separate statements with \""+dbmigrate.StatementBreak+"\".\n",
		"-- Undoes the .up.sql of the same number. A file left without a statement cannot be rolled back.\n", out)
}
