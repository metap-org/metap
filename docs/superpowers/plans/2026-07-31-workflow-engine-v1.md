# Workflow Engine V1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make declared workflow transitions (e.g. `crm.customers`' `activate`/`block`) actually executable — atomically, with optimistic locking, optional guard conditions, and a permanent audit trail — instead of only assigning initial status on create.

**Architecture:** Extend the existing `WorkflowEngine` (one class, no new service) with transition lookup/guard/audit/outbox helper methods. Add a new `CrudService.transition()` orchestration method mirroring the shape of the existing `create`/`update` methods. Wire it behind a new `POST /api/:entity/:id/transitions/:action` route.

**Tech Stack:** Fastify, Zod, Drizzle ORM, PostgreSQL, vitest (live-DB integration tests, following the existing pattern in `src/core/crud/crud-service.test.ts`).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-31-workflow-engine-v1-design.md` — every task below implements a section of it.
- Do **not** touch `PATCH /api/:entity/:id` behavior — it continues to freeze the state field.
- Do **not** build a notification consumer — only the `<entity>.workflow.transitioned` outbox topic (the stub hook).
- Guards are TypeScript predicates on `WorkflowTransition`, not a declarative DSL.
- Per project convention (CLAUDE.md): **do not commit implementation changes.** Leave the diff uncommitted for the user to review at the end. (The design spec doc was already committed separately during brainstorming — that's expected and already done.)
- `docker compose up -d postgres rabbitmq` must be running for any test/migration step — confirmed already up on this machine (postgres on host port 5433).
- Run `pnpm typecheck` after any type-level change before moving to the next task.

---

### Task 1: `workflow_events` table + migration

**Files:**
- Modify: `src/infra/db/schema.ts:46-64` (insert new table between `outboxEvents` and `recordRelations`)
- Create (generated): `src/infra/db/migrations/000X_*.sql` (via `pnpm db:generate`, filename is auto-generated)

**Interfaces:**
- Produces: `workflowEvents` Drizzle table export — columns `id, tenantId, entity, recordId, action, fromState, toState, actor, createdAt` — consumed by Task 3's `WorkflowEngine.recordEvent`.

- [ ] **Step 1: Add the `workflowEvents` table to the schema**

In `src/infra/db/schema.ts`, insert this block after the `outboxEvents` table definition (after line 62, before `export const recordRelations = relations(records, () => ({}));` on line 64):

```ts
export const workflowEvents = pgTable(
  "workflow_events",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    tenantId: uuid("tenant_id").notNull(),
    entity: varchar("entity", { length: 120 }).notNull(),
    recordId: uuid("record_id").notNull(),
    action: varchar("action", { length: 80 }).notNull(),
    fromState: varchar("from_state", { length: 80 }).notNull(),
    toState: varchar("to_state", { length: 80 }).notNull(),
    actor: uuid("actor"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  },
  (table) => ({
    tenantEntityRecordIdx: index("workflow_events_tenant_entity_record_idx").on(
      table.tenantId,
      table.entity,
      table.recordId,
      table.createdAt,
    ),
  }),
);
```

No new imports are needed — `pgTable`, `uuid`, `varchar`, `timestamp`, `index` are already imported at the top of the file.

- [ ] **Step 2: Generate the migration**

Run: `pnpm db:generate`
Expected: A new file appears under `src/infra/db/migrations/` (e.g. `0001_<random-name>.sql`) containing a `CREATE TABLE "workflow_events"` statement and a `CREATE INDEX` for `workflow_events_tenant_entity_record_idx`. Open the generated file and confirm it contains only this new table — no unrelated diffs against `records` or `outbox_events`.

- [ ] **Step 3: Apply the migration**

Run: `pnpm db:migrate`
Expected: command exits 0, no errors.

- [ ] **Step 4: Verify the table exists**

Run: `docker compose exec postgres psql -U metap -d metap -c '\d workflow_events'`
Expected: output lists the 9 columns above and the index.

---

### Task 2: Guard support on `WorkflowTransition` + example guard

**Files:**
- Modify: `src/core/metadata/entity.ts:1-2, 37-42`
- Modify: `src/modules/crm/customer.entity.ts:60-63`

**Interfaces:**
- Consumes: nothing new.
- Produces: `WorkflowTransition.guard?: (data: Record<string, unknown>, context: RequestContext) => true | string` — consumed by Task 3's `WorkflowEngine.runGuard`.

- [ ] **Step 1: Add the `RequestContext` type import and the `guard` field**

In `src/core/metadata/entity.ts`, change line 1 from:

```ts
import type { z } from "zod";
```

to:

```ts
import type { z } from "zod";
import type { RequestContext } from "../permission/permission-service";
```

(This is a type-only import into a type-only import in `permission-service.ts` back into this file via `MetadataRegistry` — a type-only circular reference, which TypeScript resolves fine since `import type` is erased at compile time.)

Then change the `WorkflowTransition` type (lines 37-42) from:

```ts
export type WorkflowTransition = {
  action: string;
  from: string;
  to: string;
  label: string;
};
```

to:

```ts
export type WorkflowTransition = {
  action: string;
  from: string;
  to: string;
  label: string;
  guard?: (data: Record<string, unknown>, context: RequestContext) => true | string;
};
```

- [ ] **Step 2: Add an example guard to `crm.customers`' `activate` transition**

In `src/modules/crm/customer.entity.ts`, change the `transitions` array (lines 60-63) from:

```ts
    transitions: [
      { action: "activate", from: "draft", to: "active", label: "Activate" },
      { action: "block", from: "active", to: "blocked", label: "Block" },
    ],
