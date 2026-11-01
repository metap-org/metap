package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/reconciler"
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
	if len(args) < 5 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools reconcile-plan <tenantId> <entityJsonPath> <tableName>")
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

	if len(planned) == 0 {
		fmt.Printf("-- No changes: %q (table %q) already matches this EntityDefinition. --\n", entity.Name, desired.Table)
		return nil
	}

	fmt.Printf("-- %d planned operation(s) for entity %q (tenant %s), table %q --\n", len(planned), entity.Name, tenantID, desired.Table)
	fmt.Println("-- Nothing has been executed — review, then apply by hand. --")
	for _, plannedOp := range planned {
		if len(plannedOp.SQL) == 0 {
			fmt.Printf("-- %v: checkpointed batch backfill, not a single SQL statement — run a real Reconcile() to apply it\n", plannedOp.Op)
			continue
		}
		for _, stmt := range plannedOp.SQL {
			fmt.Printf("%s;\n", stmt)
		}
	}
	return nil
}
