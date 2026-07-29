# QueryPlanner Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build metadata-constrained filter and real sort support into `QueryPlanner`, which currently has neither (filters don't exist at all; `sort` is silently ignored).

**Architecture:** The route layer does the bare minimum — split known reserved querystring keys (`limit`, `sort`) from everything else, and discard any non-string value outright (never pass an array/object through). Everything else becomes a `filters: Record<string, string>` bag handed to `CrudService.list` → `QueryPlanner.planList`, which does the real allowlist check against `entity.listViews[0].filters` and builds SQL using `jsonb_extract_path_text(data, fieldName)` with the field name passed as a genuine bound parameter (never string-concatenated) via Drizzle's `sql` tagged template.

**Tech Stack:** Drizzle ORM (`and`, `asc`, `desc`, `eq`, `sql` from `drizzle-orm`), Zod, Vitest.

## Global Constraints

- Filter operators: **equality** for ordinary allowed fields, **contains** (`ILIKE '%value%'`) for fields with `searchable: true`. No other operators, no client-chosen operator syntax.
- An unrecognized filter key (not in `entity.listViews[0].filters`) is **silently ignored**, not an error.
- Sort fields allowed: `{ f.name for f in entity.fields where f.sortable === true } ∪ { "createdAt", "updatedAt" }`. An invalid/unsortable requested sort falls back to `entity.listViews[0].defaultSort`, and if that's also unusable, falls back to `"-createdAt"` — the fallback resolves the *whole* field+direction pair together at each step, not just the field name.
- `createdAt`/`updatedAt` sort/filter directly on their real top-level columns (they don't exist inside the `data` JSONB blob at all). Every other field goes through `jsonb_extract_path_text`.
- `cursor` is removed outright from `ListInput` and the HTTP querystring schema — it currently does nothing, and a no-op parameter that looks like it does something is worse than not having it. Real pagination is separate, later work (roadmap Phase 4).
- No type-aware filter coercion (every comparison is a text comparison) — every filterable field on `crm.customers` today is a string/enum. No optimization of `code`/`status` to use their mirrored top-level columns — that's roadmap Phase 2's job, not this pass.
- The route must NOT register an AJV `schema.querystring` for the list route. Reason: `zodToJsonSchema` generates `additionalProperties: false` for a plain `z.object()`, and this app's Fastify AJV config uses `removeAdditional: true` (see `src/server/app.ts`), which strips any key not declared in a schema that sets `additionalProperties: false` — exactly the mechanism that already broke `POST /api/:entity`'s body once before this plan started. Registering a static AJV schema for the querystring would silently strip every filter key before the handler ever saw them. Validation of the known fields (`limit`, `sort`) still happens — just via `ListQuerySchema.parse(request.query)` inside the handler (as it already does today), not also via a Fastify-level AJV schema.
- Two pre-existing, unrelated typecheck errors exist in this repo (`src/infra/messaging/rabbitmq.ts`, and previously `src/server/routes/records.ts`'s `ListInput`/`cursor` exactOptionalPropertyTypes mismatch — that second one is fixed as a side effect of this plan's Task 2, since `cursor` is removed and the conditional-key-assignment pattern is used for `sort`). Confirm no *new* errors beyond whatever remains.
- Testing is scoped to important cases only per project convention — the spec's 5 test cases, no more. Use the `ctx.skip()` pattern (established in the prior plan's fix wave) for live-DB tests when Postgres isn't reachable, not `if (!dbAvailable) return;` (which reports as a false PASS).

---

### Task 1: `QueryPlanner.planList` — filters, sort, and their tests

**Files:**
- Modify: `src/core/query/query-planner.ts`
- Create: `src/core/query/query-planner.test.ts`

**Interfaces:**
- Produces: `ListInput = { limit: number; sort?: string; filters?: Record<string, string> }` (replaces the old `{ limit; cursor?; sort? }` shape — `cursor` is gone). `PlannedListQuery` unchanged. `QueryPlanner.planList(entityName, input, context): PlannedListQuery` — same signature, new behavior.
- Consumes: `MetadataRegistry.getEntity`, `PermissionService.scopedTenant` (both existing, unchanged). `createContainer` from `src/core/container.ts`, `AppConfig` from `src/server/config.ts` (existing, for the live-DB test harness).

- [ ] **Step 1: Write the failing tests, `src/core/query/query-planner.test.ts`**