```

to:

```ts
    transitions: [
      {
        action: "activate",
        from: "draft",
        to: "active",
        label: "Activate",
        guard: (data) =>
          typeof data.email === "string" && data.email.length > 0
            ? true
            : "Email is required to activate a customer.",
      },
      { action: "block", from: "active", to: "blocked", label: "Block" },
    ],
```

- [ ] **Step 3: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

---

### Task 3: `WorkflowEngine` — transition lookup, guard execution, audit log, outbox stub

**Files:**
- Modify: `src/core/workflow/workflow-engine.ts` (full file)
- Test: `src/core/workflow/workflow-engine.test.ts` (new)

**Interfaces:**
- Consumes: `WorkflowTransition.guard` (Task 2), `workflowEvents` table (Task 1).
- Produces (consumed by Task 4's `CrudService.transition`):
  - `findTransition(entity: EntityDefinition, action: string, fromState: string): WorkflowTransition | undefined`
  - `runGuard(transition: WorkflowTransition, data: Record<string, unknown>, context: RequestContext): true | string`
  - `recordEvent(executor: DbExecutor, entity: EntityDefinition, recordId: string, action: string, fromState: string, toState: string, context: RequestContext): Promise<void>`
  - `emitTransitioned(executor: DbExecutor, entity: EntityDefinition, recordId: string, action: string, fromState: string, toState: string, actor: string | undefined): Promise<void>`

- [ ] **Step 1: Write the failing unit tests**

Create `src/core/workflow/workflow-engine.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { customerEntity } from "../../modules/crm/customer.entity";
import type { OutboxService } from "../outbox/outbox-service";
import type { RequestContext } from "../permission/permission-service";
import { WorkflowEngine } from "./workflow-engine";

const context: RequestContext = {
  tenantId: "00000000-0000-0000-0000-000000000001",
  userId: "00000000-0000-0000-0000-000000000002",
};

describe("WorkflowEngine.findTransition", () => {
  const engine = new WorkflowEngine({} as unknown as OutboxService);

  it("finds a transition matching the action and current state", () => {
    const transition = engine.findTransition(customerEntity, "activate", "draft");
    expect(transition?.to).toBe("active");
  });

  it("returns undefined when the action does not exist", () => {
    expect(engine.findTransition(customerEntity, "nope", "draft")).toBeUndefined();
  });

  it("returns undefined when the action does not apply to the current state", () => {
    expect(engine.findTransition(customerEntity, "block", "draft")).toBeUndefined();
  });
});

