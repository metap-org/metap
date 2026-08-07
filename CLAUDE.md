# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Metap is a metadata-driven ERP/platform core. The core idea: entity behavior (fields, list views, validation, workflow) is declared once as metadata, and CRUD/list/workflow behavior is generated from it — but each concern (metadata, permission, query planning, workflow, outbox) is an explicit service with a fixed boundary, not a grab-bag helper.

After completing the feature, do not commit any changes. Keep the diff intact so I can review it first. Making roadmap stay updated.

**Backend stack (2026-08-07 on): Rust — axum + sqlx + PostgreSQL + RabbitMQ** (outbox pattern for reliable event publishing), replacing the original TypeScript backend (Fastify + Zod + Drizzle) in full — see `docs/rust-core-viability.md` for the decision record and `docs/roadmap.md`'s Phase 12 for status. `docs/why.md` explains the *unchanged* choices (PostgreSQL, RabbitMQ, the outbox pattern) plus the historical TS-era reasoning for what's since been replaced. `docs/architectures/05-building-blocks.md` documents the target layering (start at `docs/architectures/index.md` for the full architecture doc set) — the layering is unchanged by the language migration, only the implementation is. `docs/roadmap.md` tracks phased goals. The frontend stack (`packages/platform-react` + `apps/crm-fe`) is unaffected — React/TypeScript, talks to the backend over HTTP only, was never coupled to the backend's implementation language.

## Monorepo layout

This repo is a pnpm workspace (`pnpm-workspace.yaml`: `packages/*`, `apps/*`) and a Cargo workspace (root `Cargo.toml`), overlapping at `apps/`: `crates/` holds only the Rust **library** (`metap-*`) plus the ops binaries built on it (`outbox-publisher`, `db-migrate`, `dev-tools`); `apps/` holds the **sample/example consumers** of that library, one per language (`apps/crm-server`, a Cargo workspace member; `apps/crm-fe`, a pnpm workspace member) — kept together specifically so the "this is a throwaway example, not the product" signal lives in one place rather than being split by tooling. `packages/` holds the reusable **frontend** library. Commands below are still run from the **repo root**; the root `package.json`'s scripts forward to the right place (`pnpm --filter`/`pnpm -r` for frontend packages, plain `cargo ... -p <crate>` for Rust crates — the workspace `Cargo.toml` at the repo root means no `--manifest-path` is ever needed).

