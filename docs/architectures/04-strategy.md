# 4. Solution Strategy

## Fundamental Decisions

1. **Metadata-driven generic CRUD over one `records` table**, instead of a table + route + repository per entity. Declaring an `EntityDefinition` once (fields, list views, validation schema, workflow) is enough to get CRUD, filtering, sorting, permission enforcement, and workflow behavior. See `docs/why.md`'s "Why Keep Metadata-driven Core" and [05. Building Block View](05-building-blocks.md).
2. **Explicit, fixed service boundaries**, not a "helpers as facade" grab-bag. `MetadataRegistry`, `PermissionService`, `QueryPlanner`, `WorkflowEngine`, and `OutboxService` each own one concern and are wired together once, in `src/core/container.ts` — routes and modules never reach around them into Drizzle or RabbitMQ directly (see [02. Constraints](02-constraints.md)). This boundary was fixed from day one, even while some services (e.g. `PermissionService`) were still allow-everything scaffolds — the shape didn't change when the real logic was filled in.
3. **Trigger-based evolution, not speculative infrastructure.** A second deployable service, a dedicated per-entity table, and a report/analytics query path are all designed-for but not built — each has a documented trigger, and none is built ahead of it (Multi-Service Evolution, Data Model Strategy Steps 3-4, Report Query Boundary — see [11. Risks and Technical Debt](11-risks.md)). This mirrors the project's own YAGNI stance: three similar lines beat a premature abstraction.
4. **Metadata is a compiled, validated runtime artifact**, not a passive description. `MetadataCompiler` rejects a broken entity definition at `MetadataRegistry.register()` time — a bad entity module fails at boot, never at the first request that touches it.
5. **The transactional outbox pattern** for every event a business write needs to emit, so a RabbitMQ outage degrades event delivery latency, never API availability or data correctness. See `docs/why.md`'s "Why Outbox Pattern" and [06. Runtime View](06-runtime.md).

## Technology Choices

| Concern | Choice | Why (short) |
|---|---|---|
| HTTP framework | Fastify | Fast, light, low ceremony — see `docs/why.md` |
| Validation | Zod | Familiar, readable, also drives generated OpenAPI/JSON schema |
| ORM | Drizzle | SQL-close, strong TS inference, easy JSONB, no heavy generated client |
| Datastore | PostgreSQL | Transactions, constraints, JSONB for the metadata-driven payload, real indexes |
| Messaging | RabbitMQ + outbox | Reliable integration events without losing them on broker downtime |

Full reasoning for each is in `docs/why.md` — not duplicated here.

## Achieving the Top Quality Goals (see [10. Quality Requirements](10-quality.md) for the full list)

- **Correctness/data integrity** → optimistic locking (`version` column, every `UPDATE ... WHERE version = expected`) + the transactional outbox (business write and its event commit together or not at all).
- **Security** → tenant scope and permission checks happen inside `CrudService`/`QueryPlanner`, server-side, on every call — never trusted from the client, never left to the frontend.
- **Maintainability** → `MetadataCompiler` validation at boot, fixed service boundaries enforced by convention and reviewed for, and a spec-first workflow for every structural change (see [09. Architecture Decisions](09-adr.md)).

## Future Evolution: Multi-Service Split

Metap is the backbone of a low-code platform, not a single-purpose ERP app. `src/core` (metadata, permission, query planner, workflow, outbox) is the reusable core platform. Each business subsystem — CRM, sales, inventory, accounting, and so on — is expected to eventually become its own independently deployable service built on that same core, not a copy of it.

This section is documentation-only: it records the target shape and the triggers for moving toward it. None of it is built yet, and none of it should be built ahead of its trigger (see `docs/superpowers/specs/2026-07-29-multi-service-target-architecture-design.md`, indexed in [09. Architecture Decisions](09-adr.md)).

