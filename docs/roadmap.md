# Roadmap

## Current Status (updated 2026-08-01)

| Phase | Status |
|---|---|
| 0. Skeleton | Done |
| 1. Production-shaped Platform Kernel | Done |
| 2. Metadata Compiler | Done |
| 3. Permission Engine | Done |
| 4. Query Planner V1 | Done |
| 5. Workflow Engine V1 | Done |
| 6. Frontend Core | Partial |
| 7. Module Migration Strategy | Not started |
| 8. Hardening | Not started |
| 9. Multi-Service Evolution | Trigger-based (no trigger fired yet) |
| 10. Monorepo, npm publish | Not started |
| 11. Low-code Platform Backbone Architecture | Not started |

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

**Status: Done** (spec/plan under `docs/superpowers/{specs,plans}/2026-08-01-metadata-compiler*`).

- `MetadataCompiler.validate` — startup validation per entity: duplicate field names, dangling listView field/filter/defaultSort references, enum fields with no `enumValues`, malformed workflow shape, duplicate transitions. Runs inside `MetadataRegistry.register()`, so a bad entity module fails at boot, not at first request.
- `MetadataRegistry.validateReferences()` — cross-entity check that every `reference`-kind field's `refEntity` names a registered entity; runs once after all entities are registered (deferred out of `container.ts` — see the entity-registration note below).
- `MetadataCompiler.hash` — deterministic SHA-256 over a canonically-sorted serialization of an entity's shape (workflow transition `guard` functions excluded, since they're unrepresentable and already stripped on the wire). Exposed as `version` on `EntitySummary` (`GET /metadata/entities`) and on the frontend's `EntitySummary` type.
- `metadata_versions` table (migration `0005_condemned_cerise.sql`) + `MetadataDriftService` — compares each entity's current hash against the last-recorded one at boot and warns (never crashes) on drift, mirroring `HealthService`'s graceful-degradation stance. Wired into the container as `container.metadataDrift`, called from `buildApp`.
- OpenAPI generator (`openapi-generator.ts`) — exposed at `GET /metadata/openapi.json`, built only from the safe `EntitySummary` projection.

Also fixed as part of this work: `createContainer` (`src/core/container.ts`) previously imported `customerEntity` directly and registered it inline — a `core` file reaching into `modules`, which the layering (`modules -> metadata definitions`, not the reverse) doesn't allow. Entity registration is now an application-layer concern: `createContainer` returns an empty `MetadataRegistry`, and `registerEntities()` (`src/modules/registry.ts`) — the one place that knows the deployment's entity list — registers them and calls `validateReferences()` afterward. Callers (`buildApp`, the outbox worker, tests) call `registerEntities(container.metadata)` right after `createContainer(config)`.

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

**Status: Done**, shipped as a 4-part initiative (specs/plans under
`docs/superpowers/{specs,plans}/2026-07-31-dynamic-role-assignment*` and
`2026-08-01-{policy-storage-rbac-abac,field-record-enforcement,policy-explainer-snapshot-cache}*`),
going further than the roadmap's original "modest RBAC+ABAC scaffold" by
making role assignment itself dynamic:

1. **Dynamic role assignment** — roles live in the DB per `(tenantId, userId)`,
   granted/revoked at runtime via an admin API (`RoleAssignmentService`,
   `src/core/auth/role-assignment-service.ts`) instead of being baked into the
   JWT; the JWT is now a bare identity assertion. `scripts/seed-admin.mjs`
   bootstraps the first admin outside the (admin-gated) API.
2. **Policy storage + RBAC/ABAC evaluator** — the `policies` table (per
   tenant) combines a role allow-list with an optional attribute condition
   (`PolicyCondition`, `src/core/permission/policy-condition.ts`), OR-combined
   across multiple matching policies, no deny rules.
3. **Field-level + record-level enforcement** — `condition-to-sql.ts`
   translates record-scoped conditions into a Drizzle `WHERE` clause wired
   into `QueryPlanner.planList`; `PermissionService`/`PermissionSnapshot` mask
   field-level reads and gate field-level writes, wired into every
   `CrudService` call site (`list`/`create`/`update`/`transition`).
