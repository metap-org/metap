# Roadmap

## Phase 0: Skeleton

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
