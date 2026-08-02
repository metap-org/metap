# Monorepo Restructure, Sub-project 3: `apps/crm` (real module split)

Date: 2026-08-02

Status: approved

Scope: third part of the monorepo restructure (see sub-project 1: `docs/superpowers/specs/2026-08-02-monorepo-packages-core-design.md`, sub-project 2: `docs/superpowers/specs/2026-08-02-monorepo-platform-react-design.md`). Originally deferred during sub-project 1's brainstorming (the backend-split trigger — "the first time a second, genuinely separate module needs to exist as its own deployable unit" — hadn't fired, since only `crm.customers` existed). The user has now explicitly chosen to pull that trigger early, ahead of an actual second business module existing, to get `packages/core` into its real target shape now rather than later.

## Motivation

`docs/architectures/04-strategy.md`'s "Future Evolution: Multi-Service Split" names the target: `packages/core` (shared, entity-agnostic) + `apps/<module>` (one thin app per business module, importing `packages/core`, registering only its own entities). Investigating the current code to plan this split surfaced that `packages/core` isn't actually entity-agnostic today — `src/server/app.ts`'s `buildApp()` internally imports and calls `registerEntities()` from `src/modules/registry.ts`, and `src/main.ts` plus both worker scripts (`outbox-publisher.ts`, `reconcile-indexes.ts`) do the same. This sub-project both moves the CRM module out and fixes that coupling, since the move can't work correctly without it.

## Design

### `packages/core` becomes a pure library (no runnable app of its own)

**Code change:** `buildApp` (`src/server/app.ts`) signature changes from `buildApp(config: AppConfig)` to `buildApp(config: AppConfig, entities: readonly EntityDefinition[])`. It no longer imports `../modules/registry` — the internal `registerEntities(container.metadata)` call becomes `registerEntities(container.metadata, entities)` (reusing the registration helper, just no longer sourcing the entity list itself).

**New reusable exports**, so every future `apps/<module>`'s worker entry points stay ~10 lines instead of re-implementing loop/signal-handling boilerplate:
- `runOutboxPublisherLoop(container: AppContainer, options?: { pollMs?: number; batchSize?: number }): Promise<void>` — extracted from today's `workers/outbox-publisher.ts` (the `while (!closing) { publishPending(); sleep(); }` loop plus `SIGINT`/`SIGTERM` handling), moved into `packages/core/src/workers/outbox-publisher-loop.ts` and exported from the package's public surface.
- Index reconciliation stays as today's `container.indexReconciler.reconcile(entities, log)` — already thin enough that no new wrapper is needed; a caller just passes its own registered entities.

**Removed from `packages/core`:** `src/main.ts`, `src/modules/` (moves to `apps/crm`), the old `src/workers/outbox-publisher.ts`/`src/workers/reconcile-indexes.ts` CLI entry scripts (replaced by the new reusable `runOutboxPublisherLoop` export plus thin entry scripts living in `apps/crm`).

**`packages/core/package.json` script changes:** removes `dev`, `start`, `build`, `worker:outbox`, `index:reconcile` (nothing left to run — no `main.ts`, no build target). Keeps `typecheck`, `test`, `lint`, `format`, `format:check` (still has code to check), and `db:generate`/`db:migrate`/`db:migrate:test`/`db:studio` (the DB schema — `records`/`policies`/`outbox_events`/`workflow_events` — is shared infrastructure, not module-specific, so migrations stay a `packages/core` concern regardless of which `apps/<module>` exist). `auth:dev-keys`/`mint-token`/`seed:admin` also stay — generic JWT/dev-utility scripts, not CRM-specific.

**`packages/core` keeps its own `.env`/`keys/`** (unchanged from sub-project 1) — `drizzle-kit` (`db:generate`/`db:migrate`) still needs `DATABASE_URL` from `packages/core`'s own cwd when those scripts run, independent of any app actually being deployed.

### `apps/crm` (new)

Package name `@metap/crm`, depends on `"@metap/core": "workspace:*"`.