4. **`PolicyExplainer` + snapshot cache** — `explain()` produces a read-only
   trace of every policy considered and why, exposed via the admin-gated
   `POST /admin/policies/explain` simulator; `PermissionSnapshot` batches a
   tenant/entity's policies into one DB fetch reused across a single
   `CrudService` call (deliberately *not* a cross-request/TTL cache — see
   that sub-project's spec for the reasoning).

Known deviations/gaps, deliberately deferred rather than silently dropped:
- Record-level read enforcement only runs through `list()` — there's no
  single-record `GET /api/:entity/:id` endpoint yet for it to cover.

Bugfixed since (2026-08-01), both found during Phase 3's manual E2E
verification and confirmed with regression tests in
`src/core/crud/crud-service.test.ts`:
- `recordPolicyWhereClause` (`src/core/query/condition-to-sql.ts`) had no
  admin bypass, so a non-admin-scoped record-level read policy incorrectly
  emptied an admin's `list()` results. Fixed by bypassing policy evaluation
  entirely when `context.roles` includes `admin`, matching every other
  permission-decision entry point (`PermissionSnapshot.filterReadableFields`/
  `assertWritableFields`/`canUpdateRecordCondition`).
- `filterReadableFields` only masked the `data` JSONB blob, not the
  top-level `code`/`status` columns on `records` that mirror fields inside
  it (`src/infra/db/schema.ts`), so field-level masking of `code`/`status`
  was incomplete. Fixed with a new `CrudService.maskRecordForRead` helper
  that also nulls out `code`/`status` when the mirrored field
  (`code`, or `entity.workflow.stateField` for `status`) was masked out of
  `data`. (A third, minor issue was fixed earlier in the same diff:
  `POST /admin/policies` didn't validate that `field`+`action` combinations
  were coherent — now rejected with 400 via a schema refinement.)

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

**Status: Done**, shipped as 3 sub-projects (specs under
`docs/superpowers/specs/2026-08-0{1,2,2}-*-design.md`, plans under
`docs/superpowers/plans/` with matching names), in this order:

1. **Hot field index strategy** — `EntityField.indexed`/`unique` (previously
   declared but unread) now drive `IndexReconciler`
   (`src/core/metadata/index-reconciler.ts`): per-entity partial expression
   indexes on `records`, reconciled automatically at boot (`CREATE INDEX
   CONCURRENTLY IF NOT EXISTS`, best-effort, never blocks startup) and via a
   manual `pnpm index:reconcile` script. Caught and fixed a real bug during
   implementation: the indexed expression has to be
   `jsonb_extract_path_text(data, field)`, byte-for-byte matching
   `QueryPlanner`'s own filter/sort expression — an index built on the
   semantically-equivalent `data->>field` form is silently never selected by
   Postgres's planner.
2. **Full-text search strategy** — new opt-in `EntityField.searchMode: "fts"`
   (default `"substring"`, i.e. today's ILIKE behavior unchanged) matched via
   `to_tsvector('simple', ...) @@ plainto_tsquery('simple', ...)`, backed by a
   GIN index (`IndexReconciler`'s third index kind, same expression-matching
   discipline as above).
3. **Keyset pagination** — opaque base64 cursor (`src/core/query/cursor.ts`)
   validated against the *resolved* sort (post-fallback); `QueryPlanner`
   builds the keyset `WHERE` condition as an explicit two-clause OR (not a
   single row-value comparison) because the existing `orderBy` tiebreaker
   (`id ASC`) doesn't flip with the primary field's direction.
   `CrudService.list` executes with a `limit + 1` lookahead to produce
   `page.nextCursor: string | null`; a cursor for the wrong sort, or a
   malformed one, is a clean `400 invalid_cursor`, never a 500.

**Report query boundary — deferred, trigger-based** (not built), matching
Phase 9's style rather than this phase's other three items: there is no
concrete gap driving it yet — no reporting/analytics UI or consumer exists,
and the system has exactly one entity (`crm.customers`). Building a
`ReportService`/report-specific query path now would be infrastructure for a
workload that doesn't exist, contradicting this project's own trigger-based
evolution philosophy (see Phase 9, and `docs/architectures/05-building-blocks.md`'s Data Model
Strategy: "none of it should be built ahead of its trigger"). Trigger: a
concrete export/aggregation need shows up (a real UI or consumer asks for
it), or an OLTP-path query is measurably slowed by report-shaped access
patterns.

Original goals, for reference:

- Support metadata-defined filters. (Phase 1/pre-existing.)
- Support safe sort fields. (Phase 1/pre-existing.)
- Add keyset pagination. (Done, sub-project 3 above.)
- Add full-text search strategy. (Done, sub-project 2 above.)
- Add generated column/index strategy for hot JSONB fields. (Done, sub-project 1 above.)
- Add report query boundary. (Deferred, see above.)

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

**Status: Done.** React + TypeScript app shell, TanStack Query API client (`web/src/platform/api`), the metadata client, `GeneratedList` (with cursor-based infinite-scroll pagination and `@tanstack/react-virtual` row windowing — see `docs/superpowers/specs/2026-08-02-list-pagination-design.md`), and `FieldRenderer` (both halves — `FieldValue`/`fieldKindConfig` for read, `FieldInput` for write, see `docs/superpowers/specs/2026-08-02-field-renderer-design.md`) are done. `GeneratedForm` is done (`docs/superpowers/specs/2026-08-02-generated-form-design.md`). `WorkflowActionBar` is done (`docs/superpowers/specs/2026-08-02-workflow-action-bar-design.md`). Permission-aware UI state is done — `CrudService.get()` now returns proactive `capabilities` (writable fields, record-level `canUpdate`, real per-transition guard results) that `GeneratedForm`/`WorkflowActionBar`/`FieldValue` consume to disable/mark what would fail before the user tries (see `docs/superpowers/specs/2026-08-02-permission-aware-ui-design.md`). List navigation and delete were added as a follow-up gap-fix after manual verification found `GeneratedList` had no way to actually reach `GeneratedForm`'s create route or `RecordDetail`, and delete didn't exist anywhere: `GeneratedList` now has a "New" button and a per-row View/Delete action column, `RecordDetail` has a Delete button, and the backend gained soft-delete support (`EntityAction` extended with `"delete"`, `PermissionService.canDeleteEntity`, `CrudService.delete()`, `DELETE /api/:entity/:id`, `WorkflowEngine.emitDeleted`) — see `docs/superpowers/specs/2026-08-02-list-navigation-delete-design.md`. All of this is implemented and passing typecheck/lint/the backend test suite, but not yet browser-verified in this sandbox (no working headless Chromium — missing system libraries, no `sudo`, no cached alternative) and not yet committed, pending user review.

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

Unlike Phases 1-8, this phase is trigger-based, not sequential — it starts when its trigger condition happens, not when Phase 8 finishes. See `docs/architectures/04-strategy.md`'s "Future Evolution: Multi-Service Split" section for the full reasoning.

Triggers and the transition each one unlocks:

- **A second module (CRM, sales, inventory, accounting, ...) actually needs to be built as its own deployable unit** → split the repo into a pnpm workspace: `packages/core` (today's `src/core` + shared `src/infra`) and one `apps/<module>` per service, each a thin Fastify app importing `packages/core`.
- **A single frontend screen needs to aggregate data from ≥2 services** → build a GraphQL gateway as a BFF in front of the REST services.
- **The repo/package split above has actually happened** → evaluate gRPC for service-to-service calls where REST's overhead matters.

Until a trigger fires, its transition is not built. The one thing to do now, ahead of any trigger: keep every new entity module's name domain-namespaced (`<module>.<entity>`, e.g. `crm.customers`) and never let `QueryPlanner`/`CrudService` join across different entities' data in SQL — both are already true today and cost nothing to keep true.

## Success Criteria

Metap is successful if a developer can:

1. Define an ERP entity with fields and workflow.
2. Get CRUD/list/form metadata without writing boilerplate.
3. Add policies without touching HTTP routes.
4. Get reliable events without manual RabbitMQ publishing.
5. Tune a slow list view through query/index metadata.
6. Keep security enforcement on the server.

## Phase 10: Monorepo, npm publish

**Status: Not started.** Split the repo into a pnpm workspace and publish `packages/core` (today's `src/core` + shared `src/infra`) as an installable npm package, so a downstream project can depend on Metap's core instead of forking it. Overlaps Phase 9's repo/package-split trigger, but is scoped separately here because "publish an npm package other people install" is a distinct, additional commitment (semver, changelog, public API surface) beyond just splitting the repo for internal multi-service use.

Goals:

- Split into a pnpm workspace (`packages/core`, `apps/*`).
- Define and stabilize `packages/core`'s public API surface.
- Set up versioning/changelog and an npm publish pipeline.

## Phase 11: Low-code Platform Backbone Architecture

**Status: Not started.** Define the architecture for using Metap as the backbone of a low-code platform (ERP, CRM, and beyond — see `docs/superpowers/specs` project-vision context), not just a single-purpose ERP core. This is a design/architecture phase, not an implementation one — its output is a spec, to be broken into further implementation phases once written.

Goals:

- Define what "low-code" means concretely for Metap (who configures entities/workflows — code, admin UI, or both; what's user-editable at runtime vs. deploy-time).
- Reconcile this with the metadata-driven design already in place (Phases 0-6) and the multi-service split (Phases 9-10).
- Produce a design spec under `docs/superpowers/specs/` before any implementation plan is written.


