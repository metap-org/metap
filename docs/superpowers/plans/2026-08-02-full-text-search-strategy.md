# Full-Text Search Strategy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an `EntityField` opt into real Postgres full-text matching (`to_tsvector`/`plainto_tsquery`, backed by a GIN index) instead of the current ILIKE-substring behavior, without changing anything for fields that don't opt in.

**Architecture:** A new `EntityField.searchMode?: "substring" | "fts"` flag (default `"substring"`, i.e. today's behavior unchanged). `QueryPlanner.planList` branches on it to build a `to_tsvector('simple', jsonb_extract_path_text(data, field)) @@ plainto_tsquery('simple', value)` condition instead of ILIKE. `IndexReconciler` (sub-project 1) grows a third index kind — a GIN expression index over the exact same `to_tsvector(...)` expression — so the new filter path is actually indexed, not just correct.

**Tech Stack:** TypeScript, Drizzle ORM (`sql` template), PostgreSQL (`tsvector`/`tsquery`/GIN), Vitest (live-DB tests).

## Global Constraints

- `searchMode` only matters when `searchable: true`; unset (or `"substring"`) must produce byte-for-byte the same SQL as before this plan — no behavior change for `crm.customers`' existing four searchable fields.
- Filter values reach `plainto_tsquery`, never `to_tsquery` — `to_tsquery` accepts client-controlled boolean/proximity operators as syntax, which would reopen exactly the operator-injection class `docs/superpowers/specs/2026-07-29-query-planner-hardening-design.md` was written to close. `plainto_tsquery` treats its whole input as plain text.
- Text search config is always the literal `'simple'` — no stemming/stopwords, no per-tenant/per-locale config selection (out of scope, see spec).
- The GIN index's indexed expression must be **exactly** `to_tsvector('simple', jsonb_extract_path_text(data, '<field>'))` — the same expression `QueryPlanner` generates for the query. Sub-project 1 shipped an index that was silently never used because `IndexReconciler` built it on `data->>'<field>'` while `QueryPlanner` queried via `jsonb_extract_path_text(data, '<field>')` — semantically equal, syntactically different, so Postgres never selected it (see the "Post-implementation correction" note in `docs/superpowers/plans/2026-08-01-hot-field-index-strategy.md`). Task 2 verifies this with an `EXPLAIN`-based test, not just a `pg_indexes`-existence check.
- Minimal, targeted tests only — no exhaustive matrix, per this project's established test-scope convention.

---

### Task 1: `EntityField.searchMode` + `QueryPlanner` FTS branch

**Files:**
- Modify: `src/core/metadata/entity.ts`
- Modify: `src/core/query/query-planner.ts`
- Test: `src/core/query/query-planner.test.ts`

**Interfaces:**
- Consumes: nothing new — extends the existing `EntityField` type and `QueryPlanner.planList`'s existing filter loop.
- Produces: `EntityField.searchMode?: "substring" | "fts"`, consumed by Task 2 (`IndexReconciler`) via the same field on the same type (`IndexableField` in `index-reconciler.ts` gets a matching `searchMode` property).

- [ ] **Step 1: Add the `searchMode` field to `EntityField`**

In `src/core/metadata/entity.ts`, add to the `EntityField` type, right after `searchable?: boolean;`:

```ts
  searchMode?: "substring" | "fts"; // default: "substring" — only meaningful when searchable: true
```

- [ ] **Step 2: Write the failing tests**

Add a new `describe` block to the end of `src/core/query/query-planner.test.ts` (after the existing closing `});` of `"QueryPlanner (via CrudService.list, live DB)"`, still inside the same file — add these imports at the top alongside the existing ones:

```ts
import { z } from "zod";
import type { EntityDefinition } from "../metadata/entity";
```

Then append:

```ts
describe("QueryPlanner searchMode: 'fts' (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;
  const createdIds: string[] = [];

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000040",
    userId: "00000000-0000-0000-0000-000000000041",
    roles: ["admin"],
  };

  const ftsTestEntity: EntityDefinition = {
    name: "test.fts_entries",
    label: "FTS Test Entry",
    tableName: "records",
    schema: z.object({ title: z.string(), code: z.string() }),
    fields: [
      { name: "title", label: "Title", kind: "string", searchable: true, searchMode: "fts" },
      { name: "code", label: "Code", kind: "string", searchable: true },
    ],
    listViews: [
      {
        name: "default",
        label: "Default",
        fields: ["title", "code"],
        filters: ["title", "code"],
        maxLimit: 50,
      },
    ],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-fts-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    const config: AppConfig = {
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    };

    container = createContainer(config);
    registerEntities(container.metadata);
    container.metadata.register(ftsTestEntity);

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping QueryPlanner FTS live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
      return;
    }

    const seedRecords = [
      { title: "Quick Brown Fox", code: "T001" },
      { title: "Lazy Dog Sleeps", code: "T002" },
      { title: "Brown Bear Wakes", code: "T003" },
    ];

    for (const seed of seedRecords) {
      const created = await container.crud.create("test.fts_entries", seed, context);
      if (created.ok) {
        createdIds.push(created.data.id);
      }
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      for (const id of createdIds) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [id]);
        await pgClient.query("DELETE FROM records WHERE id = $1", [id]);
      }
      await pgClient.end();
    }
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("matches a searchMode: 'fts' field regardless of word order", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "test.fts_entries",
      { limit: 30, filters: { title: "brown quick" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const titles = result.data.map((record) => (record.data as { title?: string }).title);
      expect(titles).toEqual(["Quick Brown Fox"]);
    }
  });

  it("does not match a searchMode: 'fts' field on an unrelated word", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "test.fts_entries",
      { limit: 30, filters: { title: "elephant" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.data).toHaveLength(0);
    }
  });

  it("still substring-matches a plain searchable field with no searchMode — no regression", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "test.fts_entries",
      { limit: 30, filters: { code: "T00" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.data).toHaveLength(3);
    }
  });
});
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `pnpm vitest run src/core/query/query-planner.test.ts -t "searchMode"`
Expected: FAIL — the first two tests fail because `QueryPlanner` still builds an ILIKE clause for `title` (which won't match "brown quick" against "Quick Brown Fox" via substring, and unrelated-word test may pass or fail depending on data, but the word-order test is the one that must fail pre-fix). The third test should already pass (no code change needed for plain `searchable` fields) — confirms it's a real regression guard, not a tautology.

- [ ] **Step 4: Implement the `QueryPlanner` branch**

In `src/core/query/query-planner.ts`, replace the existing filter branch:

```ts
      if (fieldDef?.searchable) {
        const escapedValue = value.replace(/[\\%_]/g, "\\$&");
        conditions.push(sql`${fieldExpr} ILIKE ${`%${escapedValue}%`}`);
      } else {
        conditions.push(sql`${fieldExpr} = ${value}`);
      }
```

with:

```ts
      if (fieldDef?.searchable && fieldDef.searchMode === "fts") {
        conditions.push(
          sql`to_tsvector('simple', ${fieldExpr}) @@ plainto_tsquery('simple', ${value})`,
        );
      } else if (fieldDef?.searchable) {
        const escapedValue = value.replace(/[\\%_]/g, "\\$&");
        conditions.push(sql`${fieldExpr} ILIKE ${`%${escapedValue}%`}`);
      } else {
        conditions.push(sql`${fieldExpr} = ${value}`);
      }
```

`${fieldExpr}` is already `jsonb_extract_path_text(${records.data}, ${fieldName})` (built by the existing `fieldExpression()` helper just above this loop) — reused as-is inside `to_tsvector(...)`, so the query's expression is `to_tsvector('simple', jsonb_extract_path_text(data, 'field'))`. Task 2's index must be built on that exact expression.

- [ ] **Step 5: Run tests to verify they pass**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: PASS — all tests in the file, including the pre-existing ones (regression check) and the three new ones.

- [ ] **Step 6: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no new errors (baseline: 17 pre-existing lint errors unrelated to these files).

- [ ] **Step 7: Commit**

```bash
git add src/core/metadata/entity.ts src/core/query/query-planner.ts src/core/query/query-planner.test.ts
git commit -m "Add EntityField.searchMode: fts, matched via to_tsvector/plainto_tsquery in QueryPlanner"
```

---

### Task 2: `IndexReconciler` GIN index for `searchMode: "fts"` fields

**Files:**
- Modify: `src/core/metadata/index-reconciler.ts`
- Modify: `src/core/metadata/index-reconciler.test.ts`

**Interfaces:**
- Consumes: `EntityField.searchMode` (Task 1) — `IndexableField` (in `index-reconciler.ts`) gets a matching `searchMode?: "substring" | "fts"` property, structurally compatible with `EntitySummary`'s fields the same way `indexed`/`unique` already are.
- Produces: no new public API — `IndexReconciler.reconcile` picks up `searchMode: "fts"` fields automatically, same call signature as before.

- [ ] **Step 1: Write the failing tests**

In `src/core/metadata/index-reconciler.test.ts`, add a `ginIndexName` constant next to the existing `indexedIndexName`/`uniqueIndexName`:

```ts
  const ginIndexName = `gin_records_${entityName.replace(/\./g, "_")}_title`;
```

Add `ginIndexName` to `dropTestIndexes`:

```ts
  async function dropTestIndexes() {
    await pgClient.query(`DROP INDEX IF EXISTS ${indexedIndexName}`);
    await pgClient.query(`DROP INDEX IF EXISTS ${uniqueIndexName}`);
    await pgClient.query(`DROP INDEX IF EXISTS ${ginIndexName}`);
  }
```

Add two new tests (after the existing "is idempotent across repeated runs" test):

```ts
  it("creates a GIN index for a searchMode: 'fts' field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    const log = makeLogSpy();
    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "title", searchable: true, searchMode: "fts" }] }],
      log,
    );

    const { rows } = await pgClient.query<{ indexdef: string }>(
      "SELECT indexdef FROM pg_indexes WHERE indexname = $1",
      [ginIndexName],
    );
    expect(rows).toHaveLength(1);
    expect(rows[0]?.indexdef.toLowerCase()).toContain("gin");
    expect(rows[0]?.indexdef).toContain("to_tsvector");
    expect(log.infoMessages).toContain("index: created");
  });

  it("creates a GIN index Postgres actually uses for QueryPlanner's exact FTS expression", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }
    await dropTestIndexes();

    await reconciler.reconcile(
      [{ name: entityName, fields: [{ name: "title", searchable: true, searchMode: "fts" }] }],
      makeLogSpy(),
    );

    // Same regression class as sub-project 1: verify actual index usage via
    // EXPLAIN, not just that a pg_indexes row exists. The table's existing
    // (tenant_id, entity, ...) composite indexes are cheap enough on a tiny
    // test table that the planner prefers them + a Filter over the GIN index
    // even with seqscan disabled — drop them for the duration of this
    // transaction (rolled back after) so the GIN index is the only viable
    // path and its selection actually proves it's usable.
    await pgClient.query("BEGIN");
    try {
      await pgClient.query("DROP INDEX records_tenant_entity_status_idx");
      await pgClient.query("DROP INDEX records_tenant_entity_created_idx");
      await pgClient.query("SET LOCAL enable_seqscan = off");
      const { rows } = await pgClient.query<{ "QUERY PLAN": string }>(
        `EXPLAIN SELECT id FROM records WHERE entity = $1 AND deleted = false
         AND to_tsvector('simple', jsonb_extract_path_text(data, $2)) @@ plainto_tsquery('simple', $3)`,
        [entityName, "title", "quick brown"],
      );
      const plan = rows.map((row) => row["QUERY PLAN"]).join("\n");
      expect(plan).toContain(ginIndexName);
    } finally {
      await pgClient.query("ROLLBACK");
    }
  });
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm vitest run src/core/metadata/index-reconciler.test.ts -t "GIN"`
Expected: FAIL — `field.searchMode` isn't read by `IndexReconciler` yet, so no index gets created and `pg_indexes` returns 0 rows.

- [ ] **Step 3: Implement the GIN index kind**

In `src/core/metadata/index-reconciler.ts`:

Update `IndexableField`:

```ts
export type IndexableField = {
  name: string;
  indexed?: boolean;
  unique?: boolean;
  searchable?: boolean;
  searchMode?: "substring" | "fts";
};
```

Update `buildIndexName` to take a `kind` instead of a boolean, since there are now three prefixes:

```ts
function buildIndexName(entityName: string, fieldName: string, kind: "idx" | "uniq" | "gin"): string {
  const prefix = kind === "uniq" ? "uniq_records" : kind === "gin" ? "gin_records" : "idx_records";
  return `${prefix}_${entityName.replace(/\./g, "_")}_${fieldName}`;
}
```

Update the two existing `buildIndexName(entityName, fieldName, unique)` call sites in `ensureIndex` to pass `unique ? "uniq" : "idx"` instead of the raw boolean — `ensureIndex`'s own `unique: boolean` parameter is unchanged, only the call into `buildIndexName` changes:

```ts
    const indexName = buildIndexName(entityName, fieldName, unique ? "uniq" : "idx");
