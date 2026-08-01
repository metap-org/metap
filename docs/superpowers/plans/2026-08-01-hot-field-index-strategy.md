# Hot Field Index Strategy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically create per-entity partial Postgres indexes for any metadata field declared `indexed: true` or `unique: true`, so filter/sort/uniqueness on hot JSONB fields stop doing unindexed `jsonb_extract_path_text` scans.

**Architecture:** A new `IndexReconciler` (mirrors the existing `MetadataDriftService`) reads registered entities' field metadata and issues `CREATE INDEX CONCURRENTLY IF NOT EXISTS` statements scoped per-entity via a `WHERE entity = '...'` predicate — no physical columns added to the shared `records` table. Wired into `container.ts`/`buildApp` for automatic best-effort reconciliation at every boot, plus a standalone script for manual/ops use.

**Tech Stack:** TypeScript, Drizzle ORM (`sql` template + `sql.identifier`), PostgreSQL, Vitest (live-DB tests).

## Global Constraints

- `IndexReconciler` must never be handed anything derived from a request — entity/field names come only from server-authored, `MetadataCompiler`-validated metadata (spec's load-bearing invariant).
- `CREATE INDEX CONCURRENTLY` cannot run inside a transaction block — every statement is its own standalone `db.client.execute(...)` call, never inside `db.client.transaction(...)`.
- Reconciliation is best-effort: any DB error is caught and logged via `warn`, never thrown — mirrors `MetadataDriftService`'s and `HealthService`'s existing graceful-degradation stance.
- Single-field indexes only. No composite indexes, no GIN/full-text indexes (sub-project 2), no dropping indexes when a field's flag is removed, no touching `records.code`/`records.status`'s existing physical columns — all explicitly out of scope per the spec.
- Minimal, targeted tests only — no exhaustive matrix, per this project's established test-scope convention.

**Deviation from the spec's illustrative file path, decided during planning:** the spec suggested `scripts/reconcile-indexes.mjs` "following the `scripts/seed-admin.mjs` pattern." On inspection, `seed-admin.mjs` is a raw-SQL script with no import from `src/` — and `tsconfig.json`'s `include` is `["src", "drizzle.config.ts"]` only, so a file under `scripts/` importing `IndexReconciler` from `src/core/metadata/` would run fine under `tsx` but never be typechecked by `pnpm typecheck`. `src/workers/outbox-publisher.ts` is the actual matching precedent in this codebase: a standalone process that calls `createContainer`, does one thing, and closes — and it lives under `src/`, so it's typechecked and linted like the rest of the code. Task 3 below places the new script at `src/workers/reconcile-indexes.ts` instead. This doesn't change the design (still a standalone script reusing `IndexReconciler.reconcile` directly, still invoked manually) — only its location and file extension.

---

### Task 1: `IndexReconciler`

**Files:**
- Create: `src/core/metadata/index-reconciler.ts`
- Test: `src/core/metadata/index-reconciler.test.ts`

**Interfaces:**
- Consumes: `Database` type + `.client` (Drizzle instance with `.execute(sql...)`) from `src/infra/db/client.ts`; `sql` and `sql.identifier` from `drizzle-orm`.
- Produces:
  ```ts
  export type IndexableField = { name: string; indexed?: boolean; unique?: boolean };
  export type IndexableEntity = { name: string; fields: readonly IndexableField[] };

  export class IndexReconciler {
    constructor(db: Database);
    reconcile(
      entities: readonly IndexableEntity[],
      log: { info: (obj: unknown, msg: string) => void; warn: (obj: unknown, msg: string) => void },
    ): Promise<void>;
  }
  ```
  `MetadataRegistry.listEntities()`'s `EntitySummary[]` (`src/core/metadata/metadata-registry.ts`) is structurally assignable to `readonly IndexableEntity[]` — no adapter needed, same trick `MetadataDriftService.check` already relies on for its own minimal entity type.

- [ ] **Step 1: Write the failing tests**

Create `src/core/metadata/index-reconciler.test.ts`:

```ts
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createDatabase } from "../../infra/db/client";
import type { Database } from "../../infra/db/client";
import { IndexReconciler } from "./index-reconciler";

const databaseUrl = process.env.TEST_DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap_test";

describe("IndexReconciler (live DB)", () => {
  let db: Database;
  let reconciler: IndexReconciler;
  let pgClient: Client;
  let dbAvailable = true;

  const entityName = "test.index_reconciler_entity";
  const indexedIndexName = `idx_records_${entityName.replace(/\./g, "_")}_region`;
  const uniqueIndexName = `uniq_records_${entityName.replace(/\./g, "_")}_code`;

  function makeLogSpy() {
    const infoMessages: string[] = [];
    const warnMessages: string[] = [];
    return {
      info: (_obj: unknown, msg: string) => infoMessages.push(msg),
      warn: (_obj: unknown, msg: string) => warnMessages.push(msg),
      infoMessages,
      warnMessages,
    };
  }

  async function dropTestIndexes() {
    await pgClient.query(`DROP INDEX IF EXISTS ${indexedIndexName}`);
    await pgClient.query(`DROP INDEX IF EXISTS ${uniqueIndexName}`);
  }

  beforeAll(async () => {
    db = createDatabase(databaseUrl);
    reconciler = new IndexReconciler(db);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping IndexReconciler live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await dropTestIndexes();
      await pgClient.end();
    }
    await db.close();
  });

  it("creates a partial index for an indexed: true field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const log = makeLogSpy();
    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "region", indexed: true }] }],
      log,
    );

    const { rows } = await pgClient.query<{ indexdef: string }>(
      "SELECT indexdef FROM pg_indexes WHERE indexname = $1",
      [indexedIndexName],
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]?.indexdef).toContain("region");
    expect(log.infoMessages).toContain("index: created");
  });

  it("creates a tenant-scoped unique index for a unique: true field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const log = makeLogSpy();
    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "code", unique: true }] }],
      log,
    );

    const { rows } = await pgClient.query<{ indexdef: string }>(
      "SELECT indexdef FROM pg_indexes WHERE indexname = $1",
      [uniqueIndexName],
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]?.indexdef).toContain("UNIQUE");
    expect(rows[0]?.indexdef).toContain("tenant_id");
  });

  it("is idempotent across repeated runs", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const entities = [{ name: entityName, fields: [{ name: "region", indexed: true }] }];
    const first = makeLogSpy();
    await reconciler.reconcile(entities, first);
    const second = makeLogSpy();
    await reconciler.reconcile(entities, second);

    expect(first.infoMessages).toEqual(["index: created"]);
    expect(second.infoMessages).toEqual([]);

    const { rows } = await pgClient.query(
      "SELECT indexdef FROM pg_indexes WHERE indexname = $1",
      [indexedIndexName],
    );
    expect(rows).toHaveLength(1);
  });

  it("does not throw when the database is unreachable", async () => {
    const unreachableDb = createDatabase("postgres://x:x@localhost:1/x");
    const unreachableReconciler = new IndexReconciler(unreachableDb);
    const log = makeLogSpy();

    await expect(
      unreachableReconciler.reconcile(
        [{ name: entityName, fields: [{ name: "region", indexed: true }] }],
        log,
      ),
    ).resolves.toBeUndefined();
    expect(log.warnMessages).toEqual(["index: reconcile skipped, could not reach the database"]);

    await unreachableDb.close();
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm vitest run src/core/metadata/index-reconciler.test.ts`
Expected: FAIL — `Cannot find module './index-reconciler'` (file doesn't exist yet).

- [ ] **Step 3: Write the implementation**

Create `src/core/metadata/index-reconciler.ts`:

```ts
import { sql } from "drizzle-orm";
import type { Database } from "../../infra/db/client";

export type IndexableField = {
  name: string;
  indexed?: boolean;
  unique?: boolean;
};

export type IndexableEntity = {
  name: string;
  fields: readonly IndexableField[];
};

function buildIndexName(entityName: string, fieldName: string, unique: boolean): string {
  const prefix = unique ? "uniq_records" : "idx_records";
  return `${prefix}_${entityName.replace(/\./g, "_")}_${fieldName}`;
}

// Postgres DDL statements (CREATE INDEX) do not support bind parameters at
// all — unlike the SELECT below, `CREATE INDEX ... WHERE entity = $1` fails
// outright. entityName/fieldName are safe to inline as quoted literals here
// only because they come exclusively from server-authored, MetadataCompiler-
// validated metadata, never from request input.
function quoteLiteral(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

export class IndexReconciler {
  constructor(private readonly db: Database) {}

  // Never let a DB hiccup at boot become a crash — mirrors MetadataDriftService's
  // graceful-degradation stance. CONCURRENTLY means these statements never block
  // concurrent reads/writes on `records`, so this is safe to run on every boot in
  // every environment (no dev/prod split needed).
  async reconcile(
    entities: readonly IndexableEntity[],
    log: { info: (obj: unknown, msg: string) => void; warn: (obj: unknown, msg: string) => void },
  ): Promise<void> {
    try {
      for (const entity of entities) {
        for (const field of entity.fields) {
          if (field.indexed) {
            await this.ensureIndex(entity.name, field.name, false, log);
          }
          if (field.unique) {
            await this.ensureIndex(entity.name, field.name, true, log);
          }
        }
      }
    } catch (error) {
      log.warn({ err: error }, "index: reconcile skipped, could not reach the database");
    }
  }

  private async ensureIndex(
    entityName: string,
    fieldName: string,
    unique: boolean,
    log: { info: (obj: unknown, msg: string) => void },
  ): Promise<void> {
    const indexName = buildIndexName(entityName, fieldName, unique);

    // Check first rather than relying solely on IF NOT EXISTS so we only log
    // (and only pay for a CONCURRENTLY build) when something actually changes.
    const existing = await this.db.client.execute(
      sql`SELECT 1 FROM pg_indexes WHERE indexname = ${indexName}`,
    );
    if (existing.rows.length > 0) {
      return;
    }

    const fieldLiteral = sql.raw(quoteLiteral(fieldName));
    const entityLiteral = sql.raw(quoteLiteral(entityName));
    // Must match QueryPlanner's/condition-to-sql.ts's fieldExpression() exactly
    // (jsonb_extract_path_text, not the `->>` operator) — Postgres only uses an
    // expression index when the query's expression is syntactically identical
    // to the indexed one; `data->>'f'` and `jsonb_extract_path_text(data, 'f')`
    // are semantically equal but distinct expressions, so an index built on one
    // form is never selected for a query written in the other (confirmed via
    // EXPLAIN with enable_seqscan off).
    const columns = unique
      ? sql`tenant_id, (jsonb_extract_path_text(data, ${fieldLiteral}))`
      : sql`(jsonb_extract_path_text(data, ${fieldLiteral}))`;
    const uniqueKeyword = unique ? sql`UNIQUE ` : sql``;

    await this.db.client.execute(sql`
      CREATE ${uniqueKeyword}INDEX CONCURRENTLY IF NOT EXISTS ${sql.identifier(indexName)}
      ON records (${columns})
      WHERE entity = ${entityLiteral} AND deleted = false
    `);

    log.info({ entity: entityName, field: fieldName, unique, index: indexName }, "index: created");
  }
}
```

Note: the `SELECT ... WHERE indexname = ${indexName}` check above is a normal
DML query, so it uses Drizzle's ordinary bound-parameter interpolation — the
same technique `condition-to-sql.ts`/`query-planner.ts` already use for field
names. `CREATE INDEX` is DDL, and **Postgres does not support bind parameters
in DDL statements at all** (confirmed empirically while implementing this
task: `CREATE INDEX ... WHERE entity = $1` fails with "bind message supplies
N parameters, but prepared statement requires 0", independent of
`CONCURRENTLY`). So inside the `CREATE INDEX` statement, `entityName`/
`fieldName` are inlined as manually-escaped SQL string literals via
`sql.raw(quoteLiteral(...))`, and the index name is inlined as an identifier
via `sql.identifier(...)` (which also has no bind-parameter form). Both are
safe only because they come exclusively from server-authored,
`MetadataCompiler`-validated metadata, never from request input — the
load-bearing invariant from the spec applies to all three interpolations in
this statement, not just the identifier.

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm vitest run src/core/metadata/index-reconciler.test.ts`
Expected: PASS (4 tests, or "skipped" if no local Postgres is running on port 5433).

- [ ] **Step 5: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no new errors (baseline is 17 pre-existing lint errors unrelated to this file).

- [ ] **Step 6: Commit**

```bash
git add src/core/metadata/index-reconciler.ts src/core/metadata/index-reconciler.test.ts
git commit -m "Add IndexReconciler: metadata-driven partial indexes for hot JSONB fields"
```

**Post-implementation correction (found while scoping sub-project 2, before
this task was committed):** the first implementation used `(data->>${fieldLiteral})`
as the indexed expression. Verified via `EXPLAIN` with `enable_seqscan` off
against the real dev database that Postgres never selects an index built on
`data->>'f'` for a query written as `jsonb_extract_path_text(data, 'f')`
(the form `QueryPlanner`/`condition-to-sql.ts` already use everywhere) — the
two are semantically equal but syntactically distinct expressions, and
Postgres's expression-index matching is syntactic. The code block above and
the implementation now both use `jsonb_extract_path_text(data, ${fieldLiteral})`.
A fifth test, "creates an index Postgres actually uses for QueryPlanner's
exact expression form," was added to `index-reconciler.test.ts` as a
regression guard — it runs `EXPLAIN` with `enable_seqscan` disabled and
asserts the plan contains `Index Cond`, not just that a `pg_indexes` row
exists (a `pg_indexes`-only check is insufficient — it passed even for the
buggy `data->>` version, since the index still gets *created*, just never
*used*).

---

### Task 2: Wire `IndexReconciler` into the container and boot path

**Files:**
- Modify: `src/core/container.ts`
- Modify: `src/server/app.ts`

**Interfaces:**
- Consumes: `IndexReconciler` from Task 1 (`src/core/metadata/index-reconciler.ts`).
- Produces: `container.indexReconciler: IndexReconciler`, called from `buildApp` — later tasks/scripts consume `container.indexReconciler.reconcile(entities, log)`.

- [ ] **Step 1: Add `IndexReconciler` to the container**

In `src/core/container.ts`, add the import next to the existing `MetadataDriftService` import:

```ts
import { IndexReconciler } from "./metadata/index-reconciler";
```

Right after the existing `const metadataDrift = new MetadataDriftService(db);` line, add:

```ts
  const indexReconciler = new IndexReconciler(db);
```

And add `indexReconciler,` to the returned object, right after `metadataDrift,`.

- [ ] **Step 2: Call it from `buildApp`**

In `src/server/app.ts`, right after the existing line
`await container.metadataDrift.check(container.metadata.listEntities(), app.log);`, add:

```ts
  await container.indexReconciler.reconcile(container.metadata.listEntities(), app.log);
```

- [ ] **Step 3: Typecheck**

Run: `pnpm typecheck`
Expected: no errors. `container.metadata.listEntities()` returns `EntitySummary[]`, which is structurally assignable to `readonly IndexableEntity[]` (same trick already relied on for `container.metadataDrift.check`).

- [ ] **Step 4: Run the full test suite**

Run: `pnpm test`
Expected: all existing tests still pass — `src/server/app.test.ts` boots `buildApp` against the live test DB already, so it now also exercises `IndexReconciler.reconcile` on every boot as a side effect. No new test file is needed for this task; the existing boot-path tests (including the one that boots against an unreachable DB and asserts no crash) already cover it.

- [ ] **Step 5: Commit**

```bash
git add src/core/container.ts src/server/app.ts
git commit -m "Wire IndexReconciler into the container and buildApp boot path"
```

---

### Task 3: Standalone manual-reconcile script

**Files:**
- Create: `src/workers/reconcile-indexes.ts`
- Modify: `package.json`

**Interfaces:**
- Consumes: `createContainer` (`src/core/container.ts`), `registerEntities` (`src/modules/registry.ts`), `loadConfig` (`src/server/config.ts`), `container.indexReconciler.reconcile` (Task 2).
- Produces: `pnpm index:reconcile` CLI entry point.

- [ ] **Step 1: Write the script**

Create `src/workers/reconcile-indexes.ts`:

```ts
import { createContainer } from "../core/container";
import { registerEntities } from "../modules/registry";
import { loadConfig } from "../server/config";

const config = loadConfig();
const container = createContainer(config);
registerEntities(container.metadata);

const log = {
  info: (obj: unknown, msg: string) => console.log(msg, obj),
  warn: (obj: unknown, msg: string) => console.warn(msg, obj),
};

try {
  await container.indexReconciler.reconcile(container.metadata.listEntities(), log);
} finally {
  await container.close();
}
```

- [ ] **Step 2: Add the pnpm script**

In `package.json`, add to `"scripts"`, next to the existing `"worker:outbox"` entry:

```json
    "index:reconcile": "tsx src/workers/reconcile-indexes.ts",
```

- [ ] **Step 3: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no new errors — unlike the `scripts/*.mjs` files, this one lives under `src/` and is covered by both.

- [ ] **Step 4: Manual verification**

Run: `docker compose up -d postgres` (if not already running), then `pnpm index:reconcile`.
Expected: process exits 0. If `crm.customers` doesn't yet have any `unique: true` field and its `indexed: true` fields (`code`, `status`) already got indexed by `pnpm dev`/`pnpm test` boots, output may show no "index: created" lines — that's correct (idempotent, nothing to do). To see a "created" line, temporarily add `indexed: true` to a field that doesn't have it yet in `src/modules/crm/customer.entity.ts`, run again, then revert the temporary change.

- [ ] **Step 5: Commit**

```bash
git add src/workers/reconcile-indexes.ts package.json
git commit -m "Add pnpm index:reconcile script for manual index reconciliation"
```
