# DB Connection Boot Visibility + Fail-Fast Schema Check

Date: 2026-08-02

Status: approved

Scope: fourth of 4 sub-projects addressing DB-coupling risks (sub-project 1: outbox row locking, sub-project 2: permission storage seam, sub-project 3: outbox per-service DB config). Scoped down from "config-drift enforcement" to what's actually achievable by application code alone — see Motivation.

## Motivation

The original ask was preventing `apps/<module>`'s `.env` from silently pointing at a different Postgres instance than every other module. Investigating this: **no application-code mechanism can enforce that across independently-deployed processes** — process A (`apps/crm`) has no way to observe process B's (a future module's) `DATABASE_URL` to cross-check against. That's a deployment/secrets-management concern (same category as the already-documented "no production deployment topology" risk), not something `packages/core` can solve alone.

What's real and buildable: today, `createDatabase()` connects silently — nothing logs *which* database a process actually connected to, and nothing checks that the connected database looks like a real, migrated `packages/core` database before the app starts serving requests. A misconfigured `DATABASE_URL` (wrong host, wrong database name, an unmigrated database) currently fails downstream, confusingly, on the first query that touches a missing table — not immediately, clearly, at boot.

## Design

**Connection visibility** — `createDatabase(databaseUrl)` (`infra/db/client.ts`) logs the database it connected to (host, port, database name — never credentials) via `console.log`, using a new small helper:

```ts
export function describeDatabaseUrl(databaseUrl: string): string {
  const url = new URL(databaseUrl);
  const dbName = url.pathname.replace(/^\//, "");
  return `${url.hostname}:${url.port || "5432"}/${dbName}`;
}
```

**Fail-fast schema check** — new `assertCoreSchemaPresent(db: Database): Promise<void>` (`infra/db/schema-check.ts`), querying `information_schema.tables` for the tables `packages/core`'s migrations create (`records`, `policies`, `outbox_events`, `workflow_events`, `metadata_versions`, `user_roles`) and throwing a clear, actionable error naming exactly which are missing if any aren't found — pointing at the likely cause ("did `DATABASE_URL` point at an unmigrated or wrong database? Run `pnpm db:migrate`").

**Not baked into `createContainer`** — deliberately. `createContainer` is called by every test file (17+ of them) that already assumes a migrated test database; adding a schema check there would slow every test down for no real benefit (tests already know their DB state). Instead, `apps/<module>`'s own `main.ts` (and worker entry points) call `assertCoreSchemaPresent(container.db)` explicitly, right after `createContainer(config)`, before `buildApp`/`listen()` — this is where a real fail-fast check at actual boot time belongs, not inside the library's general-purpose constructor.

## Testing

TDD:
- `describeDatabaseUrl` — unit test, no DB needed: given a URL with credentials, asserts the returned string contains host/port/dbname and never the username/password.
- `assertCoreSchemaPresent` — live-DB test: resolves without throwing against the real (migrated) test database; throws with a message naming the missing table(s) when pointed at a `Database` connected to a schema lacking one of the required tables (create a throwaway Postgres schema/search_path with none of the tables, or drop-and-recreate a scoped check — whichever is simplest to set up reliably in a test transaction/rollback).

## Out of scope

- True cross-process enforcement (see Motivation) — not achievable by code alone.
- Wiring `assertCoreSchemaPresent` into `apps/crm/src/main.ts` is part of this sub-project's implementation (the whole point is a real app actually calling it), but wiring it into hypothetical *future* `apps/<module>` is obviously not — each new module's `main.ts` follows the same one-line pattern when it's created.
