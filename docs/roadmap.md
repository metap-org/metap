# Roadmap

## Current Status (updated 2026-07-31)

| Phase | Status |
|---|---|
| 0. Skeleton | Done |
| 1. Production-shaped Platform Kernel | Done |
| 2. Metadata Compiler | Not started |
| 3. Permission Engine | Not started |
| 4. Query Planner V1 | Not started |
| 5. Workflow Engine V1 | Done |
| 6. Frontend Core | Partial |
| 7. Module Migration Strategy | Not started |
| 8. Hardening | Not started |
| 9. Multi-Service Evolution | Trigger-based (no trigger fired yet) |

## Phase 0: Skeleton

**Status: Done.**

Current scaffold:

- Fastify app shell
- Zod config validation
- Drizzle PostgreSQL setup
- RabbitMQ publisher
- outbox table/service
- metadata registry
- generic CRUD service
- query planner boundary
- permission service boundary
- workflow engine boundary
- sample `crm.customers` entity

## Phase 1: Production-shaped Platform Kernel

**Status: Done.** Auth middleware, `RequestContext` (`tenantId`/`userId`/`roles`/`functionId`), structured error responses with request/trace id, tenant scope enforcement, the outbox publisher worker, and CRUD/query service tests are all in place. `defaultContext()` has been fully replaced by real JWT-derived context — no code in `src/` still references it. One deliberate deviation: no separate `TransactionManager`/`BaseRepository` classes were built — DB transactions are handled inline via Drizzle's `db.client.transaction()`, which has been sufficient so far (YAGNI over premature abstraction).

Goals:

- Add auth middleware.
- Add request context with `tenantId`, `userId`, `roles`, `functionId`.
- Replace default context in `CrudService`.
- Enforce tenant scope everywhere.
- Add structured error response.
- Add request id and trace id.
- Add service tests for CRUD/query/metadata.
- Add outbox publisher worker.
- Add DB transaction helper.

Deliverables:

- `AuthService`
- `RequestContext`
- `TransactionManager`
- `OutboxPublisherWorker`
- `BaseRepository`
- first real migration

## Phase 2: Metadata Compiler

**Status: Not started.** `GET /metadata/entities` and `/metadata/entities/:entity` now return a hand-written safe projection (`MetadataRegistry.toMetadata`) instead of leaking the raw `EntityDefinition` (Zod `schema`, transition `guard` functions) — but that's a manual patch, not a real compiler. No `MetadataCompiler`, startup validation, OpenAPI generation, or metadata version/hash yet.

Goals:

- Validate entity definitions at startup.
- Compile field definitions into:
  - validation schema
  - list view contracts
  - OpenAPI schema
  - frontend metadata
  - index recommendations
- Add metadata version/hash.
- Add schema compatibility checks.

Deliverables:

- `MetadataCompiler`
- `MetadataValidationError`
- generated OpenAPI
- generated frontend metadata endpoint

## Phase 3: Permission Engine

**Status: Not started.** `PermissionService` still only does the Phase 0 scaffold's entity-level RBAC (per-action role allow-lists, `admin` bypasses everything). No field-level or record-level permission, ABAC, policy simulator, or permission snapshot cache.

Goals:

- Implement RBAC + ABAC.
- Support field-level permission.
- Support record-level permission.
- Support policy context.
- Add policy simulator.
- Cache user permission snapshot.

Deliverables:

- `PolicyDefinition`
- `AccessDecision`
- `PolicyExplainer`
- `PermissionSnapshotCache`
- policy tests

## Phase 4: Query Planner V1

**Status: Not started.** `QueryPlanner` still only enforces the Phase 0 baseline invariants (tenant scope, metadata-constrained filter/sort fields, a max limit). No keyset pagination, full-text search strategy, generated-column/index strategy, or report query boundary.

Goals:

- Support metadata-defined filters.
- Support safe sort fields.
- Add keyset pagination.
- Add full-text search strategy.
- Add generated column/index strategy for hot JSONB fields.
- Add report query boundary.