Backend library (`crates/`, a Cargo workspace — see `docs/rust-core-viability.md` for how each crate maps to what it replaced). This is the reusable, entity-agnostic surface — no crate here knows about `crm.customers` or any other business entity:
- **`metap-infra`** — Postgres pool, the `EventBus` trait + `RabbitEventBus` impl, config loading (`AppConfig`, same env vars the old `packages/core` used), outbox enqueue, health check.
- **`metap-metadata`** — `EntityDefinition`/`EntityField`/etc., `MetadataCompiler` (validate + hash), `MetadataRegistry`, the OpenAPI generator (`/metadata/openapi.json`).
- **`metap-permission`** — `PolicyCondition`, `PolicyStore` trait + `PostgresPolicyStore`, `PermissionSnapshot`, `PermissionService`, `PolicyExplainer`.
- **`metap-query`** — `QueryPlanner` (`plan_list`), keyset cursor encode/decode, record-level policy → SQL.
- **`metap-workflow`** — initial-status resolution, transition lookup, guard evaluation (guards are `PolicyCondition`, not functions — see `metap-metadata`'s `WorkflowTransition` doc comment), the `workflow_events` audit write, outbox emits.
- **`metap-crud`** — `CrudService` (list/get/create/update/transition/delete), the field-metadata-driven payload validator (replaces per-entity Zod schemas), record masking/capabilities.
- **`metap-http`** — the axum router: `/api/:entity*`, `/metadata/*`, `/health`, the JWT `AuthContext` extractor.
- **`metap-peripherals`** — index reconciler, metadata drift check, role assignment (grant/revoke/list).
- **`outbox-publisher`** (binary) — the outbox drain/publish worker loop, a separate long-running process from `crm-server`. Ops tooling built on the library above, not itself entity-aware.
- **`db-migrate`** (binary) — applies `crates/migrations/*.sql` via `sqlx::migrate!` to a fresh database. Replaces Drizzle's `db:generate`/`db:migrate`; there is no schema-diff tool anymore — new migrations are written by hand as new numbered `.sql` files in `crates/migrations/`.
- **`dev-tools`** (binary) — `gen-keys`/`mint-token`/`seed-admin` subcommands, replacing the old `packages/core/scripts/*.mjs`.

Sample apps (`apps/`, both a Cargo workspace member and a pnpm workspace — deliberately mixed since both are the same kind of thing: a concrete, throwaway consumer proving the library surface works, not the product being distributed. A downstream project is expected to depend on `crates/metap-*`/`packages/platform-react` directly and write its own equivalent of these, not import `apps/*` itself):
- **`apps/crm-server`** (binary crate `crm-server`) — the actual runnable backend: registers the real `crm.customers` entity (ported from the old `customer.entity.ts`) and wires every `crates/metap-*` crate above into a running server. Owns its own `.env`/`keys/` (see Commands below).
- **`apps/crm-fe`** (`@metap/crm-fe`) — the frontend dev harness (routing, dev-login, entities page) that consumes `packages/platform-react` via `workspace:*`. Not a real app — see "Frontend" below.

Frontend library (pnpm workspace):
- **`packages/platform-react`** (`@metap/platform-react`) — the reusable frontend pieces (api client, generated list/form, field renderers, workflow action bar, record detail).

`apps/crm-server` keeps its **own** `.env`/`.env.example`/`keys/` (dotenv and the JWT key paths resolve relative to whichever directory a binary actually runs from — `pnpm dev:rs`/`mint-token`/etc. all `cd apps/crm-server` first). This mirrors the old TS-era per-package `.env` convention, now collapsed to one location since there's only one backend binary.

## Commands

```bash
pnpm install                              # frontend workspace only (apps/crm-fe, packages/platform-react)
cp apps/crm-server/.env.example apps/crm-server/.env   # full runtime config
docker compose up -d postgres rabbitmq   # postgres exposed on host port 5433, not 5432
pnpm db:migrate                           # apply crates/migrations/*.sql to a fresh DB (sqlx migrate)
pnpm auth:dev-keys                        # generate a dev JWT keypair into apps/crm-server/keys/
pnpm mint-token [tenantId] [userId]       # mint a dev JWT (defaults to fixed dev tenant/user)
pnpm seed:admin <tenantId> <userId>       # grant the 'admin' role
pnpm dev:rs                               # build + run the API (apps/crm-server), port 3000
pnpm start                                # build apps/crm-fe + apps/crm-server, serve both from one process/port
pnpm worker:outbox:rs                     # run the outbox-publisher worker loop
pnpm dev:web                              # run the frontend dev harness (apps/crm-fe), port 5173

pnpm typecheck                            # tsc --noEmit, frontend packages only
pnpm test                                 # vitest run, frontend packages only (no backend TS tests anymore)
pnpm test:rs                              # cargo test --workspace (crates/) — unit tests only, no DB needed
pnpm test:rs:e2e                          # cargo test --workspace -- --ignored — needs DATABASE_URL + a running dev Postgres/RabbitMQ
pnpm lint                                 # recursive, frontend packages only
pnpm format                               # recursive, frontend packages only
pnpm format:check                         # recursive, frontend packages only
```

Run a single Rust test: `cargo test -p <crate> <test_name>` (add `-- --ignored` for e2e tests) from the repo root — the workspace `Cargo.toml` lives at the repo root, not under `crates/`, so no `--manifest-path` is needed.

Node >=24.15.0 for the frontend workspace; a recent stable Rust toolchain for the backend (no pinned MSRV yet). Frontend module system is ESM throughout (`"type": "module"`).

## Architecture

Request flow is strictly layered, enforced by convention rather than tooling — do not shortcut it:

```
axum routes (crates/metap-http/src/routes/*)
  -> application service (crates/metap-crud/src/crud_service.rs)
    -> platform core (metap-metadata / metap-permission / metap-query / metap-workflow)
      -> Postgres (sqlx::PgPool, injected directly — no repository abstraction; see
         docs/architecture-review-2026-08-07.md Part 1's Repository finding for why)
      -> outbox (metap-infra::outbox::enqueue) -> RabbitMQ (metap-infra::EventBus)
```

There's no single DI container the way `packages/core/src/core/container.ts` used to be — `CrudService::new(pool, metadata, permissions)` takes its dependencies directly, and `apps/crm-server/src/main.rs` does the wiring inline (connect DB, register entities, build `PermissionService`, build `AppState`, build the router, serve). Routes receive `AppState` (an axum `State` extractor) and call into `CrudService`; they never touch `sqlx`/`lapin` directly.

`metap_http::build_router(state, cors_origins)` takes the registered entities indirectly via `AppState`'s `MetadataRegistry` — it does not know about `crm.customers` or any other business entity. A second business module would be a new binary crate (or a flag/config on `crm-server`) registering its own entities, not a hardcoded entity import into any `metap-*` library crate.

### Metadata-driven records

There is no per-entity database table. All business records live in one generic `records` table (`crates/migrations/*.sql`, originally generated from `packages/core/src/infra/db/schema.ts` before that was removed): tenant/entity/status/code columns plus a `data jsonb` column for the metadata-driven fields, with a `version` column reserved for optimistic locking. Entities are defined as `EntityDefinition` values (`crates/metap-metadata/src/entity.rs`) — see `apps/crm-server/src/customer_entity.rs` for the pattern: field/list-view/workflow metadata, no separate validation-schema object (see `metap-crud/src/validation.rs`'s doc comment for why) — and registered into `MetadataRegistry` by whichever binary owns them (see `crm-server/src/main.rs`), not inside any `metap-*` library crate. Adding a new business entity means adding a new entity-definition module and registering it in the owning binary's `main.rs`, not creating a new table or route by hand.

The roadmap (`docs/roadmap.md`, Data Model Strategy in `docs/architectures/05-building-blocks.md`) explicitly plans to peel off dedicated typed tables for high-volume or accounting-critical modules later — the generic JSONB table is a deliberate starting point, not an oversight.

### Core services and their fixed boundaries

- **`MetadataRegistry`** (`metap-metadata`) — owns entity definitions (fields, list views, workflow). Read-only after boot, populated once by whichever binary registers entities.
- **`CrudService`** (`metap-crud`) — the only thing routes call for record operations. Orchestrates: permission check -> field-metadata-driven validation -> query planning -> DB write -> workflow status assignment -> outbox enqueue.
- **`PermissionService`** (`metap-permission`) — tenant scope, RBAC/ABAC, field- and record-level permission. Real implementation (not a scaffold) — policies are stored via the `PolicyStore` trait (`PostgresPolicyStore` is the only impl today).
- **`QueryPlanner`** (`metap-query`, `plan_list`) — the *only* place list/filter/sort queries are turned into SQL. Every list has a max limit, every query is tenant-scoped, filter/sort fields must come from entity metadata (never arbitrary client-supplied operators).
- **Workflow functions** (`metap-workflow`) — metadata-driven state machine (state field, initial state, terminal states, transitions). Fully implemented: initial-status resolution, transition lookup, guard evaluation (a `PolicyCondition`, not a function), the audit log, and outbox emits on transition/create/update/delete.
- **`EventBus`** (`metap-infra`) — a trait (`RabbitEventBus` is the only impl) events are published through; `metap-infra::outbox::enqueue` writes to the `outbox_events` table in the same transaction as the business write (outbox pattern), so RabbitMQ downtime can't lose events. `outbox-publisher` is the separate long-running process that drains and publishes them — a distinct binary from `crm-server`, not a background task inside it.

### Boundaries to preserve

From `docs/architectures/05-building-blocks.md`, still true of the current code and worth enforcing in review:

- Route/handler code must not import `sqlx`/`lapin` directly — go through `CrudService`/`metap-infra`'s `EventBus`.
- Frontend/client query input must not map directly to SQL operators — it goes through `QueryPlanner`, constrained by entity metadata.
- Workflow side effects are emitted through the outbox, never published to RabbitMQ directly from a service.
- Every business route assumes tenant scope and real auth (JWT + a live `user_roles` lookup per request — no defaulting, no caching roles on the token).
- No `metap-*` library crate gets business-entity knowledge — that's `crm-server`'s job (or a future second binary's).

## Frontend

`apps/crm-fe` (`@metap/crm-fe`) is a real pnpm workspace member (Vite + React + TypeScript), consuming `packages/platform-react` via `workspace:*` — install/run it as part of the normal workspace `pnpm install`, then `pnpm dev:web` (serves on `http://localhost:5173`, proxying `/api`, `/metadata`, `/health` to the backend on port 3000 — unaffected by the backend's language, it's still plain HTTP). It's still a temporary dev harness, not a real app: `packages/platform-react` holds the reusable pieces (api-client, metadata-client, auth context, generated list/form, field renderers, workflow action bar) a future downstream project would import; `apps/crm-fe/src/demo/` holds throwaway demo pages that exercise them.

There's no real login yet — the backend is verify-only. Run `pnpm mint-token` (repo root, requires `pnpm auth:dev-keys` to have been run once) to mint a JWT, then paste it into the `/dev-login` screen the frontend redirects to when there's no token. The token lives only in memory (React state) and is lost on refresh — that's deliberate, not a bug.

### Metadata types stay generated, not hand-written

`packages/platform-react/src/metadata/types.ts` (`EntityField`/`EntityWorkflow`/`EntitySummary`/etc.) is a thin façade over `packages/platform-react/src/metadata/generated-types.ts`, which is generated — never hand-edit `generated-types.ts` directly. After a backend meta-model change (a new `EntityField` property, etc.), start the backend (`pnpm dev:rs`) and run `pnpm --filter @metap/platform-react generate:types`, then commit the regenerated file. The source of truth for what's in the generated types is `crates/metap-metadata/src/openapi.rs`'s hand-written `EntitySummary` JSON Schema (there's no Zod-equivalent reflection step in Rust, so this is maintained by hand rather than derived — see that file's doc comment; it must stay in sync with `entity.rs`'s structs). `GET /metadata/openapi.json` is intentionally public (no auth) so this codegen step can run without a minted token — it only describes API shape (entity/field names and kinds), never tenant data.
