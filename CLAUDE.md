# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Metap is a metadata-driven ERP/platform core. The core idea: entity behavior (fields, list views, validation, workflow) is declared once as metadata, and CRUD/list/workflow behavior is generated from it — but each concern (metadata, permission, query planning, workflow, outbox) is an explicit service with a fixed boundary, not a grab-bag helper.

After completing the feature, do not commit any changes. Keep the diff intact so I can review it first. Making roadmap stay updated

Stack: Fastify + Zod + Drizzle ORM + PostgreSQL + RabbitMQ (outbox pattern for reliable event publishing). Read `docs/why.md` for the reasoning behind each choice and `docs/architectures/05-building-blocks.md` for the target layering (start at `docs/architectures/index.md` for the full architecture doc set) — worth reading in full before making structural changes. `docs/roadmap.md` tracks phased goals.

## Monorepo layout

This repo is a pnpm workspace (`pnpm-workspace.yaml`: `packages/*`, `apps/*`). Every command below is still run from the **repo root**; the root `package.json`'s scripts forward to the right package via `pnpm --filter`/`pnpm -r`, so nobody's daily commands change.

- **`packages/core`** (`@metap/core`) — the entity-agnostic platform library: metadata/permission/query-planner/workflow/outbox services, the generic Fastify app builder (`buildApp`) and generic routes (`/api/:entity`, `/metadata`, `/admin`, auth, health), the DB schema/migrations, and dev utility scripts (`auth:dev-keys`, `mint-token`, `seed:admin`). No business entity knowledge, no `main.ts` of its own, not independently runnable — it's a library other packages import via `workspace:*`.
- **`apps/crm`** (`@metap/crm`) — the one business module today, and the only thing that's actually a runnable Fastify app. Owns `src/modules/crm/customer.entity.ts` (the real `crm.customers` entity) and the thin entry points (`main.ts`, `workers/outbox-publisher.ts`, `workers/reconcile-indexes.ts`) that import `buildApp`/`createContainer`/`runOutboxPublisherLoop` from `@metap/core` and register only its own entities. This is the target shape from `docs/architectures/04-strategy.md`'s "Future Evolution: Multi-Service Split" — see `docs/superpowers/specs/2026-08-02-monorepo-apps-crm-design.md` for how/why it was pulled forward. A second business module would be a new `apps/<module>` alongside this one, not a folder inside `packages/core`.
- **`packages/platform-react`** (`@metap/platform-react`) — the reusable frontend pieces (api client, generated list/form, field renderers, workflow action bar, record detail) — see `docs/superpowers/specs/2026-08-02-monorepo-platform-react-design.md`.
- **`apps/demo`** (`@metap/demo`) — the frontend dev harness (routing, dev-login, entities page) that consumes `packages/platform-react` via `workspace:*`. Not a real app — see "Frontend" below.