Content:
- `src/modules/crm/customer.entity.ts` and `src/modules/registry.ts` (today's `registerEntities`/`entities` list — CRM-specific, this is genuinely "the one place that knows which business modules are wired in" per its own existing comment) move here as-is.
- New `src/main.ts` (~10 lines): loads config, calls `buildApp(config, entities)` from `@metap/core`, listens.
- New `src/workers/outbox-publisher.ts` (~10 lines): `createContainer`, `registerEntities`, `runOutboxPublisherLoop` (imported from `@metap/core`).
- New `src/workers/reconcile-indexes.ts` (~10 lines): `createContainer`, `registerEntities`, `container.indexReconciler.reconcile(...)`.

`package.json` scripts: `dev` (`tsx watch src/main.ts`), `build` (`tsup src/main.ts ...`, same shape as today's `packages/core` build), `start`, `worker:outbox`, `index:reconcile`. No `typecheck`/`test`/`lint`/`format` scope decision needed beyond matching `packages/core`'s existing setup — `apps/crm` gets the same `tsconfig.json`/`eslint.config.js` shape (own copy, since it's a separate TS project) and the same script set for those.

**`.env`/`.env.example`/`keys/` — new copies, not shared with `packages/core`'s.** `apps/crm` is the actual running server/worker process; it needs its own `DATABASE_URL`/`RABBITMQ_URL`/`PORT`/`HOST`/`CORS_ORIGINS`/`AUTH_JWT_PUBLIC_KEY_PATH`/`AUTH_JWT_PRIVATE_KEY_PATH` resolved from its own cwd (same dotenv/keys cwd-relative behavior established in sub-project 1). This is real, acknowledged duplication between `packages/core/.env` (DB URL only, for migrations) and `apps/crm/.env` (the full runtime config) — acceptable at this scale; centralizing later (e.g. a shared root `.env` referenced via an explicit path) is a future concern, not solved here.

### Root scripts

`dev`, `start`, `worker:outbox`, `index:reconcile`, `auth:dev-keys`, `mint-token`, `seed:admin` — `dev`/`start`/`worker:outbox`/`index:reconcile` now forward to `@metap/crm` instead of `@metap/core` (that's where the runnable code now lives); `auth:dev-keys`/`mint-token`/`seed:admin` stay pointed at `@metap/core` (unchanged, generic dev utilities). `db:*` scripts stay pointed at `@metap/core` (schema/migrations, unchanged). `typecheck`/`test`/`lint`/`format`/`format:check`/`build` stay `pnpm -r <script>` (already recursive since sub-project 2, automatically picks up `apps/crm` once it exists).

## Out of scope (deliberate, not an oversight)

- **Actually deploying `apps/crm` as a separate process from anything else.** This sub-project only makes the *code* independently deployable — no deployment infra (Docker images, process managers, etc.) exists in this repo yet for either the old or new shape, so there's nothing to change there.
- **A real second business module.** This sub-project creates the *shape* for one (`apps/<module>`) without adding a second one — `crm` remains the only module, just correctly isolated now.
- **Centralizing `.env` across `packages/core` and `apps/crm`.** Acknowledged duplication, not solved now (see above).
- **GraphQL gateway / gRPC.** Both still explicitly gated on later triggers per `04-strategy.md`, untouched by this sub-project.

## Testing

Same bar as sub-projects 1-2 — behavior must be identical after the split, since nothing about what the running app *does* should change, only how its code is organized:
- `pnpm -r typecheck` — no errors across `packages/core` and the new `apps/crm`.
- `pnpm -r test` — same 137 tests pass (all currently live in `packages/core/src`, untouched by the split since none of them exercise `main.ts`/the worker CLI entry scripts directly — they call `container.crud`/`container.permissions`/etc. directly, which don't move).
- `pnpm -r lint` / `pnpm -r format:check` — clean in both packages.
- `pnpm dev` (now forwarding to `@metap/crm`) — server starts, `/health` responds, confirms `apps/crm`'s own `.env` resolves correctly and `buildApp(config, entities)` registers `crm.customers` correctly (spot-check via `GET /metadata/entities/crm.customers` returning the entity, and a `POST /api/crm.customers` round-trip).
- `pnpm worker:outbox` (now forwarding to `@metap/crm`) — starts without error, confirms `runOutboxPublisherLoop` imported correctly from `@metap/core`.