describe("WorkflowEngine.runGuard", () => {
  const engine = new WorkflowEngine({} as unknown as OutboxService);

  it("allows a transition with no guard", () => {
    const transition = engine.findTransition(customerEntity, "block", "active");
    if (!transition) throw new Error("expected transition");
    expect(engine.runGuard(transition, {}, context)).toBe(true);
  });

  it("allows a guarded transition when the guard passes", () => {
    const transition = engine.findTransition(customerEntity, "activate", "draft");
    if (!transition) throw new Error("expected transition");
    expect(engine.runGuard(transition, { email: "a@b.com" }, context)).toBe(true);
  });

  it("blocks a guarded transition and returns the guard's reason", () => {
    const transition = engine.findTransition(customerEntity, "activate", "draft");
    if (!transition) throw new Error("expected transition");
    const result = engine.runGuard(transition, {}, context);
    expect(result).toBe("Email is required to activate a customer.");
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/workflow/workflow-engine.test.ts`
Expected: FAIL — `findTransition` and `runGuard` do not exist on `WorkflowEngine` (TypeScript compile error surfaced through vitest's transform step).

- [ ] **Step 3: Implement the new `WorkflowEngine` methods**

Replace the full contents of `src/core/workflow/workflow-engine.ts` with:

```ts
import type { DbExecutor } from "../../infra/db/client";
import { workflowEvents } from "../../infra/db/schema";
import type { EntityDefinition, WorkflowTransition } from "../metadata/entity";
import type { OutboxService } from "../outbox/outbox-service";
import type { RequestContext } from "../permission/permission-service";

export class WorkflowEngine {
  constructor(private readonly outbox: OutboxService) {}

  getInitialStatus(entity: EntityDefinition, data: Record<string, unknown>) {
    if (!entity.workflow) {
      return undefined;
    }

    const explicitStatus = data[entity.workflow.stateField];
    return typeof explicitStatus === "string" ? explicitStatus : entity.workflow.initialState;
  }

  findTransition(
    entity: EntityDefinition,
    action: string,
    fromState: string,
  ): WorkflowTransition | undefined {
    return entity.workflow?.transitions.find((t) => t.action === action && t.from === fromState);
  }

  runGuard(
    transition: WorkflowTransition,
    data: Record<string, unknown>,
    context: RequestContext,
  ): true | string {
    if (!transition.guard) {
      return true;
    }

    return transition.guard(data, context);
  }

  async recordEvent(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    action: string,
    fromState: string,
    toState: string,
    context: RequestContext,
  ) {
    await executor.insert(workflowEvents).values({
      tenantId: context.tenantId,
      entity: entity.name,
      recordId,
      action,
      fromState,
      toState,
      actor: context.userId,
    });
  }

  async emitTransitioned(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    action: string,
    fromState: string,
    toState: string,
    actor: string | undefined,
  ) {
    await this.outbox.enqueue(executor, {
      topic: `${entity.name}.workflow.transitioned`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: { recordId, action, from: fromState, to: toState, actor },
    });
  }

  async emitCreated(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    data: Record<string, unknown>,
  ) {
    await this.outbox.enqueue(executor, {
      topic: `${entity.name}.record.created`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: {
        recordId,
        data,
      },
    });
  }

  async emitUpdated(
    executor: DbExecutor,
    entity: EntityDefinition,
    recordId: string,
    data: Record<string, unknown>,
    version: number,
  ) {
    await this.outbox.enqueue(executor, {
      topic: `${entity.name}.record.updated`,
      aggregateType: entity.name,
      aggregateId: recordId,
      payload: {
        recordId,
        data,
        version,
      },
    });
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/workflow/workflow-engine.test.ts`
Expected: PASS (6 tests).

- [ ] **Step 5: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

---

### Task 4: `CrudService.transition` orchestration + error plumbing

**Files:**
- Modify: `src/core/crud/result.ts` (full file)
- Modify: `src/server/error-handler.ts:31-40, 66-72` (message lookup + `sendServiceError`)
- Modify: `src/core/crud/crud-service.ts` (add `transition` method; fix stale comment at lines 165-170)
- Test: `src/core/crud/crud-service.test.ts` (append new describe block)

**Interfaces:**
- Consumes: `WorkflowEngine.findTransition/runGuard/recordEvent/emitTransitioned` (Task 3).
- Produces (consumed by Task 5's route): `CrudService.transition(entityName: string, id: string, action: string, expectedVersion: number, context: RequestContext): Promise<ServiceResult<RecordDto>>`, returning error codes `entity_not_found` (404), `forbidden` (403), `record_not_found` (404), `no_workflow` (400), `invalid_transition` (409), `guard_failed` (422, with `message` set to the guard's reason), `version_conflict` (409).

- [ ] **Step 1: Add an optional `message` to `ServiceResult`**

Replace the full contents of `src/core/crud/result.ts` with:

```ts
export type ServiceResult<T> =
  | {
      ok: true;
      data: T;
      page?: unknown;
    }
  | {
      ok: false;
      status: number;
      error: string;
      message?: string;
    };
```

- [ ] **Step 2: Let `sendServiceError` use the optional message, and add new error codes**

In `src/server/error-handler.ts`, change the `SERVICE_ERROR_MESSAGES` map (lines 31-38) from:

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

to:

```ts
const SERVICE_ERROR_MESSAGES: Record<string, string> = {
  entity_not_found: "Entity not found.",
  forbidden: "You do not have permission to perform this action.",
  validation_failed: "Request validation failed.",
  insert_failed: "Failed to create the record.",
  record_not_found: "Record not found.",
  version_conflict: "The record was modified by someone else. Reload and try again.",
  no_workflow: "This entity has no workflow.",
  invalid_transition: "This transition is not valid from the record's current state.",
  guard_failed: "This transition is not allowed.",
};
```

Then change `sendServiceError` (lines 66-72) from:

```ts
export function sendServiceError(
  request: FastifyRequest,
  reply: FastifyReply,
  result: Extract<ServiceResult<unknown>, { ok: false }>,
) {
  const message = SERVICE_ERROR_MESSAGES[result.error] ?? result.error;
  return reply.code(result.status).send(errorBody(request, result.error, message));
}
```

to:

```ts
export function sendServiceError(
  request: FastifyRequest,
  reply: FastifyReply,
  result: Extract<ServiceResult<unknown>, { ok: false }>,
) {
  const message = result.message ?? SERVICE_ERROR_MESSAGES[result.error] ?? result.error;
  return reply.code(result.status).send(errorBody(request, result.error, message));
}
```

(The `guard_failed` entry in the map is now only a fallback for the case where `message` isn't set; `CrudService.transition` always sets it to the guard's specific reason.)

- [ ] **Step 3: Write the failing integration tests**

Append to `src/core/crud/crud-service.test.ts` (after the closing `});` of the existing `describe("CrudService.update (live DB)", ...)` block, i.e. after line 264), a second top-level describe block:

```ts
describe("CrudService.transition (live DB)", () => {
  let container: AppContainer;
  let tmpDir: string;
  let pgClient: Client;
  let dbAvailable = true;

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

    tmpDir = mkdtempSync(path.join(tmpdir(), "metap-crud-transition-test-"));
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
        `Skipping CrudService.transition live-DB tests: could not connect to ${databaseUrl}: ${
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

  async function cleanup(recordId: string) {
    await pgClient.query("DELETE FROM workflow_events WHERE record_id = $1", [recordId]);
    await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [recordId]);
    await pgClient.query("DELETE FROM records WHERE id = $1", [recordId]);
  }

  it("executes a valid transition: updates state/status/version, logs the event, emits the outbox event", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T001", name: "Acme", email: "acme@example.com" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "activate",
        created.data.version,
        context,
      );

      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.data.version).toBe(created.data.version + 1);
        expect(result.data.status).toBe("active");
        expect((result.data.data as { status?: string }).status).toBe("active");
      }

      const events = await pgClient.query<{ action: string; from_state: string; to_state: string }>(
        "SELECT action, from_state, to_state FROM workflow_events WHERE record_id = $1",
        [created.data.id],
      );
      expect(events.rows).toEqual([
        { action: "activate", from_state: "draft", to_state: "active" },
      ]);

      const outboxRows = await pgClient.query<{ topic: string }>(
        "SELECT topic FROM outbox_events WHERE aggregate_id = $1 AND topic = $2",
        [created.data.id, "crm.customers.workflow.transitioned"],
      );
      expect(outboxRows.rows).toHaveLength(1);
    } finally {
      await cleanup(created.data.id);
    }
  });

  it("rejects a transition that is not valid from the current state", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T002", name: "Beta", email: "beta@example.com" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "block",
        created.data.version,
        context,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(409);
        expect(result.error).toBe("invalid_transition");
      }
    } finally {
      await cleanup(created.data.id);
    }
  });

  it("rejects a transition blocked by a guard and surfaces the guard's reason", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T003", name: "Gamma" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const result = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "activate",
        created.data.version,
        context,
      );

      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(422);
        expect(result.error).toBe("guard_failed");
        expect(result.message).toBe("Email is required to activate a customer.");
      }
    } finally {
      await cleanup(created.data.id);
    }
  });

  it("rejects a transition with a stale version", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const created = await container.crud.create(
      "crm.customers",
      { code: "T004", name: "Delta", email: "delta@example.com" },
      context,
    );
    expect(created.ok).toBe(true);
    if (!created.ok) return;

    try {
      const bumped = await container.crud.update(
        "crm.customers",
        created.data.id,
        created.data.version,
        { name: "Delta Updated" },
        context,
      );
      expect(bumped.ok).toBe(true);

      const stale = await container.crud.transition(
        "crm.customers",
        created.data.id,
        "activate",
        created.data.version,
        context,
      );

      expect(stale.ok).toBe(false);
      if (!stale.ok) {
        expect(stale.status).toBe(409);
        expect(stale.error).toBe("version_conflict");
      }
    } finally {
      await cleanup(created.data.id);
    }
  });
});
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: FAIL — `container.crud.transition` does not exist (TypeScript compile error).

- [ ] **Step 5: Implement `CrudService.transition`**

In `src/core/crud/crud-service.ts`, first fix the now-stale comment inside `update` (lines 165-170), changing:

```ts
    if (entity.workflow) {
      // The state field can never change through this path, so the top-level `status`
      // column (mirrored from data[stateField] only by `create`) can't go out of sync
      // here and is intentionally never recomputed.
      mergedData[entity.workflow.stateField] = existingData[entity.workflow.stateField];
    }
```

to:

```ts
    if (entity.workflow) {
      // The state field can never change through this path — only `create` and
      // `transition` are allowed to move it — so it's always reset to its existing value.
      mergedData[entity.workflow.stateField] = existingData[entity.workflow.stateField];
    }
```

Then add a new `transition` method, placed after `update` (after line 218's closing `}`) and before `flushOutbox`:

```ts
  async transition(
    entityName: string,
    id: string,
    action: string,
    expectedVersion: number,
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

    if (!entity.workflow) {
      return { ok: false, status: 400, error: "no_workflow" };
    }

    const existingData = existing.data as Record<string, unknown>;
    const fromState = existingData[entity.workflow.stateField];

    if (typeof fromState !== "string") {
      return { ok: false, status: 409, error: "invalid_transition" };
    }

    const transition = this.workflow.findTransition(entity, action, fromState);

    if (!transition) {
      return { ok: false, status: 409, error: "invalid_transition" };
    }

    const guardResult = this.workflow.runGuard(transition, existingData, context);

    if (guardResult !== true) {
      return { ok: false, status: 422, error: "guard_failed", message: guardResult };
    }

    const nextData: Record<string, unknown> = {
      ...existingData,
      [entity.workflow.stateField]: transition.to,
    };

    const outcome = await this.db.client.transaction(async (tx) => {
      const updatedRows = await tx
        .update(records)
        .set({
          data: nextData,
          status: transition.to,
          version: sql`${records.version} + 1`,
          updatedAt: new Date(),
          updatedBy: context.userId,
        })
        .where(
          and(
            eq(records.id, id),
            eq(records.tenantId, context.tenantId),
            eq(records.entity, entity.name),
            eq(records.version, expectedVersion),
            eq(records.deleted, false),
          ),
        )
        .returning();

      const record = updatedRows[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.recordEvent(
        tx,
        entity,
        record.id,
        action,
        fromState,
        transition.to,
        context,
      );
      await this.workflow.emitTransitioned(
        tx,
        entity,
        record.id,
        action,
        fromState,
        transition.to,
        context.userId,
      );

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 409, error: "version_conflict" };
    }

    return { ok: true, data: outcome.record };
  }
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `pnpm vitest run src/core/crud/crud-service.test.ts`
Expected: PASS (all tests in both describe blocks — 5 existing + 4 new = 9).

- [ ] **Step 7: Typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: no errors.

---

### Task 5: `POST /api/:entity/:id/transitions/:action` route

**Files:**
- Modify: `src/server/routes/records.ts` (full file)

**Interfaces:**
- Consumes: `CrudService.transition` (Task 4).
- Produces: nothing consumed by later tasks (this is the last task).

- [ ] **Step 1: Add the route**

In `src/server/routes/records.ts`, add a new schema after `UpdateParamsSchema` (after line 24):

```ts
const TransitionBodySchema = z.object({
  version: z.number().int().positive(),
});

const TransitionParamsSchema = z.object({
  entity: z.string(),
  id: z.string().uuid(),
  action: z.string(),
});
```

Then add a new route inside `registerRecordRoutes`, after the `PATCH` route (after line 106's closing `);`, before the function's closing `}`):

```ts
  app.post<{
    Params: { entity: string; id: string; action: string };
    Body: z.infer<typeof TransitionBodySchema>;
  }>(
    "/api/:entity/:id/transitions/:action",
    {
      schema: {
        params: zodToJsonSchema(TransitionParamsSchema),
        body: zodToJsonSchema(TransitionBodySchema),
      },
    },
    async (request, reply) => {
      const params = TransitionParamsSchema.parse(request.params);
      const body = TransitionBodySchema.parse(request.body);
      const result = await container.crud.transition(
        params.entity,
        params.id,
        params.action,
        body.version,
        request.context,
      );

      if (!result.ok) {
        return sendServiceError(request, reply, result);
      }

      return { data: result.data };
    },
  );
```

- [ ] **Step 2: Typecheck**

Run: `pnpm typecheck`
Expected: no errors.

- [ ] **Step 3: Manually verify against the dev server**

Run: `pnpm dev` (in the background/a separate terminal), then with a valid dev JWT (`pnpm mint-token`) and a customer record created via `POST /api/crm.customers`:

```bash
# happy path
curl -s -X POST http://localhost:3000/api/crm.customers/<id>/transitions/activate \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"version": 1}'
# expect 200, data.data.status === "active"

# guard failure (create a customer with no email first)
curl -s -X POST http://localhost:3000/api/crm.customers/<no-email-id>/transitions/activate \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"version": 1}'
# expect 422, error.code === "guard_failed", error.message === "Email is required to activate a customer."

# invalid transition (call block on a still-draft customer)
curl -s -X POST http://localhost:3000/api/crm.customers/<draft-id>/transitions/block \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"version": 1}'
# expect 409, error.code === "invalid_transition"
```

Confirm all three responses match. Stop the dev server afterward.

- [ ] **Step 4: Full test suite**

Run: `pnpm test`
Expected: all tests pass (including the pre-existing `app.test.ts`, `query-planner.test.ts`, etc. — nothing in this plan should have broken them).

---

## Plan Self-Review Notes

- **Spec coverage:** §1 guards → Task 2. §2 workflow_events → Task 1. §3 engine methods → Task 3. §4 CrudService.transition → Task 4. §5 route → Task 5. §6 error handling → Task 4 (bundled with the `ServiceResult.message` change it depends on) + Task 5's manual verification. §7 testing → Task 3 (engine unit tests) + Task 4 (integration tests). Open items (stale comment, `DbExecutor` threading) → both folded into Task 4/3 respectively.
- **No placeholders:** every step has literal code, no "add appropriate handling" language.
- **Type consistency checked:** `findTransition`/`runGuard`/`recordEvent`/`emitTransitioned` signatures in Task 3 match their call sites in Task 4 exactly (same parameter order/types). `ServiceResult.message` (Task 4, Step 1) matches its usage in `sendServiceError` (Task 4, Step 2) and in `CrudService.transition`'s `guard_failed` return (Task 4, Step 5).
