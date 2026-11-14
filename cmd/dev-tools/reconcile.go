package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/reconciler"
)

// metadataEntityFile mirrors the subset of GET /metadata/entities/{entity}'s response this
// command needs — the "data" field unmarshals directly into metadata.EntityDefinition since the
// wire shape (name/label/fields/workflow/uniqueConstraints, all camelCase) already matches that
// struct's own JSON tags. listViews/version in the real response are ignored the same way the
// Rust original ignores them (Compile/Reconcile never consult either) — plain unknown-field
// tolerance in encoding/json, no explicit code needed.
type metadataEntityFile struct {
	Data metadata.EntityDefinition `json:"data"`
}

func loadEntityFromFile(path string) (metadata.EntityDefinition, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return metadata.EntityDefinition{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	var parsed metadataEntityFile
	if err := json.Unmarshal(contents, &parsed); err != nil {
		return metadata.EntityDefinition{}, fmt.Errorf("failed to parse %s as a GET /metadata/entities/<entity> response: %w", path, err)
	}
	return parsed.Data, nil
}

// enqueueReconcile seeds/bumps one (tenant, entity)'s desired version in
// reconciler_entity_deployments — the manual trigger for reconciler-orchestrator's ticker loop
// to pick up on its next poll. entityName must be a published low-code entity.
//
// Resolves the write through Router.PoolFor(tenantID), same as seedAdmin/createUser —
// reconciler_entity_deployments lives per physical database, so a Schema-strategy tenant's row
// belongs in the shared DATABASE_URL pool but a DedicatedDb tenant's belongs in that tenant's
// own database; writing straight to DATABASE_URL would have exactly that bug for any
// DedicatedDb tenant.
func enqueueReconcile(args []string) error {
	if len(args) < 5 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools enqueue-reconcile <tenantId> <entityName> <desiredVersion>")
		os.Exit(1)
	}
	ctx := context.Background()
	sharedPool, err := connectShared(ctx, 1)
	if err != nil {
		return err
	}
	defer sharedPool.Close()

	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	entityName := args[3]
	desiredVersion, err := strconv.ParseInt(args[4], 10, 64)
	if err != nil {
		return fmt.Errorf("desiredVersion must be an integer: %w", err)
	}

	router := routerFor(sharedPool)
	pool, err := router.PoolFor(ctx, tenantID)
	if err != nil {
		return err
	}
	if err := reconciler.EnqueueDeployment(ctx, pool, tenantID, entityName, desiredVersion); err != nil {
		return err
	}

	fmt.Printf("Enqueued entity %q for tenant %s at desired_version=%d — a running "+
		"reconciler-orchestrator will pick this up on its next poll.\n", entityName, tenantID, desiredVersion)
	return nil
}