This exercises the real SQL through `CrudService.list` against a live Postgres — testing `QueryPlanner.planList` in isolation isn't practical since it returns opaque Drizzle `SQL` query fragments, not something worth asserting on directly. Follows the same live-DB harness pattern as `src/core/crud/crud-service.test.ts`, but uses `ctx.skip()` (not `if (!dbAvailable) return;`) so a DB-unavailable run reports as skipped, not passed:

```ts
import { generateKeyPairSync } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { Client } from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import type { AppContainer } from "../container";
import { createContainer } from "../container";
import type { RequestContext } from "../permission/permission-service";
import type { AppConfig } from "../../server/config";

const databaseUrl = process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap";
const rabbitmqUrl = process.env.RABBITMQ_URL ?? "amqp://metap:metap@localhost:5672";

describe("QueryPlanner (via CrudService.list, live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;
  const createdIds: string[] = [];

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000030",
    userId: "00000000-0000-0000-0000-000000000031",
    roles: ["admin"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-query-planner-test-"));
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

    pgClient = new Client({ connectionString: databaseUrl });
    try {
      await pgClient.connect();
    } catch (error) {
      dbAvailable = false;
      console.warn(
        `Skipping QueryPlanner live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
      return;
    }

    const seedRecords = [
      { code: "Q001", name: "Acme Corp", status: "active" },
      { code: "Q002", name: "Acme Industries", status: "draft" },
      { code: "Q003", name: "Beta LLC", status: "active" },
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

  it("filters by an allowed equality field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, filters: { status: "active" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const names = result.data.map((record) => (record.data as { name?: string }).name);
      expect(names.sort()).toEqual(["Acme Corp", "Beta LLC"].sort());
    }
  });

  it("filters by an allowed searchable field using contains", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, filters: { name: "Acme" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const names = result.data.map((record) => (record.data as { name?: string }).name);
      expect(names.sort()).toEqual(["Acme Corp", "Acme Industries"].sort());
    }
  });

  it("silently ignores an unrecognized filter key", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, filters: { notARealField: "whatever" } },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.data.length).toBe(3);
    }
  });

  it("sorts by an explicit allowed field in both directions", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const ascending = await container.crud.list("crm.customers", { limit: 30, sort: "name" }, context);
    const descending = await container.crud.list(
      "crm.customers",
      { limit: 30, sort: "-name" },
      context,
    );

    expect(ascending.ok).toBe(true);
    expect(descending.ok).toBe(true);
    if (ascending.ok && descending.ok) {
      const ascNames = ascending.data.map((record) => (record.data as { name?: string }).name);
      const descNames = descending.data.map((record) => (record.data as { name?: string }).name);
      expect(ascNames).toEqual([...descNames].reverse());
      expect(ascNames[0]).toBe("Acme Corp");
    }
  });

  it("falls back to the default sort when given an invalid sort field", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const result = await container.crud.list(
      "crm.customers",
      { limit: 30, sort: "notASortableField" },
      context,
    );

    expect(result.ok).toBe(true);
    if (result.ok) {
      const ids = result.data.map((record) => record.id);
      expect(ids).toEqual([...createdIds].reverse());
    }
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: FAIL — a TypeScript error, since `ListInput` doesn't have a `filters` field yet and `CrudService.list`'s current `QueryPlanner.planList` doesn't apply filters/sort correctly.

- [ ] **Step 3: Rewrite `QueryPlanner.planList`**

Replace the entire content of `src/core/query/query-planner.ts` with:

```ts
import type { SQL } from "drizzle-orm";
import { and, asc, desc, eq, sql } from "drizzle-orm";
import { records } from "../../infra/db/schema";
import type { MetadataRegistry } from "../metadata/metadata-registry";
import type { PermissionService, RequestContext } from "../permission/permission-service";

export type ListInput = {
  limit: number;
  sort?: string;
  filters?: Record<string, string>;
};

export type PlannedListQuery = {
  where: SQL | undefined;
  limit: number;
  orderBy: SQL[];
};

type ResolvedSort = { field: string; descending: boolean };

function parseSort(
  candidate: string | undefined,
  sortableFields: ReadonlySet<string>,
): ResolvedSort | undefined {
  if (!candidate) {
    return undefined;
  }

  const descending = candidate.startsWith("-");
  const field = descending ? candidate.slice(1) : candidate;

  return sortableFields.has(field) ? { field, descending } : undefined;
}

export class QueryPlanner {
  constructor(
    private readonly metadata: MetadataRegistry,
    private readonly permissions: PermissionService,
  ) {}

  planList(
    entityName: string,
    input: ListInput,
    context: Partial<RequestContext>,
  ): PlannedListQuery {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      throw new Error(`Entity not found: ${entityName}`);
    }

    const tenantId = this.permissions.scopedTenant(context);
    const listView = entity.listViews[0];
    const limit = Math.min(input.limit, listView?.maxLimit ?? 100);

    const conditions: SQL[] = [
      eq(records.tenantId, tenantId),
      eq(records.entity, entity.name),
      eq(records.deleted, false),
    ];

    const allowedFilterFields = new Set(listView?.filters ?? []);
    const fieldsByName = new Map(entity.fields.map((field) => [field.name, field]));

    for (const [field, value] of Object.entries(input.filters ?? {})) {
      if (!allowedFilterFields.has(field)) {
        continue;
      }

      const fieldExpr = sql`jsonb_extract_path_text(${records.data}, ${field})`;
      const fieldDef = fieldsByName.get(field);

      conditions.push(
        fieldDef?.searchable
          ? sql`${fieldExpr} ILIKE ${`%${value}%`}`
          : sql`${fieldExpr} = ${value}`,
      );
    }

    const sortableFields = new Set<string>([
      ...entity.fields.filter((field) => field.sortable).map((field) => field.name),
      "createdAt",
      "updatedAt",
    ]);

    const resolvedSort =
      parseSort(input.sort, sortableFields) ??
      parseSort(listView?.defaultSort, sortableFields) ?? { field: "createdAt", descending: true };

    const sortExpr =
      resolvedSort.field === "createdAt"
        ? records.createdAt
        : resolvedSort.field === "updatedAt"
          ? records.updatedAt
          : sql`jsonb_extract_path_text(${records.data}, ${resolvedSort.field})`;

    return {
      where: and(...conditions),
      limit,
      orderBy: [resolvedSort.descending ? desc(sortExpr) : asc(sortExpr)],
    };
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/query/query-planner.test.ts`
Expected: 5 passed against a live Postgres (bring up `docker compose up -d postgres rabbitmq` and `pnpm db:migrate` first if not already running — these tests need real data to filter/sort against, this isn't optional for this task the way it was for some earlier ones).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: `src/core/crud/crud-service.ts`'s call site (`this.queryPlanner.planList(entity.name, input, context)`) and `CrudService.list`'s `input: ListInput` parameter both still typecheck cleanly since `ListInput`'s shape change is additive-plus-removal at the type level, not a breaking signature change to `CrudService.list` itself. `src/server/routes/records.ts` will show a NEW error at this point (still passing the old `{ limit, cursor, sort }` shape with a `cursor` field that no longer exists on `ListInput`) — that's expected and fixed in Task 2, not this one. Confirm the *only* new error is in `records.ts` and it's about the removed `cursor` field.

- [ ] **Step 6: Commit**

```bash
git add src/core/query/query-planner.ts src/core/query/query-planner.test.ts
git commit -m "Add metadata-constrained filters and real sort to QueryPlanner"
```

---

### Task 2: Wire filters into the HTTP route, drop `cursor`, end-to-end verification

**Files:**
- Modify: `src/server/routes/records.ts`

**Interfaces:**
- Consumes: `ListInput` from `src/core/query/query-planner.ts` (Task 1) — note the `filters` field is optional in the type but this route always supplies a concrete object (possibly empty), never `undefined`.

- [ ] **Step 1: Update `ListQuerySchema` and the GET handler**

In `src/server/routes/records.ts`, change:

```ts
const ListQuerySchema = z.object({
  limit: z.coerce.number().int().positive().max(200).default(30),
  cursor: z.string().optional(),
  sort: z.string().optional(),
});
```

to:

```ts
const ListQuerySchema = z.object({
  limit: z.coerce.number().int().positive().max(200).default(30),
  sort: z.string().optional(),
});
```

Add the import (alongside the existing imports at the top of the file):

```ts
import type { ListInput } from "../../core/query/query-planner";
```

Replace the entire GET route registration (currently `app.get<{ Params: { entity: string }; Querystring: z.infer<typeof ListQuerySchema> }>("/api/:entity", { schema: { querystring: zodToJsonSchema(ListQuerySchema) } }, async (request, reply) => { ... })`) with:

```ts
  app.get<{ Params: { entity: string }; Querystring: Record<string, unknown> }>(
    "/api/:entity",
    async (request, reply) => {
      const query = ListQuerySchema.parse(request.query);
      const filters: Record<string, string> = {};

      for (const [key, value] of Object.entries(request.query)) {
        if (key === "limit" || key === "sort") {
          continue;
        }

        if (typeof value === "string") {
          filters[key] = value;
        }
      }

      const listInput: ListInput = { limit: query.limit, filters };

      if (query.sort !== undefined) {
        listInput.sort = query.sort;
      }

      const result = await container.crud.list(request.params.entity, listInput, request.context);

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data, page: result.page };
    },
  );
```

Note this route no longer has a `schema:` option at all — deliberately, per the Global Constraints explanation about `removeAdditional: true` stripping unknown querystring keys before the handler could read them as candidate filters. `zodToJsonSchema` is still imported and used elsewhere in this file (the POST/PATCH routes' body/params schemas) — don't remove that import.

- [ ] **Step 2: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no errors at all now (this also fixes the pre-existing `records.ts` `cursor`/`exactOptionalPropertyTypes` error, since `cursor` is gone and `sort` uses the same conditional-key-assignment pattern already established in `src/core/auth/request-context.ts`). Only the `src/infra/messaging/rabbitmq.ts` pre-existing errors should remain.

- [ ] **Step 3: Run the full test suite**

Run: `pnpm vitest run`
Expected: all passing (bring up `docker compose up -d postgres rabbitmq` + `pnpm db:migrate` first if not already running, so the live-DB suites actually exercise the DB rather than skipping).

- [ ] **Step 4: End-to-end manual verification**

Start the app:
```bash
pnpm dev
```

In another terminal, mint a token (uses the existing dev keypair from an earlier plan):
```bash
TOKEN=$(node -e "
const jwt = require('jsonwebtoken');
const fs = require('fs');
const privateKey = fs.readFileSync('keys/dev-jwt-private.pem', 'utf8');
const token = jwt.sign(
  { tenantId: '00000000-0000-0000-0000-000000000001', roles: ['admin'] },
  privateKey,
  { algorithm: 'RS256', subject: '00000000-0000-0000-0000-000000000002', expiresIn: '1h' },
);
console.log(token);
")
```

Create three records to filter/sort against:
```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"E001","name":"Acme Corp","status":"active"}}' \
  http://localhost:3000/api/crm.customers

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"E002","name":"Acme Industries","status":"draft"}}' \
  http://localhost:3000/api/crm.customers

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"E003","name":"Beta LLC","status":"active"}}' \
  http://localhost:3000/api/crm.customers
```

Filter by an equality field:
```bash
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:3000/api/crm.customers?status=active"
```
Expected: 2 records (Acme Corp, Beta LLC), not 3.

Filter by a searchable field with a partial match:
```bash
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:3000/api/crm.customers?name=Acme"
```
Expected: 2 records (Acme Corp, Acme Industries), not Beta LLC.

Send an unrecognized filter key:
```bash
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:3000/api/crm.customers?notARealField=whatever"
```
Expected: all 3 records — the bogus key is silently ignored, not an error.

Sort ascending and descending by name:
```bash
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:3000/api/crm.customers?sort=name" | node -e "let d='';process.stdin.on('data',c=>d+=c);process.stdin.on('end',()=>console.log(JSON.parse(d).data.map(r=>r.data.name)));"
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:3000/api/crm.customers?sort=-name" | node -e "let d='';process.stdin.on('data',c=>d+=c);process.stdin.on('end',()=>console.log(JSON.parse(d).data.map(r=>r.data.name)));"
```
Expected: first prints `[ 'Acme Corp', 'Acme Industries', 'Beta LLC' ]`, second prints the exact reverse.

Clean up:
```bash
docker compose exec -T postgres psql -U metap -d metap -c "DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM records WHERE code IN ('E001','E002','E003')); DELETE FROM records WHERE code IN ('E001','E002','E003');"
```

Stop the dev server.

- [ ] **Step 5: Commit**

```bash
git add src/server/routes/records.ts
git commit -m "Wire metadata-constrained filters into GET /api/:entity, drop unused cursor"
```
