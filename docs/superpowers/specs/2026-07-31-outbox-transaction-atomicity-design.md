# Outbox Transaction Atomicity

Date: 2026-07-31

Status: approved

Scope: backend only (`src/core/crud`, `src/core/workflow`, `src/core/outbox`, `src/infra/db`). No frontend changes.

## Motivation

The project's own documented invariant (`CLAUDE.md`, `docs/architectures/index.md`) is that `OutboxService` "writes events to the outbox_events table in the same transaction as the business write" — the whole point of the outbox pattern is that RabbitMQ downtime, or any failure between the two writes, can never lose an event or leave the two tables inconsistent.

The current code doesn't do this. `CrudService.create`/`update` issue the `records` insert/update as one Postgres statement (autocommits immediately on the node-postgres driver), then separately call `workflow.emitCreated`/`emitUpdated`, which calls `outbox.enqueue`, which issues a second, independent `outbox_events` insert. If the second write fails for any reason (a transient DB error, a constraint violation, a crash between the two calls), the business record now exists permanently with no corresponding outbox event — the event is lost forever, silently. This was flagged in two prior review passes this session and deferred until now.

## Design

### Executor type (`src/infra/db/client.ts`)

Export `type DbExecutor = Database["client"]`. Drizzle's node-postgres `.transaction()` callback receives a `tx` parameter typed identically to the top-level `db.client` (same `NodePgDatabase` type) — so one type alias covers both "the pool-level client" and "a transaction-scoped client," and callers don't need to know which one they were handed.

### `OutboxService.enqueue` becomes executor-scoped

```ts
async enqueue(executor: DbExecutor, event: OutboxEvent) {
  await executor.insert(outboxEvents).values(event);
}
```

No default parameter, no fallback to `this.db.client` — the executor is required. This is a deliberate strictness choice: an optional executor with a silent fallback is exactly how this bug could reappear later (someone adds a new call site, forgets to pass the transaction, and it compiles and "works" until the two writes actually race). Requiring it makes forgetting a compile error, not a latent bug.

`OutboxService` keeps its `db: Database` constructor dependency — `publishPending()` (the separate outbox-publisher worker) still needs pool-level access for its own reads/updates, unrelated to this transaction.

### `WorkflowEngine.emitCreated`/`emitUpdated` thread the executor through

```ts
async emitCreated(executor: DbExecutor, entity: EntityDefinition, recordId: string, data: Record<string, unknown>) {
  await this.outbox.enqueue(executor, { topic: `${entity.name}.record.created`, ... });
}

async emitUpdated(executor: DbExecutor, entity: EntityDefinition, recordId: string, data: Record<string, unknown>, version: number) {
  await this.outbox.enqueue(executor, { topic: `${entity.name}.record.updated`, ... });
}
```

Same strictness: no default. `getInitialStatus` is unaffected (it does no DB access).

### `CrudService.create`/`update` wrap the write + emit in one transaction

Entity lookup, permission check, Zod validation, and (for `update`) the initial fetch-and-merge of the existing record all stay **outside** the transaction — none of them write, and the `update` path's optimistic-lock correctness already comes from the `WHERE version = expectedVersion` clause on the transactional `UPDATE` itself, not from when the prior `SELECT` happened. Keeping the transaction scoped to just the write + outbox insert avoids holding a Postgres transaction open across unrelated work.

```ts
// create()
const outcome = await this.db.client.transaction(async (tx) => {
  const inserted = await tx.insert(records).values({ ... }).returning();
  const record = inserted[0];
  if (!record) return { ok: false as const };
  await this.workflow.emitCreated(tx, entity, record.id, data);
  return { ok: true as const, record };
});

if (!outcome.ok) return { ok: false, status: 500, error: "insert_failed" };
return { ok: true, data: outcome.record };
```

```ts
// update()
const outcome = await this.db.client.transaction(async (tx) => {
  const updatedRows = await tx.update(records).set({ ... }).where(...).returning();
  const record = updatedRows[0];
  if (!record) return { ok: false as const };
  await this.workflow.emitUpdated(tx, entity, record.id, data, record.version);
  return { ok: true as const, record };
});

if (!outcome.ok) return { ok: false, status: 409, error: "version_conflict" };
return { ok: true, data: outcome.record };
```

The "no row returned" branches (defensive-insert-failed, version-conflict) return a plain sentinel rather than throwing — nothing was written in either case, so there's nothing to roll back; throwing would just be a more expensive way to signal the same "no row" outcome. A genuine failure inside the transaction (e.g. `emitCreated` throwing because the outbox insert violates a constraint) propagates as a rejected promise from `.transaction()`, which Drizzle rolls back automatically — `create()`/`update()` let that propagate uncaught, same as any other unexpected error today (the app's existing error handler turns an uncaught exception into a 500).

### Error handling

No new error handling is introduced. The only behavioral change on the happy path is that both writes now commit or roll back together. On the unhappy path (outbox insert fails), the caller now sees the `records` write rolled back too, surfacing as whatever uncaught-exception handling already exists (500), instead of a silently-orphaned record.

## Out of scope

- `OutboxService.publishPending()` (the separate outbox-publisher worker) — unrelated; it only reads/updates already-committed `outbox_events` rows.
- Any `delete` path — `CrudService` has no delete method yet (the `records.deleted` column exists but nothing writes it).
- Retrofitting this pattern onto future workflow transition logic (state-machine transitions beyond initial-status assignment) — not implemented yet, nothing to fix.

## Testing

One new live-DB test added to `src/core/crud/crud-service.test.ts` (matching that file's existing convention — real Postgres via `container.crud`, `pgClient` for direct assertions, skipped gracefully if the DB is unreachable): spy on `container.outbox.enqueue` to throw once, call `container.crud.create(...)`, assert the call rejects, then query Postgres directly and assert **zero** rows exist in `records` for that record's code — proving the insert was actually rolled back, not just that the promise rejected. No new unit-test files for `WorkflowEngine` or `OutboxService` in isolation, consistent with the project's current test coverage (neither has one today, and the fix is fully exercised end-to-end through `CrudService`).