// migrateToDedicatedTable moves an entity already living on the shared generic table (or
// another generic table passed as sourceTable) onto its own dedicated table, without losing any
// row. Thin CLI wrapper around reconciler.MigrateGenericToDedicated; all the actual logic lives
// there — this only resolves the tenant's database and reconstructs the EntityDefinition this
// binary has no business-entity knowledge of on its own.
//
// entityJsonPath is a local file holding exactly what GET /metadata/entities/{entity} returns —
// fetched *before* stopping the service, since after that the service (and therefore this
// endpoint) is down.
//
// The service owning this entity must already be stopped before running this — no dual-write/
// shadow-read, so a concurrent writer to sourceTable during the copy could still write a row
// after this command has already passed it by. This command does not check or enforce that
// itself; the caller owns getting the sequencing right.
func migrateToDedicatedTable(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools migrate-to-dedicated-table <tenantId> <entityJsonPath> [sourceTable]")
		fmt.Fprintln(os.Stderr, "  entityJsonPath: a local file holding exactly what GET /metadata/entities/<entity> returns, e.g.:")
		fmt.Fprintln(os.Stderr, "    curl -H \"Authorization: Bearer $TOKEN\" <baseUrl>/metadata/entities/<entity> -o entity.json")
		fmt.Fprintln(os.Stderr, "  fetched BEFORE stopping the service that owns this entity — that service must already be")
		fmt.Fprintln(os.Stderr, "  stopped by the time you run this command.")
		fmt.Fprintln(os.Stderr, "  sourceTable defaults to \"records\".")
		os.Exit(1)
	}
	sourceTable := argOr(args, 4, "records")

	ctx := context.Background()
	// Unlike this binary's other subcommands (all 1 connection), reconciler.Reconcile (called
	// via MigrateGenericToDedicated) holds one connection for its advisory lock for the whole
	// duration of execute while issuing further queries against the same pool for each DDL op —
	// a 1-connection pool deadlocks the moment the resolved tenant's strategy is Schema
	// (PoolFor returns this exact pool, not a fresh one).
	sharedPool, err := connectShared(ctx, 5)
	if err != nil {
		return err
	}
	defer sharedPool.Close()

	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	entityJsonPath := args[3]

	parsedEntity, err := loadEntityFromFile(entityJsonPath)
	if err != nil {
		return err
	}
	entity := parsedEntity
	entity.TableName = sourceTable
	entity.ListViews = nil
	entity.Audit = nil

	router := routerFor(sharedPool)
	pool, err := router.PoolFor(ctx, tenantID)
	if err != nil {
		return err
	}

	outcome, err := reconciler.MigrateGenericToDedicated(ctx, pool, tenantID, entity, sourceTable)
	if err != nil {
		return err
	}

	fmt.Printf("Migrated entity %q for tenant %s: %d row(s) copied from %q into %q "+
		"(%d DDL op(s) applied to build the target table).\n",
		entity.Name, tenantID, outcome.Copy.RowsScanned, sourceTable, outcome.Table, outcome.Reconcile.OpsApplied)
	fmt.Printf("Remaining steps this command does NOT do: update this entity's EntityDefinition.TableName in "+
		"source code to %q, recompile, and restart the service.\n", outcome.Table)
	return nil
}

