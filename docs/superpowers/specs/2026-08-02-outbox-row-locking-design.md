# Outbox Row Locking (`FOR UPDATE SKIP LOCKED`)

Date: 2026-08-02

Status: approved

Scope: first of 4 sub-projects addressing DB-coupling risks found while reviewing `packages/core`'s architecture after the monorepo restructure (see the "brainstorm" discussion this session). The other 3: Permission storage seam, outbox per-service DB configurability, config-drift enforcement.

## Motivation

`OutboxService.publishPending()` selects pending rows and publishes them with no row locking:

```ts
const pending = await this.db.client
  .select()
  .from(outboxEvents)
  .where(isNull(outboxEvents.publishedAt))
  .orderBy(outboxEvents.createdAt)
  .limit(limit);
```

Today only `apps/crm`'s worker calls this. The moment a second module's own `worker:outbox` process runs concurrently — which is exactly what the monorepo restructure (`apps/<module>` per business module) is building toward — both workers poll the *same* `outbox_events` table with no ownership scoping and no locking. Both can select the same pending row, both publish it to RabbitMQ, and only one `UPDATE ... SET published_at` wins — the other's publish already happened. Real risk: duplicate event delivery, silently, the first time two outbox workers ever run at once.

## Design

Wrap the select-and-process cycle in a single transaction using `SELECT ... FOR UPDATE SKIP LOCKED`:

```ts
async publishPending(limit = 100) {
  await this.db.client.transaction(async (tx) => {
    const pending = await tx
      .select()
      .from(outboxEvents)
      .where(isNull(outboxEvents.publishedAt))
      .orderBy(outboxEvents.createdAt)
      .limit(limit)
      .for("update", { skipLocked: true });

    for (const event of pending) {
      try {
        await this.rabbit.publish(event.topic, event.payload);
        await tx
          .update(outboxEvents)
          .set({ publishedAt: new Date(), lastError: null })
          .where(eq(outboxEvents.id, event.id));
      } catch (error) {
        await tx
          .update(outboxEvents)
          .set({
            attempts: sql`${outboxEvents.attempts} + 1`,
            lastError: error instanceof Error ? error.message : String(error),
          })
          .where(and(eq(outboxEvents.id, event.id), isNull(outboxEvents.publishedAt)));
      }
    }
  });
}
```

`SKIP LOCKED` means a second concurrent caller's `SELECT` simply skips rows already locked by the first caller's open transaction, instead of blocking — it gets whatever *other* pending rows exist instead, and if none, an empty batch. The row stays locked (invisible to other `SKIP LOCKED` selects) for the duration of the transaction, which spans the actual RabbitMQ publish — this is the standard, accepted pattern for a Postgres-backed job queue; holding the transaction open during the network call is deliberate, not an oversight, since that's what keeps the row exclusively claimed for the whole publish-then-mark-done cycle.

Per-row error handling is preserved exactly as today (one row's publish failure doesn't stop the batch) — the `try`/`catch` per event is unchanged, just every query inside it now runs against `tx` instead of `this.db.client`.

## Testing

TDD, live-DB integration test in `outbox-service.test.ts` (new file — none exists today):
- Seed several pending `outbox_events` rows directly via SQL.
- Call `Promise.all([service.publishPending(), service.publishPending()])` — two concurrent calls, simulating two workers.
- Spy on the injected `RabbitPublisher.publish` and assert every seeded event's `publish` was called **exactly once** total across both concurrent calls, never twice.
- A second test: an existing single-call regression — `publishPending()` still marks rows `publishedAt` and still records `attempts`/`lastError` on a simulated publish failure, unchanged from today's behavior.

## Out of scope

- Ownership scoping (filtering `outbox_events` by which module/service wrote them) — not needed once locking prevents double-processing; two workers safely splitting a shared table via `SKIP LOCKED` is the standard pattern, no need to partition by module.
- The other 3 sub-projects (Permission storage seam, outbox per-service DB config, config-drift enforcement) — separate, sequential work.
