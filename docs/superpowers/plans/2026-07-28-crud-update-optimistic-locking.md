# CrudService.update + Optimistic Locking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a record-update path to `CrudService`, using the `records` table's existing (until now unused) `version` column as a real optimistic lock.

**Architecture:** `PATCH /api/:entity/:id` merges the client's partial `data` into the existing record, blocks changes to the entity's workflow state field, validates the merged result against the entity's Zod schema, then performs a single atomic `UPDATE ... WHERE version = <expected>` — the WHERE clause itself is the optimistic lock, no wrapping transaction needed. Zero rows updated means the version the client last read is stale → `409 version_conflict`.

**Tech Stack:** Drizzle ORM (`and`, `eq`, `sql` from `drizzle-orm`), Zod, Vitest.

## Global Constraints

- Update is a **partial merge** (`{ ...existing.data, ...rawData }`), not a full replace.
- The client supplies the version it read as a `version` field in the request body (not a header).
- Any change to the entity's workflow state field in the update body is **silently ignored** (overwritten back to the existing value before validation) — state changes are reserved for a future workflow-transition endpoint, not this generic update path.
- The optimistic-lock check is the `WHERE version = ?` clause on the single `UPDATE` statement — do not add a separate read-then-write transaction wrapper, the single statement is already atomic.
- No `GET /api/:entity/:id` endpoint — out of scope, not needed (clients already have `version` from `create`/`list`/`update` responses).
- Testing is scoped to important cases only: happy path, stale-version conflict, state-field-blocked. No exhaustive matrix.
- ESM, default-import convention for CJS packages, ecosystem conventions established in the existing codebase (ecosystem already uses `and`/`eq`/`sql` from `drizzle-orm` in `query-planner.ts`/`outbox-service.ts`).
- Two pre-existing, unrelated typecheck errors exist in this repo (`src/infra/messaging/rabbitmq.ts`, `src/server/routes/records.ts`'s `ListInput`/`cursor` exactOptionalPropertyTypes mismatch) — out of scope, don't fix, don't let them block judging your own new code (confirm you introduce no *new* ones).

---

### Task 1: `PermissionService.canUpdateEntity`

**Files:**
- Modify: `src/core/permission/permission-service.ts`

**Interfaces:**
- Produces: `PermissionService.canUpdateEntity(context: RequestContext, entity: string): PermissionDecision`.

- [ ] **Step 1: Add the method**

In `src/core/permission/permission-service.ts`, add a new method to the `PermissionService` class, right after `canCreateEntity`:

```ts
  canUpdateEntity(_context: RequestContext, _entity: string): PermissionDecision {
    return { allowed: true };
  }
```

The full class should now read:

```ts
export class PermissionService {
  canReadEntity(_context: RequestContext, _entity: string): PermissionDecision {
    return { allowed: true };
  }

  canCreateEntity(_context: RequestContext, _entity: string): PermissionDecision {
    return { allowed: true };
  }

  canUpdateEntity(_context: RequestContext, _entity: string): PermissionDecision {
    return { allowed: true };
  }

  scopedTenant(context: Partial<RequestContext>) {
    return context.tenantId ?? "00000000-0000-0000-0000-000000000001";
  }
}
```

No dedicated test for this step — it mirrors `canReadEntity`/`canCreateEntity`, which also have no dedicated tests (this is a Phase 1 allow-everything stub; real RBAC is a separate, later plan).

- [ ] **Step 2: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors (only the two pre-existing, unrelated ones listed in Global Constraints).

- [ ] **Step 3: Commit**

```bash
git add src/core/permission/permission-service.ts
git commit -m "Add PermissionService.canUpdateEntity stub"
```

---

### Task 2: `WorkflowEngine.emitUpdated`

**Files:**
- Modify: `src/core/workflow/workflow-engine.ts`

**Interfaces:**
- Produces: `WorkflowEngine.emitUpdated(entity: EntityDefinition, recordId: string, data: Record<string, unknown>): Promise<void>`.

- [ ] **Step 1: Add the method**

In `src/core/workflow/workflow-engine.ts`, add a new method to the `WorkflowEngine` class, right after `emitCreated`:

```ts
  async emitUpdated(entity: EntityDefinition, recordId: string, data: Record<string, unknown>) {
    await this.outbox.enqueue({
      topic: `${entity.name}.record.updated`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: {
        recordId,
        data,
      },
    });
  }
```

No dedicated test — mirrors `emitCreated`, which also has no dedicated test.

- [ ] **Step 2: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors.

- [ ] **Step 3: Commit**

```bash
git add src/core/workflow/workflow-engine.ts
git commit -m "Add WorkflowEngine.emitUpdated"
```

---

### Task 3: `CrudService.update` with optimistic locking

**Files:**
- Modify: `src/core/crud/crud-service.ts`
- Create: `src/core/crud/crud-service.test.ts`

**Interfaces:**
- Consumes: `PermissionService.canUpdateEntity` (Task 1), `WorkflowEngine.emitUpdated` (Task 2), `createContainer` from `src/core/container.ts` (existing), `AppConfig` from `src/server/config.ts` (existing).
- Produces: `CrudService.update(entityName: string, id: string, expectedVersion: number, rawData: Record<string, unknown>, context: RequestContext): Promise<ServiceResult<RecordDto>>`.

- [ ] **Step 1: Write the failing tests, `src/core/crud/crud-service.test.ts`**

This test needs a live Postgres connection (it exercises real `CrudService.create`/`update` DB round trips) and self-skips if one isn't reachable, following the same pattern already used in `src/server/app.test.ts`'s live-DB test block:

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

describe("CrudService.update (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;

  const context: RequestContext = {
    tenantId: "00000000-0000-0000-0000-000000000020",
    userId: "00000000-0000-0000-0000-000000000021",
    roles: ["admin"],
  };

  beforeAll(async () => {
    const { publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
      publicKeyEncoding: { type: "spki", format: "pem" },
      privateKeyEncoding: { type: "pkcs8", format: "pem" },
    });

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-crud-update-test-"));
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
        `Skipping CrudService.update live-DB tests: could not connect to ${databaseUrl}: ${
          error instanceof Error ? error.message : String(error)
        }`,
      );
    }
  });

  afterAll(async () => {
    if (dbAvailable) {
      await pgClient.end();
    }
    await container.close();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  it("updates a record when the version matches", async () => {
    if (!dbAvailable) return;

    const created = await container.crud.create(
      "crm.customers",
      { code: "U001", name: "Acme" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Acme Corp" },
        context,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.version).toBe(created.data.version + 1);
        expect((result.data.data as { name?: string }).name).toBe("Acme Corp");
        expect((result.data.data as { code?: string }).code).toBe("U001");
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("rejects an update with a stale version", async () => {
    if (!dbAvailable) return;

    const created = await container.crud.create(
      "crm.customers",
      { code: "U002", name: "Beta" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const first = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Beta One" },
        context,
      );
      expect(first.ok).toBe(true);

      const stale = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Beta Two" },
        context,
      );

      expect(stale.ok).toBe(false);
      if (!stale.ok) {
        expect(stale.status).toBe(409);
        expect(stale.error).toBe("version_conflict");
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });

  it("ignores a client-supplied change to the workflow state field", async () => {
    if (!dbAvailable) return;

    const created = await container.crud.create(
      "crm.customers",
      { code: "U003", name: "Gamma" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { status: "active" },
        context,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect((result.data.data as { status?: string }).status).toBe("draft");
      }
    } finally {
      await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [created.data.id]);
      await pgClient.query("DELETE FROM records WHERE id = $1", [created.data.id]);
    }
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: FAIL — `container.crud.update` doesn't exist yet (TypeScript compile error, e.g. "Property 'update' does not exist on type 'CrudService'").

- [ ] **Step 3: Implement `CrudService.update`**

In `src/core/crud/crud-service.ts`, change the top import line:

```ts
import { eq } from "drizzle-orm";
```

to:

```ts
import { and, eq, sql } from "drizzle-orm";
```

Then add this method to the `CrudService` class, right after `create`:

```ts
  async update(
    entityName: string,
    id: string,
    expectedVersion: number,
    rawData: Record<string, unknown>,
    context: RequestContext,
  ): Promise<ServiceResult<RecordDto>> {
    const entity = this.metadata.getEntity(entityName);

    if (!entity) {
      return { ok: false, status: 404, error: "entity_not_found" };
    }

    const decision = this.permissions.canUpdateEntity(context, entity.name);

    if (!decision.allowed) {
      return { ok: false, status: 403, error: decision.reason ?? "forbidden" };
    }

    const existingRows = await this.db.client
      .select()
      .from(records)
      .where(
        and(
          eq(records.id, id),
          eq(records.tenantId, context.tenantId),
          eq(records.entity, entity.name),
          eq(records.deleted, false),
        ),
      );

    const existing = existingRows[0];

    if (!existing) {
      return { ok: false, status: 404, error: "record_not_found" };
    }

    const existingData = existing.data as Record<string, unknown>;
    const mergedData: Record<string, unknown> = { ...existingData, ...rawData };

    if (entity.workflow) {
      mergedData[entity.workflow.stateField] = existingData[entity.workflow.stateField];
    }

    const parsed = entity.schema.safeParse(mergedData);

    if (!parsed.success) {
      return { ok: false, status: 400, error: "validation_failed" };
    }

    const data = parsed.data;
    const code = typeof data.code === "string" ? data.code : null;

    const updatedRows = await this.db.client
      .update(records)
      .set({
        data,
        code,
        version: sql`${records.version} + 1`,
        updatedAt: new Date(),
        updatedBy: context.userId,
      })
      .where(
        and(
          eq(records.id, id),
          eq(records.tenantId, context.tenantId),
          eq(records.version, expectedVersion),
          eq(records.deleted, false),
        ),
      )
      .returning();

    const record = updatedRows[0];

    if (!record) {
      return { ok: false, status: 409, error: "version_conflict" };
    }

    await this.workflow.emitUpdated(entity, record.id, data);

    return { ok: true, data: record };
  }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: 3 passed (skip gracefully with a console warning if Postgres isn't reachable — that's still a pass, just a no-op; if you have `docker compose up -d postgres` running and migrated, they should actually exercise the DB).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no new errors beyond the two pre-existing ones.

- [ ] **Step 6: Commit**

```bash
git add src/core/crud/crud-service.ts src/core/crud/crud-service.test.ts
git commit -m "Add CrudService.update with optimistic locking"
```

---

### Task 4: `PATCH /api/:entity/:id` route + error vocabulary + end-to-end verification

**Files:**
- Modify: `src/server/error-handler.ts`
- Modify: `src/server/routes/records.ts`

**Interfaces:**
- Consumes: `CrudService.update` (Task 3), `sendServiceError` (existing).

- [ ] **Step 1: Add the two new error codes — modify `src/server/error-handler.ts`**

Change the `SERVICE_ERROR_MESSAGES` map to:

```ts
const SERVICE_ERROR_MESSAGES: Record<string, string> = {
  entity_not_found: "Entity not found.",
  forbidden: "You do not have permission to perform this action.",
  validation_failed: "Request validation failed.",
  insert_failed: "Failed to create the record.",
  record_not_found: "Record not found.",
  version_conflict: "The record was modified by someone else. Reload and try again.",
};
```

- [ ] **Step 2: Add the route — modify `src/server/routes/records.ts`**

Add a new schema constant, right after `RecordBodySchema`:

```ts
const UpdateBodySchema = z.object({
  version: z.number().int().positive(),
  data: z.record(z.unknown()),
});
```

Add a new route registration inside `registerRecordRoutes`, right after the existing `app.post<...>("/api/:entity", ...)` block:

```ts
  app.patch<{
    Params: { entity: string; id: string };
    Body: z.infer<typeof UpdateBodySchema>;
  }>(
    "/api/:entity/:id",
    {
      schema: {
        body: zodToJsonSchema(UpdateBodySchema),
      },
    },
    async (request, reply) => {
      const body = UpdateBodySchema.parse(request.body);
      const result = await container.crud.update(
        request.params.entity,
        request.params.id,
        body.version,
        body.data,
        request.context,
      );

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data };
    },
  );
```

- [ ] **Step 3: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no new errors.

- [ ] **Step 4: Run the full test suite**

Run: `pnpm vitest run`
Expected: all passing.

- [ ] **Step 5: End-to-end manual verification**

Bring up dependencies if not already running:
```bash
docker compose up -d postgres rabbitmq
pnpm db:migrate
```

Start the app:
```bash
pnpm dev
```

In another terminal, mint a token:
```bash
node -e "
const jwt = require('jsonwebtoken');
const fs = require('fs');
const privateKey = fs.readFileSync('keys/dev-jwt-private.pem', 'utf8');
const token = jwt.sign(
  { tenantId: '00000000-0000-0000-0000-000000000001', roles: ['admin'] },
  privateKey,
  { algorithm: 'RS256', subject: '00000000-0000-0000-0000-000000000002', expiresIn: '1h' },
);
console.log(token);
"
```

Create a record, then use its `id` and `version` (both `1` from the response) for the next steps:
```bash
curl -s -X POST \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"data":{"code":"P001","name":"Original"}}' \
  http://localhost:3000/api/crm.customers
```
Expected: `201`, body includes `"version":1` and an `"id"`.

Update it with the correct version (replace `RECORD_ID` with the id from above):
```bash
curl -i -X PATCH \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"version":1,"data":{"name":"Updated Name"}}' \
  http://localhost:3000/api/crm.customers/RECORD_ID
```
Expected: `200`, body's `data.data.name` is `"Updated Name"`, `data.version` is `2`, `data.data.code` is still `"P001"` (merge preserved it).

Attempt another update reusing the now-stale version `1`:
```bash
curl -i -X PATCH \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"version":1,"data":{"name":"Should Not Apply"}}' \
  http://localhost:3000/api/crm.customers/RECORD_ID
```
Expected: `409`, body `{"error":{"code":"version_conflict",...}}`.

Attempt to change the workflow state field directly (current version is `2` after the successful update above):
```bash
curl -i -X PATCH \
  -H "Authorization: Bearer TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"version":2,"data":{"status":"active"}}' \
  http://localhost:3000/api/crm.customers/RECORD_ID
```
Expected: `200` (the request succeeds — it's not rejected), but `data.data.status` in the response is still `"draft"`, not `"active"` — confirming the state-field block works even through the real HTTP path.

Clean up the test record:
```bash
docker compose exec postgres psql -U metap -d metap -c "DELETE FROM outbox_events WHERE aggregate_id = 'RECORD_ID'; DELETE FROM records WHERE id = 'RECORD_ID';"
```

Stop the dev server.

- [ ] **Step 6: Commit**

```bash
git add src/server/error-handler.ts src/server/routes/records.ts
git commit -m "Add PATCH /api/:entity/:id route for optimistic-locked updates"
```