// reconcilePlan prints the SQL a real Reconcile() for this (tenant, entity) would run, without
// executing any of it — the "let a dev review generated SQL and apply it themselves" mode,
// alongside (never instead of) the auto-apply every downstream service's boot sequence already
// relies on.
//
// entityJsonPath is the same shape migrateToDedicatedTable takes. Unlike that command,
// table_name isn't part of that endpoint's response (it's an internal reconciler detail, not
// public API shape), so it's a required 3rd argument here — pass the entity's own real,
// already-qualified table_name (e.g. "waf.ddos_policies"), not a generic default.
func reconcilePlan(args []string) error {
	failOnDestructive := false
	var kept []string
	for _, a := range args {
		if a == "--fail-on-destructive" {
			failOnDestructive = true
			continue
		}
		kept = append(kept, a)
	}
	args = kept
	if len(args) < 5 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools reconcile-plan <tenantId> <entityJsonPath> <tableName> [--fail-on-destructive]")
		fmt.Fprintln(os.Stderr, "  --fail-on-destructive: exit non-zero if the plan drops an FK, a unique constraint or a sync trigger,")
		fmt.Fprintln(os.Stderr, "    or if a field's kind no longer matches its column type — for a CI/review gate.")
		fmt.Fprintln(os.Stderr, "  entityJsonPath: same shape migrate-to-dedicated-table takes — a local file holding exactly")
		fmt.Fprintln(os.Stderr, "    what GET /metadata/entities/<entity> returns, e.g.:")
		fmt.Fprintln(os.Stderr, "    curl -H \"Authorization: Bearer $TOKEN\" <baseUrl>/metadata/entities/<entity> -o entity.json")
		fmt.Fprintln(os.Stderr, "  tableName: the entity's own real, already-qualified table_name, e.g. \"waf.ddos_policies\".")
		fmt.Fprintln(os.Stderr, "  Prints the SQL a real Reconcile() would run — nothing is executed. Review it, then apply")
		fmt.Fprintln(os.Stderr, "  by hand (psql or your own migration tool). The owning service's boot-time Reconcile() is")
		fmt.Fprintln(os.Stderr, "  completely unaffected by this and keeps auto-applying exactly as it always has.")
		os.Exit(1)
	}

	ctx := context.Background()
	sharedPool, err := connectShared(ctx, 5)
	if err != nil {
		return err
	}
	defer sharedPool.Close()

	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	entityJsonPath := args[3]
	tableName := args[4]

	parsedEntity, err := loadEntityFromFile(entityJsonPath)
	if err != nil {
		return err
	}
	entity := parsedEntity
	entity.TableName = tableName
	entity.ListViews = nil
	entity.Audit = nil

	router := routerFor(sharedPool)
	pool, err := router.PoolFor(ctx, tenantID)
	if err != nil {
		return err
	}

	desired, err := reconciler.Compile(entity)
	if err != nil {
		return err
	}
	actual, err := reconciler.Introspect(ctx, pool, tenantID, entity.Name, desired.Table)
	if err != nil {
		return err
	}
	planned := reconciler.Plan(desired, actual, nil)
	mismatches := reconciler.ColumnTypeMismatches(desired, actual)

	for _, m := range mismatches {
		fmt.Printf("-- TYPE CHANGE NOT APPLIED: column %q is %s but the field now wants %s. Reconcile refuses this; change it with the widen_type migration first. --\n", m.Column, m.Have, m.Want)
	}
	destructive := 0
	for _, p := range planned {
		if p.Destructive {
			destructive++
		}
	}
	gate := func() error {
		if failOnDestructive && (destructive > 0 || len(mismatches) > 0) {
			return fmt.Errorf("plan has %d destructive operation(s) and %d column type change(s); failing as requested (--fail-on-destructive)", destructive, len(mismatches))
		}
		return nil
	}
	if len(planned) == 0 {
		fmt.Printf("-- No changes: %q (table %q) already matches this EntityDefinition. --\n", entity.Name, desired.Table)
		return gate()
	}

	fmt.Printf("-- %d planned operation(s) for entity %q (tenant %s), table %q --\n", len(planned), entity.Name, tenantID, desired.Table)
	fmt.Println("-- Nothing has been executed — review, then apply by hand. --")
	for _, plannedOp := range planned {
		if len(plannedOp.SQL) == 0 {
			fmt.Printf("-- %v: checkpointed batch backfill, not a single SQL statement — run a real Reconcile() to apply it\n", plannedOp.Op)
			continue
		}
		if plannedOp.Destructive {
			fmt.Println("-- DESTRUCTIVE: removes a constraint, a uniqueness guarantee or a trigger --")
		}
		for _, stmt := range plannedOp.SQL {
			fmt.Printf("%s;\n", stmt)
		}
	}
	return gate()
}