Both `packages/core` and `apps/crm` keep their **own** `.env`/`.env.example`/`keys/` (dotenv and the JWT key paths are resolved relative to whichever package's directory a script actually runs in) — `packages/core`'s only needs `DATABASE_URL` (for `db:generate`/`db:migrate`, which operate on the schema regardless of which app is deployed); `apps/crm`'s is the full runtime config for the actual running server/workers. This is acknowledged, accepted duplication at this scale, not an oversight.

## Commands

```bash
pnpm install
cp packages/core/.env.example packages/core/.env   # DATABASE_URL only, for migrations
cp apps/crm/.env.example apps/crm/.env             # full runtime config, for the actual app
docker compose up -d postgres rabbitmq   # postgres exposed on host port 5433, not 5432
pnpm db:generate                          # generate Drizzle migration from schema.ts (packages/core)
pnpm db:migrate                           # apply migrations (packages/core)
pnpm dev                                  # run the API (apps/crm) with tsx watch
pnpm dev:web                              # run the frontend dev harness (apps/demo), port 5173
pnpm worker:outbox                        # run the outbox-publisher worker loop (apps/crm)

pnpm typecheck                            # tsc --noEmit, recursive across every package
pnpm test                                 # vitest run, recursive (only packages/core has tests today)
pnpm lint                                 # recursive
pnpm lint:fix                             # packages/core only
pnpm format                               # recursive
pnpm format:check                         # recursive
pnpm db:studio                            # Drizzle Studio
```

Run a single test file: `pnpm --filter @metap/core exec vitest run path/to/file.test.ts` (or `cd packages/core && pnpm vitest run path/to/file.test.ts`). There is no dedicated test config file (no `vitest.config.ts`); vitest runs with defaults.

Node >=24.15.0, package manager is pnpm (`packageManager: pnpm@9.15.0`). Module system is ESM throughout (`"type": "module"`).

## Architecture

Request flow is strictly layered, enforced by convention rather than tooling — do not shortcut it:

```
HTTP routes (packages/core/src/server/routes/*)
  -> application services (packages/core/src/core/crud/crud-service.ts)
    -> platform core (metadata / permission / query-planner / workflow)
      -> repositories / Drizzle client (packages/core/src/infra/db)
      -> outbox (packages/core/src/core/outbox) -> RabbitMQ publisher (packages/core/src/infra/messaging)
```

Everything is wired together in `packages/core/src/core/container.ts` (`createContainer`), a plain dependency-injection factory — no framework DI. Routes receive the container and call into services; they never touch Drizzle or RabbitMQ directly. When adding a new service, wire it in the container, not by importing infra directly into a route or another service.

`packages/core/src/server/app.ts`'s `buildApp(config, entities)` takes the entity list as an explicit parameter — it does not know about `crm.customers` or any other business entity. Each `apps/<module>` calls it with its own entities (see `apps/crm/src/main.ts`). Don't reintroduce a hardcoded entity import into `packages/core` — that coupling existed once (pre this session's monorepo restructure) and had to be removed to make the module split possible.

### Metadata-driven records

There is no per-entity database table. All business records live in one generic `records` table (`packages/core/src/infra/db/schema.ts`): tenant/entity/status/code columns plus a `data jsonb` column for the metadata-driven fields, with a `version` column reserved for optimistic locking. Entities are defined as `EntityDefinition` objects (`packages/core/src/core/metadata/entity.ts`) — see `apps/crm/src/modules/crm/customer.entity.ts` for the pattern: a Zod schema plus field/list-view/workflow metadata — and registered into `MetadataRegistry` by whichever `apps/<module>` owns them (see `apps/crm/src/modules/registry.ts`), not inside `packages/core`. Adding a new business entity to an existing module means adding a new `*.entity.ts` file and registering it in that module's own registry, not creating a new table or route by hand.

The roadmap (`docs/roadmap.md`, Data Model Strategy in `docs/architectures/05-building-blocks.md`) explicitly plans to peel off dedicated typed tables for high-volume or accounting-critical modules later — the generic JSONB table is a deliberate starting point, not an oversight.

### Core services and their fixed boundaries

- **`MetadataRegistry`** — owns entity definitions (fields, list views, workflow, validation schema). Read-only registry, populated once at startup by whichever app calls `buildApp`/`createContainer`.
- **`CrudService`** — the only thing routes call for record operations. Orchestrates: permission check -> Zod validation -> query planning -> DB write -> workflow status assignment -> outbox enqueue. Currently uses a hardcoded `defaultContext()` (tenant/user/roles) — this is a known placeholder pending real auth (Phase 1 in the roadmap), not something to silently work around elsewhere.
- **`PermissionService`** — intended home for tenant scope, RBAC/ABAC, field- and record-level permission. Currently allows everything by design (scaffold phase); do not add ad-hoc permission checks elsewhere in anticipation of this — extend this service instead.
- **`QueryPlanner`** — the *only* place list/filter/sort queries are turned into SQL. Rules from `docs/architectures/05-building-blocks.md` that matter for any change here: every list has a max limit, every query is tenant-scoped, filter/sort fields must come from entity metadata (never arbitrary client-supplied operators).
- **`WorkflowEngine`** — metadata-driven state machine (state field, initial state, terminal states, transitions). Currently only assigns initial status and emits a `<entity>.record.created` outbox event on create; transition/guard logic is not yet implemented.
- **`OutboxService`** — writes events to the `outbox_events` table in the same transaction as the business write (outbox pattern), so RabbitMQ downtime can't lose events. `packages/core` exports a reusable `runOutboxPublisherLoop(container)` (the poll/drain/signal-handling loop); each `apps/<module>` has its own thin `workers/outbox-publisher.ts` entry point (run via `pnpm worker:outbox`) that calls it — a separate long-running process from the API server, not a background task inside `main.ts`.

### Boundaries to preserve

From `docs/architectures/05-building-blocks.md`, still true of the current code and worth enforcing in review:

- Module/route code must not import the Drizzle client or RabbitMQ publisher directly — go through `CrudService`/`OutboxService` via the container.
- Frontend/client query input must not map directly to SQL operators — it goes through `QueryPlanner`, constrained by entity metadata.
- Workflow side effects are emitted through the outbox, never published to RabbitMQ directly from a service.
- Every business route assumes tenant scope and (eventually) auth; don't build features that assume a single-tenant world even though `defaultContext()` hardcodes one today.
- `packages/core` stays entity-agnostic — no business entity, no `apps/<module>`-specific code, ever gets added there. It's a real workspace package other apps import, not just an app that CRM happens to live inside of.

## Frontend

`apps/demo` (`@metap/demo`) is a real pnpm workspace member (Vite + React + TypeScript), consuming `packages/platform-react` via `workspace:*` — install/run it as part of the normal workspace `pnpm install`, then `pnpm dev:web` (serves on `http://localhost:5173`, proxying `/api`, `/metadata`, `/health` to the backend on port 3000). It's still a temporary dev harness, not a real app: `packages/platform-react` holds the reusable pieces (api-client, metadata-client, auth context, generated list/form, field renderers, workflow action bar) a future downstream project would import; `apps/demo/src/demo/` holds throwaway demo pages that exercise them.

There's no real login yet — the backend is verify-only. Run `pnpm mint-token` (repo root, requires `pnpm auth:dev-keys` to have been run once) to mint a JWT, then paste it into the `/dev-login` screen the frontend redirects to when there's no token. The token lives only in memory (React state) and is lost on refresh — that's deliberate, not a bug.

### Metadata types stay generated, not hand-written

`packages/platform-react/src/metadata/types.ts` (`EntityField`/`EntityWorkflow`/`EntitySummary`/etc.) is a thin façade over `packages/platform-react/src/metadata/generated-types.ts`, which is generated — never hand-edit `generated-types.ts` directly. After a backend meta-model change (a new `EntityField` property, etc.), start the backend (`pnpm dev`) and run `pnpm --filter @metap/platform-react generate:types`, then commit the regenerated file. The source of truth for what's in the generated types is `packages/core/src/core/metadata/entity-wire-schema.ts` (a Zod schema describing exactly what crosses the wire — deliberately not backend's internal `EntityField`/`WorkflowTransition` types, since e.g. `WorkflowTransition.guard` is a function that never survives `JSON.stringify`). `GET /metadata/openapi.json` is intentionally public (no auth) so this codegen step can run without a minted token — it only describes API shape (entity/field names and kinds), never tenant data.
