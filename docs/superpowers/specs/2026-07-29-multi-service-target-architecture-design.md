# Target Architecture: Metap as Core Platform for a Multi-Service ERP

Date: 2026-07-29

Status: approved

Scope: this is a **documentation-only** design — it records a target architecture direction so future module/service additions follow it from day one, and does not restructure any code today. There is no implementation plan or task list following this spec; the deliverable is the doc changes themselves (`docs/architecture.md`, `docs/roadmap.md`).

## Motivation

Metap's stated vision (recorded earlier as project context) is to be the backbone of a low-code platform usable to build ERP, CRM, and more — not a single-purpose ERP app. As the project grows past one entity and one module, each ERP subsystem ("phân hệ" — CRM, sales, inventory, accounting, etc.) is expected to eventually become its own independently deployable service, all built on the same shared core (`MetadataRegistry`, `PermissionService`, `QueryPlanner`, `WorkflowEngine`, `OutboxService`) rather than duplicating that logic per module. This mirrors the overall shape of the legacy system this project replaces (many per-module API services sharing a common core library), while keeping the cleaner internal architecture metap already has.

This spec establishes the target shape and the concrete triggers for evolving toward it, without doing any of that evolution now — there is currently one module (`crm.customers`) and no second phân hệ to justify a real split yet.

## Design

### Vision

Metap's `src/core` (metadata, permission, query planner, workflow, outbox) is the reusable **core platform**. Each ERP phân hệ is eventually its own deployable service that depends on that core and registers its own entity modules — not a copy of the core, not a fork.

### Existing anchor: the entity naming convention already carries this

`EntityDefinition.name` is already dot-namespaced by domain (`crm.customers`). The prefix before the dot is already, in effect, the future phân hệ/service name. This requires no change — just discipline: every new entity module must be added under a domain-namespaced name from the start, so the eventual service boundary is already legible in the data before any physical split happens.

### Repo/package layout

**Now:** stays exactly as it is — one package, `src/core` + `src/modules` + `src/server`.

**Target, once a second phân hệ is actually being built:** a pnpm workspace with `packages/core` (today's `src/core` + shared `src/infra` pieces) and `apps/<phân-hệ>` (one thin Fastify app per service, each importing `packages/core` and registering only its own entity modules). This is not built now — the trigger is the first time a second, genuinely separate phân hệ needs to exist as its own deployable unit, not before.

### Data strategy

**Now, and for the foreseeable future:** one shared PostgreSQL instance, as today.

**Design constraint that makes a later split easy without being premature now:** no code may ever join across different entities' data directly in SQL. `QueryPlanner` already only ever scopes a query by a single `entity` + `tenantId` (confirmed — there is no cross-entity join anywhere in `CrudService`/`QueryPlanner` today), so this constraint costs nothing to state, only to keep enforcing as new modules are added. If a future phân hệ needs data owned by another phân hệ, it goes through that phân hệ's service-level API (today, that's just calling the shared `CrudService` in-process; conceptually it should still be treated as a remote call, not a raw join, so the eventual move to an actual remote call is a non-event). Splitting a phân hệ out to its own database later is then a matter of moving rows and a connection string, not rewriting query logic — because the query logic was never allowed to assume a shared schema across phân hệ boundaries in the first place.

### Protocol strategy

- **Now:** REST, as already built (JWT auth, structured errors, CRUD + optimistic locking + filtered/sorted list).
- **Future, triggered by having ≥2 services whose data a single frontend screen needs to aggregate:** a GraphQL gateway acting as a BFF (backend-for-frontend) in front of the REST services — the frontend talks to the gateway, the gateway talks to each service's REST API (or a more efficient internal protocol) and composes the response. Not built until that aggregation need is real.
- **Future, triggered by actual service boundaries existing (i.e., after the repo/package split above has actually happened):** gRPC as an option for service-to-service calls where REST's overhead matters. Not evaluated further until there's more than one process to call between.

## Out of scope (this pass)

- Any actual repo restructuring (pnpm workspace, `packages/core`/`apps/*` split).
- Building a GraphQL gateway/BFF.
- Building or evaluating gRPC.
- Splitting the database.
- Anything related to Phase 6 (Frontend Core) itself — this spec is about backend/service topology, not the frontend build, though the future GraphQL BFF is the layer a future frontend would eventually talk to instead of REST directly.

## Documentation changes (the actual deliverable of this spec)

- `docs/architecture.md`: add a "Target Architecture: Multi-Service Evolution" section covering the vision, the entity-naming anchor, and the three trigger-based transitions above (repo layout, data, protocol).
- `docs/roadmap.md`: add a new phase after Phase 8 (Hardening) — "Phase 9: Multi-Service Evolution" — stated as trigger-based rather than a fixed sequential goal list, since (unlike Phases 1-8) this phase's timing depends on an external event (a second phân hệ actually needing to be built), not on completing prior phases.