// migrateToNativeColumns moves one entity's table from the pre-Phase-106 shape (every field inside a
// data jsonb column) to one typed column per field — docs/roadmap/106-native-entity-columns-no-data-jsonb.md,
// section 3. Thin wrapper over reconciler.MigrateLegacy / VerifyLegacy / DropLegacyData.
//
// Run it BEFORE deploying the code that reads native columns: a service built on this version refuses
// to reconcile a table that still has a data column (ErrLegacyDataColumn) rather than serve NULLs.
//
//	step 1 (always): add every field column, copy data -> column in resumable batches, drop the old
//	                 sync triggers. data stays, so the old code keeps working until cutover.
//	step 2 (always): print the verification report (rows, and per field the rows whose value is only
//	                 in data).
//	step 3 (--drop-data, only if step 2 is clean): drop the data column. Irreversible: snapshot first.
//
// --all-tenants: the table is shared by many tenants (a Schema-strategy table), so tenantId is only the
// progress-ledger key (use the platform sentinel) and the copy covers every row. Without it the copy is
// limited to tenantId's own rows (a DedicatedDb tenant's table).
func migrateToNativeColumns(args []string) error {
	var positional []string
	allTenants, dropData := false, false
	for _, a := range args[2:] {
		switch a {
		case "--all-tenants":
			allTenants = true
		case "--drop-data":
			dropData = true
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 3 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools migrate-to-native-columns <tenantId> <entityJsonPath> <tableName> [--all-tenants] [--drop-data]")
		fmt.Fprintln(os.Stderr, "  entityJsonPath: a local file holding what GET /metadata/entities/<entity> returns.")
		fmt.Fprintln(os.Stderr, "  tableName: the entity's real, already-qualified table_name, e.g. \"waf.ddos_policies\".")
		fmt.Fprintln(os.Stderr, "  Snapshot the database first. Preview the DDL with `dev-tools reconcile-plan` if you want to read it.")
		os.Exit(1)
	}

	ctx := context.Background()
	sharedPool, err := connectShared(ctx, 5)
	if err != nil {
		return err
	}
	defer sharedPool.Close()

	tenantID, err := parseUUID(positional[0])
	if err != nil {
		return err
	}
	entity, err := loadEntityFromFile(positional[1])
	if err != nil {
		return err
	}
	entity.TableName = positional[2]
	entity.ListViews = nil
	entity.Audit = nil

	pool, err := routerFor(sharedPool).PoolFor(ctx, tenantID)
	if err != nil {
		return err
	}

	scope := reconciler.ScopeSingleTenant
	if allTenants {
		scope = reconciler.ScopeAllTenants
	}
	outcome, err := reconciler.MigrateLegacy(ctx, pool, tenantID, entity, scope)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %d DDL/backfill op(s) applied.\n", entity.TableName, outcome.OpsApplied)

	report, err := reconciler.VerifyLegacy(ctx, pool, tenantID, entity)
	if err != nil {
		return err
	}
	fmt.Printf("rows: %d, data column still present: %v, sync triggers left: %v\n", report.Rows, report.StillHasLegacyData, report.SyncTriggersLeft)
	for field, n := range report.MissingByField {
		if n > 0 {
			fmt.Printf("  field %q: %d row(s) have a value only in data\n", field, n)
		}
	}
	if !report.Ok() {
		return fmt.Errorf("verification is not clean; not dropping data. Fix the cause (a --all-tenants run for a shared table, a value that does not fit its column) and run again")
	}
	if !dropData {
		fmt.Println("Verification is clean. Re-run with --drop-data to drop the data column (irreversible).")
		return nil
	}
	if err := reconciler.DropLegacyData(ctx, pool, tenantID, entity); err != nil {
		return err
	}
	fmt.Println("data column dropped; the table is native.")
	return nil
}

// restoreLegacyData is the way back from migrate-to-native-columns: it rebuilds the data jsonb column
// from the native columns (adding the column back if --drop-data already removed it) and recreates the
// old sync triggers, so the previous release of the application can run again. Run it with the new
// release stopped; rows the new release wrote are included. A later migrate-to-native-columns copies
// forward again from scratch.
func restoreLegacyData(args []string) error {
	var positional []string
	allTenants := false
	for _, a := range args[2:] {
		if a == "--all-tenants" {
			allTenants = true
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) != 3 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools restore-legacy-data <tenantId> <entityJsonPath> <tableName> [--all-tenants]")
		fmt.Fprintln(os.Stderr, "  Stop the new release first. Use the same flags you gave migrate-to-native-columns.")
		os.Exit(1)
	}
	ctx := context.Background()
	sharedPool, err := connectShared(ctx, 5)
	if err != nil {
		return err
	}
	defer sharedPool.Close()
	tenantID, err := parseUUID(positional[0])
	if err != nil {
		return err
	}
	entity, err := loadEntityFromFile(positional[1])
	if err != nil {
		return err
	}
	entity.TableName = positional[2]
	entity.ListViews = nil
	entity.Audit = nil
	pool, err := routerFor(sharedPool).PoolFor(ctx, tenantID)
	if err != nil {
		return err
	}
	scope := reconciler.ScopeSingleTenant
	if allTenants {
		scope = reconciler.ScopeAllTenants
	}
	report, err := reconciler.RestoreLegacyData(ctx, pool, tenantID, entity, scope)
	if err != nil {
		return err
	}
	fmt.Printf("%s: data rebuilt for %d row(s).\n", entity.TableName, report.Rows)
	bad := false
	for field, n := range report.MissingByField {
		if n > 0 {
			bad = true
			fmt.Printf("  field %q: %d row(s) have a column value that data lacks\n", field, n)
		}
	}
	if bad {
		return fmt.Errorf("restore is not clean; do not start the old release yet")
	}
	fmt.Println("Restore is clean. The previous release can be started.")
	return nil
}