Deliverables:

- `ListViewDefinition`
- `FilterDefinition`
- `CursorPagination`
- `QueryExplain`
- index validation docs

## Phase 5: Workflow Engine V1

**Status: Done.** Atomic transition, optimistic locking, guard conditions (TypeScript predicates on `WorkflowTransition`), an append-only `workflow_events` audit log, and outbox side effects are implemented via `WorkflowEngine` + `CrudService.transition`, exposed at `POST /api/:entity/:id/transitions/:action`. See `docs/superpowers/specs/2026-07-31-workflow-engine-v1-design.md`. One scoped-down deliverable: "Notification integration" shipped as a stub outbox topic (`<entity>.workflow.transitioned`) only — no notification consumer exists yet, since there's no notification service to build one against.

Goals:

- Atomic transition.
- Optimistic locking.
- Guard conditions.
- Append-only workflow events.
- Side effects after commit through outbox.
- Notification integration.

Deliverables:

- `WorkflowTransitionService`
- `WorkflowGuard`
- `WorkflowEvent`
- workflow tests

## Phase 6: Frontend Core

**Status: Partial.** React + TypeScript app shell, TanStack Query API client (`web/src/platform/api`), the metadata client, and `GeneratedList` are done. `GeneratedForm`, `WorkflowActionBar`, a dedicated `FieldRenderer`, permission-aware UI state, and table virtualization are not yet built.

Goals:

- React + TypeScript app shell.
- TanStack Query API client.
- Generated list renderer.
- Generated form renderer.
- Workflow action UI.
- Permission-aware UI state.
- Table virtualization.

Deliverables:

- `metadata-client`
- `api-client`
- `GeneratedList`
- `GeneratedForm`
- `WorkflowActionBar`
- `FieldRenderer`

## Phase 7: Module Migration Strategy

**Status: Not started.**

Goals:

- Port one simple master-data module.
- Port one transaction module.
- Port one workflow-heavy module.
- Port one report/export flow.

Suggested order:

1. CRM customer/vendor master data.
2. Sales order or purchase order.
3. Inventory movement.
4. Accounting journal/report.

## Phase 8: Hardening

**Status: Not started.**

Goals:

- Secret manager integration.
- CORS allowlist by environment.
- CSP.
- HTML sanitizer.
- File scanning hook.
- non-root Docker image.
- CI checks.
- load tests for list/query/export.
- backup/restore drill.

## Phase 9: Multi-Service Evolution

Unlike Phases 1-8, this phase is trigger-based, not sequential — it starts when its trigger condition happens, not when Phase 8 finishes. See `docs/architecture.md`'s "Target Architecture: Multi-Service Evolution" section for the full reasoning.

Triggers and the transition each one unlocks:

- **A second phân hệ (CRM, sales, inventory, accounting, ...) actually needs to be built as its own deployable unit** → split the repo into a pnpm workspace: `packages/core` (today's `src/core` + shared `src/infra`) and one `apps/<phân-hệ>` per service, each a thin Fastify app importing `packages/core`.
- **A single frontend screen needs to aggregate data from ≥2 services** → build a GraphQL gateway as a BFF in front of the REST services.
- **The repo/package split above has actually happened** → evaluate gRPC for service-to-service calls where REST's overhead matters.

Until a trigger fires, its transition is not built. The one thing to do now, ahead of any trigger: keep every new entity module's name domain-namespaced (`<phân-hệ>.<entity>`, e.g. `crm.customers`) and never let `QueryPlanner`/`CrudService` join across different entities' data in SQL — both are already true today and cost nothing to keep true.

## Success Criteria

Metap is successful if a developer can:

1. Define an ERP entity with fields and workflow.
2. Get CRUD/list/form metadata without writing boilerplate.
3. Add policies without touching HTTP routes.
4. Get reliable events without manual RabbitMQ publishing.
5. Tune a slow list view through query/index metadata.
6. Keep security enforcement on the server.
