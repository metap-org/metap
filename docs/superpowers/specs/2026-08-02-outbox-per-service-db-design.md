# Outbox Per-Service DB Configurability

Date: 2026-08-02

Status: approved

Scope: third of 4 sub-projects addressing DB-coupling risks (sub-project 1: outbox row locking, sub-project 2: permission storage seam).

## Motivation

Investigating this made clear that `OutboxService.enqueue()` is *already* correctly bound to whichever database a business write's transaction lives in — it takes the caller's own transaction handle (`executor: DbExecutor`), not a database of its own. The one piece that *is* hardcoded to a single shared `Database` is `publishPending()`, which reads/updates `outbox_events` through the connection `OutboxService` was constructed with — today, always the exact same `db` every other service in `createContainer` shares. There's no structural reason it has to be that same connection object, as long as it points at whatever database `outbox_events` actually lives in for a given module — this sub-project makes that configurable instead of implicit.

## Design

`AppConfig` (`config.ts`) gains an optional `outboxDatabaseUrl: z.string().url().optional()`. Not set → outbox reads/writes go through the same connection as everything else (today's behavior, unchanged). Set → `OutboxService` gets its own separate `Database`/connection pool.

`createContainer` (`container.ts`):

```ts
const db = createDatabase(config.databaseUrl);
const outboxDb = config.outboxDatabaseUrl ? createDatabase(config.outboxDatabaseUrl) : db;
...
const outbox = new OutboxService(outboxDb, rabbit);
...
async close() {
  await rabbit.close();
  if (outboxDb !== db) {
    await outboxDb.close();
  }
  await db.close();
},
```

The `outboxDb !== db` check avoids double-closing the same connection pool when `outboxDatabaseUrl` isn't set (the common case today).

**Still true and unchanged:** whatever database `outboxDatabaseUrl` points at must be the *same* database as wherever the business write happens (i.e., matches that module's own `databaseUrl`) — `enqueue()`'s atomicity guarantee comes from the caller's transaction, not from this config, and nothing here changes that. This setting only affects where `publishPending()` reads from; getting it wrong (pointing outbox at a database with no relationship to where business writes land) would just mean `publishPending()` finds nothing to publish, not a correctness break — but it should always be set to match the module's own `databaseUrl` in practice.

## Testing

- Unit test: `createContainer` with `outboxDatabaseUrl` unset → `container.outbox`'s underlying connection is the same `db` object as `container.db` (reference equality, not just same URL) — proves the no-config-change default path doesn't waste a second connection pool.
- Unit test: `createContainer` with `outboxDatabaseUrl` set (even to the same URL, for the test) → a genuinely different `Database` instance is used for outbox, and `close()` closes both without erroring.
- No behavior change to any existing test — every current test constructs `AppConfig` without `outboxDatabaseUrl`, so `OutboxService` keeps getting the shared `db` exactly as today.

## Out of scope

- Actually running two different Postgres instances in this repo's dev/test setup — this sub-project only adds the config seam.
- Sub-project 4 (config-drift enforcement) — separate, next.
