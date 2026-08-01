# Keyset Pagination Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a client page past the first `limit` rows of a list via an opaque cursor, without changing behavior for any request that doesn't send one.

**Architecture:** A new `src/core/query/cursor.ts` encodes/decodes an opaque base64(JSON) cursor `{ field, value, id, dir }`. `QueryPlanner.planList` validates a decoded cursor against the *resolved* sort (post-fallback) and, on match, adds a keyset WHERE condition mirroring the existing `[primary, id ASC]` orderBy exactly (a two-clause OR, since `id` is always ascending regardless of the primary field's direction) — or throws `InvalidCursorError` on any mismatch. `CrudService.list` executes with `limit + 1`, trims the lookahead row, and builds `nextCursor` from the last row of the trimmed page.

**Tech Stack:** TypeScript, Drizzle ORM (`sql` template), PostgreSQL, Vitest (live-DB tests).

## Global Constraints

- No behavior change for any request without a `cursor` — this is purely additive to `limit`.
- A cursor is valid only for the exact resolved sort (field + direction) it was generated under; any mismatch — including a garbage/undecodable cursor string — is a `400 invalid_cursor`, never silently ignored and never a 500.
- The keyset condition must reproduce the existing `orderBy`'s tiebreaker exactly: `id` compared with `>` always, regardless of the primary field's sort direction. A single Postgres row-value comparison can't express this (mixed directions), so it's built as an explicit OR — see Task 1.
- Cursor comparisons are built via raw `sql` template interpolation (like every other comparison already in `QueryPlanner.planList`, e.g. the existing `ILIKE`/`=` filter branches), not Drizzle's typed `eq`/`gt`/`lt` combinators — `fieldExpression()`'s return type is a union of a real `PgColumn` (for `createdAt`/`updatedAt`) and a raw `SQL` fragment (for JSONB fields), and only the untyped `sql` template treats both uniformly without fighting Drizzle's per-column type checking.
- Minimal, targeted tests only — no exhaustive matrix, per this project's established test-scope convention.

---

### Task 1: `Cursor` encode/decode + `QueryPlanner` keyset condition

**Files:**
- Create: `src/core/query/cursor.ts`
- Modify: `src/core/query/query-planner.ts`
- Test: `src/core/query/query-planner.test.ts`

**Interfaces:**
- Produces:
  ```ts
  // src/core/query/cursor.ts
  export type Cursor = { field: string; value: string; id: string; dir: "asc" | "desc" };
  export function encodeCursor(cursor: Cursor): string;
  export function decodeCursor(raw: string): Cursor | undefined;

  // src/core/query/query-planner.ts
  export class InvalidCursorError extends Error {}
  export type ListInput = { limit: number; sort?: string; filters?: Record<string, string>; cursor?: string };
  export type PlannedListQuery = {
    where: SQL | undefined;
    limit: number;
    orderBy: SQL[];
    resolvedSort: { field: string; descending: boolean };
  };
  ```
  Task 2 (`CrudService.list`) consumes `InvalidCursorError`, `plan.resolvedSort`, and `encodeCursor`.

- [ ] **Step 1: Write `cursor.ts`**

Create `src/core/query/cursor.ts`:

```ts
export type Cursor = {
  field: string;
  value: string;
  id: string;
  dir: "asc" | "desc";
};

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function encodeCursor(cursor: Cursor): string {
  return Buffer.from(JSON.stringify(cursor), "utf8").toString("base64");
}

export function decodeCursor(raw: string): Cursor | undefined {
  let parsed: unknown;

  try {
    parsed = JSON.parse(Buffer.from(raw, "base64").toString("utf8"));
  } catch {
    return undefined;
  }

  if (typeof parsed !== "object" || parsed === null) {
    return undefined;
  }

  const candidate = parsed as Partial<Cursor>;

  if (
    typeof candidate.field !== "string" ||
    typeof candidate.value !== "string" ||
    typeof candidate.id !== "string" ||
    !UUID_RE.test(candidate.id) ||
    (candidate.dir !== "asc" && candidate.dir !== "desc")
  ) {
    return undefined;
  }

  return { field: candidate.field, value: candidate.value, id: candidate.id, dir: candidate.dir };
}
```

- [ ] **Step 2: Write the failing `QueryPlanner`/cursor tests**

These test cursor *validation* and *SQL shape* directly against `QueryPlanner`, separate from the full paging-through-real-data tests in Task 2 (which exercise `CrudService.list` end-to-end). Add to the top of `src/core/query/query-planner.test.ts`, alongside the existing imports:

```ts
import { encodeCursor } from "./cursor";
import { InvalidCursorError } from "./query-planner";
```

Add this new `describe` block at the end of the file (after the `"QueryPlanner searchMode: 'fts' (live DB)"` block added in sub-project 2):

```ts
describe("QueryPlanner cursor validation", () => {
  let container: AppContainer;
  let tmpDir: string;

  beforeAll(() => {
    // No live-DB gating needed here — planList only builds a query object,
    // it never executes anything, so the only setup this needs is a real
    // (never-connected-to) container. createContainer still eagerly reads
    // the JWT public key file at construction time (createJwtVerifier does
    // fs.readFileSync synchronously), so a real keypair is required even
    // though auth is never exercised in these tests.
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-cursor-test-"));
    const publicKeyPath = path.join(tmpDir, "public.pem");
    writeFileSync(publicKeyPath, publicKey);

    container = createContainer({
      nodeEnv: "test",
      host: "0.0.0.0",
      port: 3000,
      databaseUrl,
      rabbitmqUrl,
      corsOrigins: [],
      authJwtPublicKeyPath: publicKeyPath,
    });
    registerEntities(container.metadata);
  });

  afterAll(async () => {
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("throws InvalidCursorError when the cursor's field doesn't match the resolved sort", () => {
    const cursor = encodeCursor({ field: "code", value: "X", id: "00000000-0000-0000-0000-000000000001", dir: "desc" });

    expect(() =>
      container.queryPlanner.planList(
        "crm.customers",
        { limit: 10, sort: "name", cursor },
        { tenantId: "00000000-0000-0000-0000-000000000001" },
      ),
    ).toThrow(InvalidCursorError);
  });

  it("throws InvalidCursorError for a garbage cursor string", () => {
    expect(() =>
      container.queryPlanner.planList(
        "crm.customers",
        { limit: 10, cursor: "not-a-real-cursor" },
        { tenantId: "00000000-0000-0000-0000-000000000001" },
      ),
    ).toThrow(InvalidCursorError);
  });
});
```

Note: `createContainer` never actually connects to Postgres just by being constructed (`pg.Pool` connects lazily on first query), so this `beforeAll` needs no DB-availability check/skip logic like the other describe blocks in this file — the only thing it truly depends on is a real key *file* on disk, not a reachable database.

- [ ] **Step 3: Run tests to verify they fail**

Run: `pnpm vitest run src/core/query/query-planner.test.ts -t "cursor"`
Expected: FAIL — `decodeCursor`/`InvalidCursorError` don't exist yet (import errors), and `ListInput` doesn't have a `cursor` field yet.

- [ ] **Step 4: Implement in `query-planner.ts`**

Add imports at the top:

```ts
import { decodeCursor } from "./cursor";
```

Add `cursor?: string;` to `ListInput`:

```ts
export type ListInput = {
  limit: number;
  sort?: string;
  filters?: Record<string, string>;
  cursor?: string;
};
```

Add `resolvedSort` to `PlannedListQuery`:

```ts
export type PlannedListQuery = {
  where: SQL | undefined;
  limit: number;
  orderBy: SQL[];
  resolvedSort: ResolvedSort;
};
```

Add the `InvalidCursorError` class, right after the `PlannedListQuery` type:

```ts
export class InvalidCursorError extends Error {}
```

In `planList`, right after `const sortExpr = fieldExpression(resolvedSort.field);` and before the `return` statement, add the cursor handling:

```ts
    if (input.cursor !== undefined) {
      const cursor = decodeCursor(input.cursor);

      if (
        !cursor ||
        cursor.field !== resolvedSort.field ||
        cursor.dir !== (resolvedSort.descending ? "desc" : "asc")
      ) {
        throw new InvalidCursorError("Cursor does not match the current sort");
      }

      const tiebreak = sql`(${sortExpr} = ${cursor.value} AND ${records.id} > ${cursor.id})`;
      const cursorCondition = resolvedSort.descending
        ? sql`((${sortExpr} < ${cursor.value}) OR ${tiebreak})`
        : sql`((${sortExpr} > ${cursor.value}) OR ${tiebreak})`;

      conditions.push(cursorCondition);
    }
```

Update the `return` statement to include `resolvedSort` and to build `where` from `conditions` *after* the cursor push above (the existing `return` already references `and(...conditions)` — since `conditions` is the same array pushed to above, no other change needed there beyond adding the new field):

```ts
    return {
      where: and(...conditions),
      limit,
      orderBy: [resolvedSort.descending ? desc(sortExpr) : asc(sortExpr), asc(records.id)],
      resolvedSort,
    };
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: PASS — all tests in the file (11 from before + 2 new cursor-validation ones = 13).

- [ ] **Step 6: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no new errors (baseline 17).

- [ ] **Step 7: Commit**

```bash
git add src/core/query/cursor.ts src/core/query/query-planner.ts src/core/query/query-planner.test.ts
git commit -m "Add cursor encode/decode and QueryPlanner keyset condition + InvalidCursorError"
```

---

### Task 2: `CrudService.list` executes with lookahead and returns `nextCursor`

**Files:**
- Modify: `src/core/crud/crud-service.ts`
- Modify: `src/server/routes/records.ts`
- Test: `src/core/query/query-planner.test.ts` (paging-through-real-data tests, alongside the existing live-DB describe blocks in this file — despite the filename, these test end-to-end paging behavior via `CrudService.list`, matching how the file's other describe blocks already test `QueryPlanner` indirectly this way)

**Interfaces:**
- Consumes: `InvalidCursorError`, `PlannedListQuery.resolvedSort` (Task 1), `encodeCursor` (Task 1).
- Produces: `CrudService.list`'s `ServiceResult.page` becomes `{ limit: number; nextCursor: string | null }` on success; on an invalid cursor, `{ ok: false, status: 400, error: "invalid_cursor", message: string }`. `ListQuerySchema` accepts `?cursor=`.

- [ ] **Step 1: Write the failing tests**

Add this new `describe` block to the end of `src/core/query/query-planner.test.ts`:

```ts
describe("QueryPlanner keyset pagination (via CrudService.list, live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;
  const createdIds: string[] = [];

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000050",
    userId: "00000000-0000-0000-0000-000000000051",
    roles: ["admin"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-pagination-test-"));
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

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping keyset pagination live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
      return;
    }

    const seedRecords = [
      { code: "P001", name: "Page Alpha" },
      { code: "P002", name: "Page Bravo" },
      { code: "P003", name: "Page Charlie" },
      { code: "P004", name: "Page Delta" },
      { code: "P005", name: "Page Echo" },
    ];

    for (const seed of seedRecords) {
      const created = await container.crud.create("crm.customers", seed, context);
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

  it("pages through the default sort (createdAt) with no overlap or gaps", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const page1 = await container.crud.list("crm.customers", { limit: 2 }, context);
    expect(page1.ok).toBe(true);
    if (!page1.ok) return;
    expect(page1.data).toHaveLength(2);
    const cursor1 = (page1.page as { nextCursor: string | null }).nextCursor;
    expect(cursor1).not.toBeNull();

    const page2 = await container.crud.list(
      "crm.customers",
      { limit: 2, cursor: cursor1 as string },
      context,
    );
    expect(page2.ok).toBe(true);
    if (!page2.ok) return;
    expect(page2.data).toHaveLength(2);

    const idsSoFar = [...page1.data.map((r) => r.id), ...page2.data.map((r) => r.id)];
    expect(new Set(idsSoFar).size).toBe(4);
    expect(idsSoFar.every((id) => createdIds.includes(id))).toBe(true);
  });

  it("pages through a JSONB-backed sort field (name)", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const page1 = await container.crud.list("crm.customers", { limit: 2, sort: "name" }, context);
    expect(page1.ok).toBe(true);
    if (!page1.ok) return;
    const names1 = page1.data.map((r) => (r.data as { name?: string }).name);
    const cursor1 = (page1.page as { nextCursor: string | null }).nextCursor;
    expect(cursor1).not.toBeNull();

    const page2 = await container.crud.list(
      "crm.customers",
      { limit: 2, sort: "name", cursor: cursor1 as string },
      context,
    );
    expect(page2.ok).toBe(true);
    if (!page2.ok) return;
    const names2 = page2.data.map((r) => (r.data as { name?: string }).name);

    expect(names1).toEqual(["Page Alpha", "Page Bravo"]);
    expect(names2).toEqual(["Page Charlie", "Page Delta"]);
  });

  it("rejects a cursor generated under a different sort with 400 invalid_cursor", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const page1 = await container.crud.list("crm.customers", { limit: 2 }, context);
    expect(page1.ok).toBe(true);
    if (!page1.ok) return;
    const cursorFromDefaultSort = (page1.page as { nextCursor: string | null }).nextCursor as string;

    const result = await container.crud.list(
      "crm.customers",
      { limit: 2, sort: "name", cursor: cursorFromDefaultSort },
      context,
    );

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.status).toBe(400);
    expect(result.error).toBe("invalid_cursor");
  });

  it("rejects a garbage cursor string with 400 invalid_cursor, not a 500", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 2, cursor: "not-a-real-cursor" },
      context,
    );

    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.status).toBe(400);
    expect(result.error).toBe("invalid_cursor");
  });

  it("returns nextCursor: null on the last page", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list("crm.customers", { limit: 30 }, context);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect((result.page as { nextCursor: string | null }).nextCursor).toBeNull();
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm vitest run src/core/query/query-planner.test.ts -t "keyset pagination"`
Expected: FAIL — `CrudService.list` doesn't send `cursor` to `planList`'s caller correctly yet in a way that produces `nextCursor` (the `page` object has no `nextCursor` key at all yet, so `(page as {nextCursor}).nextCursor` is `undefined`, not `null`, failing the `toBeNull()`/`not.toBeNull()` assertions), and there's no `invalid_cursor` handling in `CrudService.list` yet (an `InvalidCursorError` thrown by `planList` propagates uncaught, failing the request instead of being caught and converted to a 400).

- [ ] **Step 3: Implement in `crud-service.ts`**

Update the import line for `query-planner`:

```ts
import { InvalidCursorError, type ListInput, type QueryPlanner } from "../query/query-planner";
```

Add the import for `encodeCursor` alongside the other imports:

```ts
import { encodeCursor } from "../query/cursor";
```

Add a small private helper, right after `maskRecordForRead`:

```ts
  private sortFieldValue(row: RecordDto, field: string): string {
    if (field === "createdAt" || field === "updatedAt") {
      return row[field].toISOString();
    }
    return String((row.data as Record<string, unknown>)[field] ?? "");
  }
```

Replace the body of `list`:

```ts
  async list(
    entityName: string,
    input: ListInput,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto[]>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = await this.permissions.canReadEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const snapshot = await this.permissions.loadSnapshot(context.tenantId, entity.name);
    const recordPolicies = snapshot.getRecordPolicies("read");

    let plan;
    try {
      plan = this.queryPlanner.planList(entity.name, input, context, recordPolicies);
    } catch (error) {
      if (error instanceof InvalidCursorError) {
        return { ok: false, status: 400, error: "invalid_cursor", message: error.message };
      }
      throw error;
    }

    const rows = await this.db.client
      .select()
      .from(records)
      .where(plan.where)
      .orderBy(...plan.orderBy)
      .limit(plan.limit + 1);

    const hasMore = rows.length > plan.limit;
    const pageRows = hasMore ? rows.slice(0, plan.limit) : rows;

    const data = pageRows.map((row) => this.maskRecordForRead(entity, context, snapshot, row));

    let nextCursor: string | null = null;
    const lastRow = pageRows[pageRows.length - 1];

    if (hasMore && lastRow) {
      nextCursor = encodeCursor({
        field: plan.resolvedSort.field,
        value: this.sortFieldValue(lastRow, plan.resolvedSort.field),
        id: lastRow.id,
        dir: plan.resolvedSort.descending ? "desc" : "asc",
      });
    }

    return {
      ok: true,
      data,
      page: {
        limit: plan.limit,
        nextCursor,
      },
    };
  }
```

- [ ] **Step 4: Add `cursor` to the route's query schema**

In `src/server/routes/records.ts`, update `ListQuerySchema`:

```ts
const ListQuerySchema = z.object({
  limit: z.coerce.number().int().positive().max(200).default(30),
  sort: z.string().optional(),
  cursor: z.string().optional(),
});
```

And after the existing `if (query.sort !== undefined) { listInput.sort = query.sort; }` block, add:

```ts
      if (query.cursor !== undefined) {
        listInput.cursor = query.cursor;
      }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: PASS — all tests in the file (13 from Task 1 + 5 new = 18).

- [ ] **Step 6: Typecheck, lint, full suite**

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: no new lint errors (baseline 17); full suite passes (117 before this sub-project + 2 cursor-validation + 5 pagination = 124).

- [ ] **Step 7: Commit**

```bash
git add src/core/crud/crud-service.ts src/server/routes/records.ts src/core/query/query-planner.test.ts
git commit -m "CrudService.list: execute with lookahead, return nextCursor; accept ?cursor= in the route"
```