```

Add the new branch in `reconcile`'s loop, alongside the existing `indexed`/`unique` checks:

```ts
          if (field.searchMode === "fts") {
            await this.ensureGinIndex(entity.name, field.name, log);
          }
```

Add the new private method, right after `ensureIndex`:

```ts
  private async ensureGinIndex(
    entityName: string,
    fieldName: string,
    log: { info: (obj: unknown, msg: string) => void },
  ): Promise<void> {
    const indexName = buildIndexName(entityName, fieldName, "gin");

    const existing = await this.db.client.execute(
      sql`SELECT 1 FROM pg_indexes WHERE indexname = ${indexName}`,
    );
    if (existing.rows.length > 0) {
      return;
    }

    const fieldLiteral = sql.raw(quoteLiteral(fieldName));
    const entityLiteral = sql.raw(quoteLiteral(entityName));

    await this.db.client.execute(sql`
      CREATE INDEX CONCURRENTLY IF NOT EXISTS ${sql.identifier(indexName)}
      ON records USING GIN (to_tsvector('simple', jsonb_extract_path_text(data, ${fieldLiteral})))
      WHERE entity = ${entityLiteral} AND deleted = false
    `);

    log.info({ entity: entityName, field: fieldName, index: indexName }, "index: created");
  }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm vitest run src/core/metadata/index-reconciler.test.ts`
Expected: PASS — all 7 tests in the file (5 from sub-project 1 + 2 new).

- [ ] **Step 5: Typecheck, lint, full suite**

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: no new lint errors (baseline 17); full suite passes (should be 112 + 5 new = 117, given Task 1 adds 3 and Task 2 adds 2).

- [ ] **Step 6: Commit**

```bash
git add src/core/metadata/index-reconciler.ts src/core/metadata/index-reconciler.test.ts
git commit -m "Extend IndexReconciler with a GIN index kind for searchMode: fts fields"
```