**Anchor already in place:** `EntityDefinition.name` is already dot-namespaced by domain (`crm.customers`). The prefix before the dot is, in effect, the future service name. No code change is needed for this — just discipline: every new entity module goes under a domain-namespaced name so the future service boundary is legible in the data before any physical split happens.

**Repo/package layout.** Now: stays exactly as it is, one package. Target, once a second module is actually being built: a pnpm workspace with `packages/core` (today's `src/core` plus shared `src/infra`) and `apps/<module>` (one thin Fastify app per service, each importing `packages/core` and registering only its own entity modules). Trigger: the first time a second, genuinely separate module needs to exist as its own deployable unit — not before.

**Data strategy.** Now, and for the foreseeable future: one shared PostgreSQL instance. Design constraint that keeps a later split cheap without being premature now: no code may join across different entities' data directly in SQL — `QueryPlanner` already only ever scopes a query by a single `entity` + `tenantId`, so this costs nothing to state, only to keep enforcing. If a future module needs another module's data, it goes through that module's service-level API — today that's an in-process call into the shared `CrudService`, but it should be treated as a remote call architecturally, so the eventual move to an actual remote call is a non-event. Splitting a module's data out to its own database later then becomes a matter of moving rows and a connection string, not rewriting query logic.

**Protocol strategy.** Now: REST, as already built (JWT auth, structured errors, CRUD with optimistic locking, metadata-constrained filter/sort). Future, triggered by having ≥2 services whose data a single frontend screen needs to aggregate: a GraphQL gateway acting as a BFF (backend-for-frontend) in front of the REST services, composing their responses for the frontend. Future, triggered by the repo/package split above actually having happened: gRPC as an option for service-to-service calls where REST's overhead matters. Neither is built or evaluated further until its trigger is real.

## Future Evolution: Frontend Platform Package

The same "reusable core, many independent consumers" shape from Multi-Service Evolution above applies to the frontend, not just the backend. `web/src/` is already split into `platform/` (api-client, metadata-client, auth context, `GeneratedList`/`FieldValue`, and future `GeneratedForm`/`WorkflowActionBar` — the reusable pieces a future downstream project would import) and `demo/` (throwaway pages that only exist to exercise `platform/` in this repo). That split is the anchor, same role `EntityDefinition.name`'s dot-namespacing plays for the backend.

**Target, once a second real consumer of `web/src/platform/` is actually being built:** publish it as an installable package (mirrors `packages/core` in the backend's own Multi-Service Evolution target above) — `@metap/platform-react` or similar, versioned and consumed via `npm install`, not copy-pasted. Trigger: an actual second app needing to import it — not before.

**Design constraint that keeps that migration cheap without being premature now: `web/src/platform/` stays agnostic about how a consumer is built.** Different downstream projects will have different requirements — one might be a monorepo (Nx/Turborepo/pnpm workspaces), another a micro-frontend (Module Federation, single-spa), another a plain standalone SPA. A shared component/hook library can't dictate that choice without breaking whichever consumers don't fit it. Concretely, this means:

- No global, app-wide client state store (Redux or otherwise) baked into `platform/`. Server state stays in React Query (already the case — it's inherently consumption-agnostic, every consumer just brings its own `QueryClient`); anything else stays component-local state or a narrowly-scoped Context (like `AuthContext` today), never a singleton store that every consumer is forced to share or route around. This matters most for micro-frontend consumers specifically — a baked-in global store is a well-known source of version/singleton conflicts when multiple independently-deployed frontends each expect to own "the" store.
- If a specific downstream app wants Redux, Zustand, or anything else for *its own* app-level state, that's that app's call, made in its own shell (its `demo/`-equivalent) — not something `platform/` imposes on it.
- This costs nothing to keep true today (the current `web/` app already doesn't have a global store), only discipline to keep enforcing as `GeneratedForm`/`WorkflowActionBar`/permission-aware UI state get built in the remaining Phase 6 sub-projects.

Until the trigger fires, `web/` stays exactly as it is: one Vite app, one `package.json`, `platform/` and `demo/` in the same repo.
