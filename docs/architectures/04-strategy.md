# 4. Solution Strategy

## Fundamental Decisions

1. **Metadata-driven generic CRUD over one `records` table**, instead of a table + route + repository per entity. Declaring an `EntityDefinition` once (fields, list views, validation schema, workflow) is enough to get CRUD, filtering, sorting, permission enforcement, and workflow behavior. See `docs/why.md`'s "Why Keep Metadata-driven Core" and [05. Building Block View](05-building-blocks.md).
2. **Explicit, fixed service boundaries**, not a "helpers as facade" grab-bag. `MetadataRegistry`, `PermissionService`, `QueryPlanner`, `WorkflowEngine`, and `OutboxService` each own one concern and are wired together once, in `packages/core/src/core/container.ts` — routes and modules never reach around them into Drizzle or RabbitMQ directly (see [02. Constraints](02-constraints.md)). This boundary was fixed from day one, even while some services (e.g. `PermissionService`) were still allow-everything scaffolds — the shape didn't change when the real logic was filled in. It's now also a *package* boundary, not just a convention: `packages/core` physically cannot import a business entity, because business entities live in a different pnpm workspace package (`apps/<module>`) that depends on `packages/core`, never the other way around.
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

## Multi-Service Split (packaging done, deploy split not)

Metap is the backbone of a low-code platform, not a single-purpose ERP app. `packages/core` (metadata, permission, query planner, workflow, outbox) is the reusable core platform. Each business subsystem — CRM, sales, inventory, accounting, and so on — is expected to eventually become its own independently deployable service built on that same core, not a copy of it.

**Repo/package layout — done, ahead of the originally-planned trigger.** A pnpm workspace with `packages/core` (the entity-agnostic library: `core/`, `infra/`, `server/`, `workers/`) and `apps/crm` (one thin Fastify app, importing `packages/core` via `workspace:*` and registering only its own entities — `src/modules/crm/customer.entity.ts`, `src/main.ts`, `src/workers/*`). This was originally scoped to wait for "the first time a second, genuinely separate module needs to exist as its own deployable unit" (see `docs/superpowers/specs/2026-07-29-multi-service-target-architecture-design.md`) — the user explicitly chose to pull it forward on 2026-08-02, ahead of a real second module existing, to get the code into its target shape now rather than later. See `docs/superpowers/specs/2026-08-02-monorepo-{packages-core,apps-crm}-design.md` for the implementation, including the `buildApp(config)` → `buildApp(config, entities)` signature change that removing the CRM coupling required.

**Anchor already in place:** `EntityDefinition.name` is already dot-namespaced by domain (`crm.customers`). The prefix before the dot is, in effect, the service name. A second business module means a new `apps/<module>` alongside `apps/crm`, each with its own entities and its own thin entry points — not a folder inside `packages/core`.

**Still not done — real deploy separation.** `packages/core` and `apps/crm` are separate *packages* (independently typechecked/linted, only one direction of dependency), but there is still only one entity/module (`crm.customers`) and no deployment infrastructure in this repo (no Docker images, no process manager, no orchestrator config) for running `apps/<module>` as actually-separate deployed processes. That's a distinct, larger, still-untriggered decision — see [11. Risks and Technical Debt](11-risks.md).

**Data strategy.** Now, and for the foreseeable future: one shared PostgreSQL instance, owned by `packages/core` (`db:generate`/`db:migrate` run there regardless of which `apps/<module>` exist). Design constraint that keeps a later split cheap without being premature now: no code may join across different entities' data directly in SQL — `QueryPlanner` already only ever scopes a query by a single `entity` + `tenantId`, so this costs nothing to state, only to keep enforcing. If a future module needs another module's data, it goes through that module's service-level API — today that's an in-process call into the shared `CrudService`, but it should be treated as a remote call architecturally, so the eventual move to an actual remote call is a non-event. Splitting a module's data out to its own database later then becomes a matter of moving rows and a connection string, not rewriting query logic.

**Protocol strategy.** Now: REST, as already built (JWT auth, structured errors, CRUD with optimistic locking, metadata-constrained filter/sort). Future, triggered by having ≥2 modules whose data a single frontend screen needs to aggregate: a GraphQL gateway acting as a BFF (backend-for-frontend) in front of the REST services, composing their responses for the frontend. Future, triggered by `apps/<module>` actually being deployed as separate processes: gRPC as an option for service-to-service calls where REST's overhead matters. Neither is built or evaluated further until its trigger is real.

## Frontend Platform Package (done)

The same "reusable core, many independent consumers" shape from Multi-Service Split above applies to the frontend, not just the backend. `packages/platform-react` (`@metap/platform-react`) is a real pnpm workspace package — api-client, metadata-client, auth context, `GeneratedList`/`GeneratedForm`/`FieldValue`/`FieldInput`/`WorkflowActionBar`/`RecordDetail`, the reusable pieces a downstream project imports. `apps/demo` (`@metap/demo`, renamed from the old `web/`) is its first real consumer, importing it via `workspace:*` — that's the trigger this was originally waiting on ("an actual second app needing to import it"), pulled forward alongside the backend split on 2026-08-02. See `docs/superpowers/specs/2026-08-02-monorepo-platform-react-design.md`.

**Still not done — publishing to a registry.** `packages/platform-react` is workspace-internal (`private: true`, `main`/`types` point straight at `./src/index.ts`, no build step). Publishing it as a versioned, `npm install`-able package is a separate, later trigger — an actual *external* consumer (outside this workspace) needing it — not solved by this split.

**Design constraint that kept that migration cheap: `packages/platform-react` stays agnostic about how a consumer is built**, with one acknowledged exception found during the split. Different downstream projects will have different requirements — one might be a monorepo (Nx/Turborepo/pnpm workspaces), another a micro-frontend (Module Federation, single-spa), another a plain standalone SPA. A shared component/hook library can't dictate that choice without breaking whichever consumers don't fit it. Concretely:

- No global, app-wide client state store (Redux or otherwise) baked into `platform-react`. Server state stays in React Query (every consumer brings its own `QueryClient`, declared as a `peerDependency` so there's exactly one instance); anything else stays component-local state or a narrowly-scoped Context (like `AuthContext`), never a singleton store that every consumer is forced to share or route around.
- If a specific downstream app wants Redux, Zustand, or anything else for *its own* app-level state, that's that app's call, made in its own shell (`apps/demo`'s own equivalent) — not something `platform-react` imposes on it.
- **Known limitation, not fixed by this split:** `packages/platform-react` has a hard dependency on `react-router-dom` (`WorkflowActionBar`/`RecordDetail`/`GeneratedForm` use `Link`/`useNavigate` directly). A consumer using a different router — or none — can't use these three components as-is. Decoupling navigation (e.g. injected callbacks) is real future work, deliberately not undertaken as part of the packaging move — see [11. Risks and Technical Debt](11-risks.md).
