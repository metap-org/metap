# Outbox Transaction Atomicity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `CrudService.create`/`update` write the business record and its outbox event in a single Postgres transaction, so a failure between the two writes can never leave an orphaned record with a permanently lost event.

**Architecture:** Introduce a shared `DbExecutor` type (the same type Drizzle uses for both its pool-level client and a transaction's `tx` handle), thread it as a required parameter through `OutboxService.enqueue` and `WorkflowEngine.emitCreated`/`emitUpdated`, and wrap the write + emit call in `CrudService.create`/`update` inside `this.db.client.transaction(...)`.

**Tech Stack:** Drizzle ORM (`drizzle-orm/node-postgres`), PostgreSQL, Vitest (live-DB integration test against the same Postgres the dev environment already runs on port 5433).

## Global Constraints

- The executor parameter is **required**, never optional with a silent fallback — this is a deliberate strictness choice so a future call site can't forget to pass the transaction and silently reintroduce this exact bug.
- Entity lookup, permission checks, Zod validation, and (for `update`) the initial fetch-and-merge of the existing record stay **outside** the transaction — they don't write, and `update`'s optimistic-lock correctness comes from the `WHERE version = expectedVersion` clause on the transactional `UPDATE` itself, not from transaction scope. Keep the transaction scoped to just the write + outbox insert.
- `OutboxService.publishPending()` (the separate outbox-publisher worker) is out of scope — it only reads/updates already-committed `outbox_events` rows, unrelated to this fix.
- No new unit-test files for `WorkflowEngine` or `OutboxService` in isolation — neither has one today; the fix is exercised end-to-end through `CrudService`'s existing live-DB test file, matching that file's established convention (real Postgres via `container.crud`, a raw `pg.Client` for direct assertions, `ctx.skip()` if the DB is unreachable).
- Database URL for tests: `process.env.DATABASE_URL ?? "postgres://metap:metap@localhost:5433/metap"` (port 5433, not 5432 — already established in the test file).

---

### Task 1: Transactional outbox write, threaded through all three layers

**Files:**
- Modify: `src/infra/db/client.ts` (add the `DbExecutor` type export)
- Modify: `src/core/outbox/outbox-service.ts` (`enqueue` takes an executor)
- Modify: `src/core/workflow/workflow-engine.ts` (`emitCreated`/`emitUpdated` take an executor, forward it)
- Modify: `src/core/crud/crud-service.ts` (`create`/`update` wrap the write + emit in one transaction)
- Test: `src/core/crud/crud-service.test.ts` (new atomicity test in the existing `describe` block)

**Interfaces:**
- Produces: `export type DbExecutor = Database["client"]` from `src/infra/db/client.ts` — the type every layer below uses for its executor parameter.
- Produces: `OutboxService.enqueue(executor: DbExecutor, event: OutboxEvent): Promise<void>` (was `enqueue(event: OutboxEvent)`).
- Produces: `WorkflowEngine.emitCreated(executor: DbExecutor, entity: EntityDefinition, recordId: string, data: Record<string, unknown>): Promise<void>` (was `emitCreated(entity, recordId, data)`).
- Produces: `WorkflowEngine.emitUpdated(executor: DbExecutor, entity: EntityDefinition, recordId: string, data: Record<string, unknown>, version: number): Promise<void>` (was `emitUpdated(entity, recordId, data, version)`).
- Consumes: `Database` type and `records`/`outboxEvents` Drizzle table objects (all pre-existing, unchanged).

This is one task, not several: the type, the three signature changes, and the transaction wiring only compile and only mean anything together — a reviewer can't sensibly approve "add the type" separately from "use it in a transaction."

#### Step 1: Write the failing test

Add this test inside the existing `describe("CrudService.update (live DB)", ...)` block in `src/core/crud/crud-service.test.ts`, after the last `it(...)` block (the "does not allow updating a record scoped to a different tenant" test, currently ending at line 225) and before the closing `});` of the `describe` block.

First, add `vi` to the vitest import at the top of the file (currently `import { afterAll, beforeAll, describe, expect, it } from "vitest";`):

```ts
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
```

Then add the new test:

```ts
  it("rolls back the record insert when the outbox write fails", async (ctx) => {
    if (!dbAvailable) {
      ctx.skip();
      return;
    }

    const enqueueSpy = vi
      .spyOn(container.outbox, "enqueue")
      .mockImplementationOnce(async () => {
        throw new Error("simulated outbox failure");
      });

    try {
      await expect(
        container.crud.create(
          "crm.customers",
          { code: "U005", name: "Rollback Test" },
          context,
        ),
      ).rejects.toThrow("simulated outbox failure");

      const row = await pgClient.query("SELECT id FROM records WHERE code = $1", ["U005"]);
      expect(row.rows).toHaveLength(0);
    } finally {
      enqueueSpy.mockRestore();
      const leftover = await pgClient.query<{ id: string }>(
        "SELECT id FROM records WHERE code = $1",
        ["U005"],
      );
      for (const leftoverRow of leftover.rows) {
        await pgClient.query("DELETE FROM outbox_events WHERE aggregate_id = $1", [
          leftoverRow.id,
        ]);
      }
      await pgClient.query("DELETE FROM records WHERE code = $1", ["U005"]);
    }
  });
```

This spies on `enqueue` so it throws regardless of its exact signature (before or after this task's fix) — the test's job is to prove the *record* insert rolls back when the outbox write fails, not to test any particular method signature. The `finally` block cleans up defensively: if the fix isn't applied yet (this step is deliberately run before the fix), the record *will* still exist and the cleanup deletes it; once the fix lands, the `SELECT` in `finally` finds nothing and the loop is a no-op.

#### Step 2: Run the test to verify it fails

Run: `pnpm vitest run src/core/crud/crud-service.test.ts -t "rolls back the record insert"`
Expected: FAIL — the new test's `expect(row.rows).toHaveLength(0)` assertion fails because, on current (unfixed) code, the `records` insert already committed as its own statement before `emitCreated`/`enqueue` ever ran, so the row exists despite the simulated outbox failure. (The `rejects.toThrow(...)` half of the assertion passes even on unfixed code — the visible failure is specifically the leftover row.)

This confirms the test actually detects the bug described in the spec, not just a signature change.

#### Step 3: Add the `DbExecutor` type

In `src/infra/db/client.ts`, add this line after the existing `export type Database = ReturnType<typeof createDatabase>;`:

```ts
export type DbExecutor = Database["client"];
```

Full resulting file:

```ts
import { drizzle } from "drizzle-orm/node-postgres";
import pg from "pg";
import * as schema from "./schema";

const { Pool } = pg;

export function createDatabase(databaseUrl: string) {
  const pool = new Pool({ connectionString: databaseUrl });
  const client = drizzle(pool, { schema });

  return {
    client,
    pool,
    async close() {
      await pool.end();
    },
  };
}

export type Database = ReturnType<typeof createDatabase>;
export type DbExecutor = Database["client"];
```

#### Step 4: Make `OutboxService.enqueue` require an executor

Replace `src/core/outbox/outbox-service.ts`'s `enqueue` method. Full resulting file:

```ts
import { and, eq, isNull, sql } from "drizzle-orm";
import type { DbExecutor, Database } from "../../infra/db/client";
import { outboxEvents } from "../../infra/db/schema";
import type { RabbitPublisher } from "../../infra/messaging/rabbitmq";

export type OutboxEvent = {
  topic: string;
  aggregateType: string;
  aggregateId: string;
  payload: unknown;
};

export class OutboxService {
  constructor(
    private readonly db: Database,
    private readonly rabbit: RabbitPublisher,
  ) {}

  async enqueue(executor: DbExecutor, event: OutboxEvent) {
    await executor.insert(outboxEvents).values(event);
  }

  async publishPending(limit = 100) {
    const pending = await this.db.client
      .select()
      .from(outboxEvents)
      .where(isNull(outboxEvents.publishedAt))
      .orderBy(outboxEvents.createdAt)
      .limit(limit);

    for (const event of pending) {
      try {
        await this.rabbit.publish(event.topic, event.payload);
        await this.db.client
          .update(outboxEvents)
          .set({ publishedAt: new Date(), lastError: null })
          .where(eq(outboxEvents.id, event.id));
      } catch (error) {
        await this.db.client
          .update(outboxEvents)
          .set({
            attempts: sql`${outboxEvents.attempts} + 1`,
            lastError: error instanceof Error ? error.message : String(error),
          })
          .where(and(eq(outboxEvents.id, event.id), isNull(outboxEvents.publishedAt)));
      }
    }
  }
}
```

(`publishPending` is untouched — it still uses `this.db.client` directly, correctly, since it's not part of any create/update transaction.)

#### Step 5: Thread the executor through `WorkflowEngine`

Replace `src/core/workflow/workflow-engine.ts` in full:

```ts
import type { DbExecutor } from "../../infra/db/client";
import type { EntityDefinition } from "../metadata/entity";
import type { OutboxService } from "../outbox/outbox-service";

export class WorkflowEngine {
  constructor(private readonly outbox: OutboxService) {}

  getInitialStatus(entity: EntityDefinition, data: Record<string, unknown>) {
    if (!entity.workflow) {
      return undefined;
    }

    const explicitStatus = data[entity.workflow.stateField];
    return typeof explicitStatus === "string" ? explicitStatus : entity.workflow.initialState;
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

#### Step 6: Wrap the write + emit in a transaction in `CrudService`

In `src/core/crud/crud-service.ts`, replace the body of `create` from the `const inserted = await this.db.client...` line through `return { ok: true, data: record };` (currently lines 93-114) with:

```ts
    const outcome = await this.db.client.transaction(async (tx) => {
      const inserted = await tx
        .insert(records)
        .values({
          tenantId: context.tenantId,
          entity: entity.name,
          code: typeof data.code === "string" ? data.code : null,
          status,
          data,
          createdBy: context.userId,
          updatedBy: context.userId,
        })
        .returning();

      const record = inserted[0];

      if (!record) {
        return { ok: false as const };
      }

      await this.workflow.emitCreated(tx, entity, record.id, data);

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 500, error: "insert_failed" };
    }

    return { ok: true, data: outcome.record };
```

Replace the body of `update` from the `const updatedRows = await this.db.client...` line through `return { ok: true, data: record };` (currently lines 173-201) with:

```ts
    const outcome = await this.db.client.transaction(async (tx) => {
      const updatedRows = await tx
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

      await this.workflow.emitUpdated(tx, entity, record.id, data, record.version);

      return { ok: true as const, record };
    });

    if (!outcome.ok) {
      return { ok: false, status: 409, error: "version_conflict" };
    }

    return { ok: true, data: outcome.record };
```

Nothing else in `crud-service.ts` changes — `list()` doesn't write, and the entity lookup / permission check / validation / existing-record fetch in both `create` and `update` stay exactly as they are today, above the transaction.

#### Step 7: Run the test to verify it passes

Run: `pnpm vitest run src/core/crud/crud-service.test.ts -t "rolls back the record insert"`
Expected: PASS — the simulated outbox failure now rolls back the `records` insert too, so the `SELECT` in the test finds zero rows.

#### Step 8: Run the full test suite and typecheck

Run: `pnpm test`
Expected: all test files pass, including the 4 pre-existing tests in `crud-service.test.ts` and the new one (24 existing + 1 new = 25 total across the suite, all passing).

Run: `pnpm typecheck`
Expected: no *new* errors. (The pre-existing, unrelated `src/infra/messaging/rabbitmq.ts` errors predate this plan and are not something this task fixes.)

#### Step 9: Commit

```bash
git add src/infra/db/client.ts src/core/outbox/outbox-service.ts src/core/workflow/workflow-engine.ts src/core/crud/crud-service.ts src/core/crud/crud-service.test.ts
git commit -m "Wrap record write and outbox insert in one transaction"
```
