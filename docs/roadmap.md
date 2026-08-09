# Roadmap

## Current Status (updated 2026-08-09)

| Phase | Status |
|---|---|
| 0. Skeleton | Done |
| 1. Production-shaped Platform Kernel | Done |
| 2. Metadata Compiler | Done |
| 3. Permission Engine | Done |
| 4. Query Planner V1 | Done |
| 5. Workflow Engine V1 | Done |
| 6. Frontend Core | Done (not browser-verified) |
| 7. Module Migration Strategy | Not started |
| 8. Hardening | In progress |
| 9. Multi-Service Evolution | Trigger-based (no trigger fired yet) |
| 10. Monorepo, npm publish | Partially done |
| 11. Low-code Platform Backbone Architecture | In progress |
| 12. Rust Core Migration | Decided; Migration Order (steps 1-9) done in `crates/`; not yet cut over to production |
| 13. Dynamic Cron Jobs | Not started |
| 14. Multi-language (i18n) | Not started |

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

**Status: Done** (see `docs/architectures/09-adr.md` for the decision record).

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

**Status: Done**, shipped as a 4-part initiative (see `docs/architectures/09-adr.md` for
the decision record),
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

**Status: Done**, shipped as 3 sub-projects (see `docs/architectures/09-adr.md` for the
decision record), in this order:

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

**Status: Done.** Atomic transition, optimistic locking, guard conditions (TypeScript predicates on `WorkflowTransition`), an append-only `workflow_events` audit log, and outbox side effects are implemented via `WorkflowEngine` + `CrudService.transition`, exposed at `POST /api/:entity/:id/transitions/:action` (see `docs/architectures/09-adr.md` for the decision record). "Notification integration" originally shipped as a publish-only stub outbox topic (`<entity>.workflow.transitioned`) with no consumer. 2026-08-09: `EventBus` gained a `subscribe` side (`crates/metap-infra/src/event_bus.rs` — durable queue bind on a topic-exchange routing key, ack/nack) and `crates/notification-worker` is the first real consumer, logging every transition. Deliberately minimal (stdout only, no email/SMS/webhook) since no real notification channel has been asked for yet; it can run as its own process (`pnpm worker:notification:rs`, the default, mirroring `outbox-publisher`) or inline inside `crm-server` via `NOTIFICATION_WORKER_INLINE=true` for single-process deployments — same `notification_worker::run` either way. Delivery semantics, same day: at-least-once (durable queue, manual ack), a per-queue DLQ (`<queue>.dlq`, wired via `x-dead-letter-exchange`/`x-dead-letter-routing-key` — a nacked poison message lands there instead of vanishing, verified live against a real broker) and `basic_qos` prefetch (20) for backpressure; `notification_worker::run` now propagates an error (instead of a clean exit) when the event stream closes unexpectedly (bus disconnect) so a process manager can tell that apart from a real shutdown signal, matching `outbox-publisher`'s "propagate and let the process manager restart" contract. Deliberately *not* built: retry-with-backoff — no call site nacks with `requeue: true` yet (nothing in `notify()` can fail), so a delay-queue/attempt-counter chain would be speculative infra ahead of a real trigger; `EventBus::subscribe`'s doc comment flags this as a known gap for whichever future consumer needs bounded retries.

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

**Status: Done.** React + TypeScript app shell, TanStack Query API client (`packages/platform-react/src/api`), the metadata client, `GeneratedList` (with cursor-based infinite-scroll pagination and `@tanstack/react-virtual` row windowing), and `FieldRenderer` (both halves — `FieldValue`/`fieldKindConfig` for read, `FieldInput` for write) are done. `GeneratedForm` is done. `WorkflowActionBar` is done. Permission-aware UI state is done — `CrudService.get()` now returns proactive `capabilities` (writable fields, record-level `canUpdate`, real per-transition guard results) that `GeneratedForm`/`WorkflowActionBar`/`FieldValue` consume to disable/mark what would fail before the user tries. List navigation and delete were added as a follow-up gap-fix after manual verification found `GeneratedList` had no way to actually reach `GeneratedForm`'s create route or `RecordDetail`, and delete didn't exist anywhere: `GeneratedList` now has a "New" button and a per-row View/Delete action column, `RecordDetail` has a Delete button, and the backend gained soft-delete support (`EntityAction` extended with `"delete"`, `PermissionService.canDeleteEntity`, `CrudService.delete()`, `DELETE /api/:entity/:id`, `WorkflowEngine.emitDeleted`). All of this passed typecheck/lint/the backend test suite and was committed; still not browser-verified in this sandbox (no working headless Chromium — missing system libraries, no `sudo`, no cached alternative). The frontend now lives in `packages/platform-react` + `apps/crm-fe` (renamed from `web/`) as part of the 2026-08-02 monorepo restructure — see [Architecture](docs/architectures/04-strategy.md)'s "Frontend Platform Package". `packages/platform-react`'s remaining `react-router-dom` coupling (`ApiErrorMessage`/`GeneratedList`/`RecordDetail` calling `Link`/`useNavigate` directly) is also fixed: a `NavigationAdapter` injected via React Context replaces all 3 direct imports, and `apps/crm-fe` provides the one real implementation (see `docs/architectures/09-adr.md` for the decision record). Passed typecheck/build/lint/the full backend test suite; not browser-verified for the same sandbox reason as above.

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

**Status: In progress** — started 2026-08-09. `docs/rust-core-viability.md`'s Migration
Order step 8 note deliberately deferred this whole phase's Rust-side gap (helmet-equivalent
headers, rate limiting, requestId/traceId) out of the initial HTTP port; that gap is what
got closed first, followed by the Docker/CI infra goals below.

Goals:

- ~~Secret manager integration~~ — Not started. No production deployment topology is
  documented yet (`docs/architectures/11-risks.md`) to say what secret manager it would
  integrate with; config today is `.env` files (dev-appropriate, not a production posture).
- ~~CORS allowlist by environment~~ — **Done**, predates this phase being tracked:
  `CORS_ORIGINS` (`crates/metap-infra/src/config.rs`) is a per-environment env var, comma-
  separated, defaulting to empty (permissive `CorsLayer::new()`) only when unset — see
  `metap_http::build_router`'s doc comment for the `allow_credentials` + explicit-origin-list
  constraint this enforces.
- ~~Helmet-equivalent security headers~~ — **Done (2026-08-09)**:
  `crates/metap-http/src/security_headers.rs`, applied globally in `build_router` (covers
  `apps/crm-server`'s static SPA fallback too, not just `/api`/`/metadata`) —
  Content-Security-Policy (helmet's `'self'`-based default, safe for a same-origin SPA),
  X-Frame-Options, X-Content-Type-Options, Referrer-Policy, Strict-Transport-Security,
  Cross-Origin-Opener/Resource-Policy, and the rest of helmet's default set.
- CSP — see "Helmet-equivalent security headers" above; folded in rather than tracked
  separately, since axum has no helmet-equivalent crate to configure a CSP directive on.
- HTML sanitizer / File scanning hook — Not applicable yet: this is a JSON-only API with no
  HTML rendering and no file-upload endpoint. Revisit if either is added.
- ~~Rate limiting~~ (not an original Phase 8 goal, added from the Rust-specific gap above) —
  **Done (2026-08-09)**: `tower_governor`, keyed on peer IP, ~300 req/min (a token-bucket
  approximation of the old `@fastify/rate-limit` fixed-window default — see
  `build_router`'s doc comment), 429 with the same `too_many_requests` error-body shape as
  every other error response. Needs the serving binary to use
  `into_make_service_with_connect_info::<SocketAddr>()` — `apps/crm-server/src/main.rs` and
  the `metap-http` e2e test both do.
- ~~requestId/traceId propagation~~ (the other Rust-specific gap) — **Done (2026-08-09)**:
  `crates/metap-http/src/request_context.rs`, `x-request-id`/`x-trace-id` response headers
  on every request, `x-trace-id` echoed when the caller sends a valid one, and both ids
  injected into every 4xx/5xx JSON error body centrally (not threaded through the ~30
  individual `service_error_response`/`internal_error_response` call sites).
- ~~non-root Docker image~~ — **Done (2026-08-09)**: `apps/crm-server/Dockerfile` — the
  first Dockerfile in the repo, colocated with the example app it packages rather than at
  the repo root (same reasoning as `apps/crm-server`'s own `keys/`/`.env`: it's this example
  app's own Dockerfile, not "the" repo Dockerfile — a downstream project builds its own
  equivalent binary and writes its own analogous Dockerfile for it, same as it writes its
  own `main.rs` rather than importing this one). Build context is still the repo root
  (`docker build -f apps/crm-server/Dockerfile .`) since both the Cargo and pnpm workspaces
  live there. Multi-stage (`node:24-slim` for `apps/crm-fe`'s static build, `rust:1-slim-
  bookworm` for `crm-server --release`, `debian:bookworm-slim` runtime), no secrets baked in
  (DB/RabbitMQ/JWT key path all read from the environment at container start, same as the
  local `.env` convention — the JWT key itself is mounted, not copied in), runs as a fixed
  non-root `metap` user (uid/gid 10001). Verified by actually building the image and running
  it against a live dev Postgres/RabbitMQ (`docker run --entrypoint id` confirmed
  `uid=10001(metap)`, `curl /health` returned 200 with every hardening header present).
- ~~CI checks~~ — **Done (2026-08-09)**: `.github/workflows/ci.yml`, three jobs — `rust`
  (build + unit tests + clippy, no DB needed), `rust-e2e` (Postgres/RabbitMQ service
  containers mirroring `docker-compose.yml`'s credentials, `db-migrate` against a fresh DB,
  then the full `--ignored` e2e suite), `frontend` (typecheck/lint/format:check/test).
  Verified by actually running the same sequence locally against throwaway Postgres/
  RabbitMQ containers (fresh-DB migration + full e2e suite passing) rather than trusting the
  YAML alone. Not yet enforced as a merge gate (no branch protection configured) and
  `clippy`/`fmt --check` aren't `-D warnings`-strict yet — the codebase isn't fully clean
  under either, see the workflow's own comments.
- ~~Structured logging / observability~~ (not an original Phase 8 goal — added 2026-08-09
  after an audit found the core crates had effectively zero logging: `metap-crud`,
  `metap-permission`, `metap-query`, `metap-workflow` had none at all, and the one place that
  did log — `metap-http`'s 500 handler — didn't even carry the `requestId`/`traceId` the
  response body already had, so a client-reported id couldn't be grepped against server
  logs) — **Done (2026-08-09)**: `tracing` + `tracing-subscriber` wired in via
  `metap_infra::init_tracing()` (one shared init, called first thing by every binary —
  `crm-server`, `outbox-publisher`, `notification-worker`, `db-migrate` — reading `RUST_LOG`,
  default `info`; `dev-tools` deliberately excluded, its stdout is CLI output — a minted
  token, a usage message — not a log stream). `crates/metap-http/src/request_id.rs` (new,
  outermost middleware) generates the request/trace id pair once into request extensions;
  `tower_http::trace::TraceLayer` (also new, wrapping every other layer) builds one span per
  request carrying both ids plus method/path/status/latency, so **any** `tracing` event
  logged anywhere downstream — a permission denial in `metap-permission`, a validation
  failure in `metap-crud`, a rejected filter in `metap-query` — is automatically correlated
  with the same ids the client sees, with no id threaded through any of those crates'
  function signatures. `request_context.rs` now reads the same ids from the extension
  instead of minting its own. Instrumented the actual decision points that were previously
  silent: permission allow/deny (`metap-permission`), rejected/ignored filter and sort fields
  and invalid cursors (`metap-query`), and in `metap-crud::CrudService` — entity/record not
  found, validation failure (with the offending field names), version conflicts, and the full
  transition-rejection chain (no workflow, no transition defined, guard failed) plus INFO-level
  success logs for create/update/transition/delete. Deliberately *not* done: no JSON/OTLP
  exporter (logs to stderr as plain text only — no aggregator exists to send to yet, same gap
  as the Docker/CI goals' "no production deployment topology documented"); revisit once one
  does. Verified live against a real Postgres/RabbitMQ/crm-server (not just `cargo build`):
  hit `/health`, an unauthenticated route, an unknown entity, and an empty-payload `create` —
  confirmed the access-log line and the `metap-crud`/`metap-permission` decision logs both
  carry the same `request_id`/`trace_id` and nest inside the same span.
- load tests for list/query/export — Not started.
- backup/restore drill — Not started.

## Phase 9: Multi-Service Evolution

Unlike Phases 1-8, this phase is trigger-based, not sequential — it starts when its trigger condition happens, not when Phase 8 finishes. See `docs/architectures/04-strategy.md`'s "Future Evolution: Multi-Service Split" section for the full reasoning.

**The repo/package structure itself is already done, ahead of its trigger.** The 2026-08-02 monorepo restructure pulled the pnpm-workspace split forward by explicit choice, not because the trigger condition fired: `packages/core` and `apps/crm` are already separate packages (`apps/crm` a thin Fastify app importing `packages/core` via `workspace:*`), matching the shape this trigger describes. What has *not* happened yet is the trigger's actual substance — there is still only one real module (`crm`); no second module has needed to be built as its own deployable unit. Treat the structural split as available infrastructure, not as evidence the underlying multi-service trigger has fired.

Triggers and the transition each one unlocks:

- **A second module (CRM, sales, inventory, accounting, ...) actually needs to be built as its own deployable unit** → done structurally (see above); the remaining work is building that second module itself — see Phase 7.
- **A single frontend screen needs to aggregate data from ≥2 services** → build a GraphQL gateway as a BFF in front of the REST services. Not yet triggered — still only one module, so no cross-service aggregation need exists.
- **The repo/package split above has actually happened** → evaluate gRPC for service-to-service calls where REST's overhead matters. The split is structurally done, but with only one running process there is no real service-to-service call to optimize yet — evaluate this once a second module is actually deployed independently (Phase 7), not from the structural split alone.

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

**Status: Partially done.** Split the repo into a pnpm workspace and publish `packages/core` (today's `src/core` + shared `src/infra`) as an installable npm package, so a downstream project can depend on Metap's core instead of forking it. Overlaps Phase 9's repo/package-split trigger, but is scoped separately here because "publish an npm package other people install" is a distinct, additional commitment (semver, changelog, public API surface) beyond just splitting the repo for internal multi-service use.

Goals:

- ~~Split into a pnpm workspace (`packages/core`, `apps/*`)~~ — **Done** 2026-08-02 (`packages/core`, `packages/platform-react`, `apps/crm`, `apps/crm-fe`). Pulled forward ahead of Phase 9's trigger, by explicit choice — see Phase 9 above. Superseded by the Rust migration (Phase 12) — `packages/core` no longer exists, its Rust equivalent is `crates/metap-*`.
- Define and stabilize `packages/core`'s public API surface. — Not started for actual crates.io/npm publishing (both `packages/platform-react` and every `metap-*` crate are still unpublished, no external non-workspace consumer exists yet). Partial progress on the *downstream-consumption ergonomics* this goal is really after, done 2026-08-09 ahead of publishing itself: `crates/metap` (a facade crate re-exporting the `metap-*` sub-crates — one dependency, one `use metap::prelude::*` instead of memorizing which sub-crate each item lives in) and `templates/metap-app` (a `cargo generate` template wired to depend on `metap` via git, since crates.io publishing hasn't happened) — both dogfooded by migrating `apps/crm-server` itself onto the facade and by actually generating + compiling + running a project from the template against a real Postgres. Publishing itself (a git dependency still means "clone and compile from source" for every consumer) is unstarted.
- Set up versioning/changelog and an npm publish pipeline (and, now, a crates.io one for `metap`/`metap-*`).

## Phase 11: Low-code Platform Backbone Architecture

**Status: In progress.** Define the architecture for using Metap as the backbone of a low-code platform (ERP, CRM, and beyond), not just a single-purpose ERP core. This is a design/architecture phase, not an implementation one — its output is a spec, to be broken into further implementation phases once written.

Goals:

- ~~Define what "low-code" means concretely for Metap~~ — **Done, at the directional level**, by `docs/vision.md` and `docs/low-code-platform-v1.md` (both 2026-08-02): who configures things (operators, via a metadata control plane, not source-code edits for the standard path), what's user-editable at runtime (metadata: entities/fields/list views/workflow/policies) vs. deploy-time (the execution engine itself — `packages/core`'s services stay code, only their metadata *inputs* become persisted).
- Reconcile this with the metadata-driven design already in place (Phases 0-6) and the multi-service split (Phases 9-10). — `docs/low-code-platform-v1.md`'s "Architectural Constraint" section already states the reconciliation principle (evolve the authoring model, keep the execution engine); making it concrete is this phase's remaining work.
- Produce a design spec before any implementation plan is written. — In progress. `docs/low-code-platform-v1.md` decomposes the work into 3 phases (A: Metadata Control Plane Foundation, B: Builder UI and Safe Runtime Rules, C: Platform Hardening); Phase A is further decomposed into 4 ordered sub-projects, the first of which has a written spec: `docs/low-code-metadata-storage-design.md` (persisted metadata storage + draft/published versioning — global, no workflow support yet, `crm.customers` stays code-authored for now). The remaining 3 Phase A sub-projects (runtime loader, publish validation pipeline, admin API) are named but not yet spec'd. That spec predates Phase 12 below (the Rust decision) and needs its implementation retargeted from TS to Rust when it's actually built — see its own status note.

## Phase 12: Rust Core Migration

**Status: Decided, Migration Order complete, not yet deployed.** `packages/core` moves to
Rust for every deployment profile — full decision record, spike results, and schema/codegen
strategy in `docs/rust-core-viability.md`. Not a sub-item of any earlier phase: it recasts
the *implementation language* of the execution engine every other phase above was built
against, without changing what any of those phases actually deliver (metadata compiler,
permission engine, query planner, workflow engine, CRUD, HTTP layer, peripherals — all
re-implemented 1:1, not redesigned).

Goals:

- ~~Decide whether to move `packages/core` to Rust~~ — **Done (2026-08-07)**, Option B (all
  profiles), after a spike measured real footprint/throughput gains — see
  `docs/rust-core-viability.md`.
- ~~Port the execution engine (Migration Order steps 1-9)~~ — **Done (2026-08-07)**:
  `crates/` is a 9-crate Cargo workspace (`metap-infra`, `metap-metadata`,
  `metap-permission`, `metap-query`, `metap-workflow`, `metap-crud`, `metap-http`,
  `metap-peripherals`, plus the `outbox-publisher` binary) — 51 unit tests (no DB
  dependency) + 19 e2e tests (real Postgres/RabbitMQ, one real HTTP server with a real
  RS256 JWT) all passing, `cargo build --release --workspace` clean. Two real bugs were
  caught only by e2e/live verification (a `data`/`status`-defaulting gap in `CrudService`,
  a CORS-config panic only reachable with a non-empty origin list) — both fixed, both now
  covered by tests.
- ~~Prove the port against the real business entity, not just fixtures~~ — **Done
  (2026-08-07)**: `apps/crm-server` (originally `crates/crm-server`, moved when `crates/`
  was scoped to library crates + ops binaries only — see the Repo Structure note below), a
  real `apps/crm`-equivalent binary running the actual `crm.customers` entity (ported from
  `customer.entity.ts`), verified live over HTTP — `pnpm dev:rs` to run it.
- ~~Delete `apps/crm`/`packages/core` once the port no longer needs them~~ — **Done
  (2026-08-07)**. Closed three gaps first so nothing was silently stranded: JWT keys moved
  to `crates/crm-server/keys/`, the three `packages/core/scripts/*.mjs` dev scripts became
  `crates/dev-tools`'s subcommands, and Drizzle's migration SQL was copied to
  `crates/migrations/` with `crates/db-migrate` (`sqlx::migrate!`) added to apply it — verified
  by running the full e2e suite against a database migrated from scratch by that tool alone,
  *before* deleting anything. See `docs/rust-core-viability.md`'s "TS Removal" section.
  `packages/platform-react`/`apps/crm-fe` untouched (frontend was always HTTP-only). Known
  gap this surfaced at the time: admin HTTP routes (policy CRUD, role grant/revoke) didn't
  exist over HTTP, only as functions with e2e coverage — closed 2026-08-08, see
  `crates/metap-http/src/routes/admin.rs` (`AdminContext` extractor requiring the `admin`
  role; `/admin/users`, `/admin/users/{userId}/roles[/{role}]`, `/admin/policies[/{id}]`,
  `/admin/policies/explain`), verified live against a real Postgres/RabbitMQ dev stack
  (role assign/revoke/list, policy create/list/delete/explain, 401 unauthenticated, 403
  non-admin).
- Cut the Rust stack over to actually serving traffic. — **Not started.** No production
  deployment topology exists for it yet (same gap Phase 8 Hardening already tracks for the
  TS stack); this is a distinct, later decision, not implied by the port being finished.
- Retarget Phase 11's in-flight TS-authored specs (starting with
  `docs/low-code-metadata-storage-design.md`) to Rust before implementing them. — Not
  started.

## Phase 13: Dynamic Cron Jobs

**Status: Not started.** Metadata-driven scheduled jobs — an operator defines a recurring job (schedule + target action) through metadata/admin API, the same way entities/workflow/policies are defined today, instead of a developer hand-wiring a new cron entry in code. "Dynamic" is the operative word: the set of jobs is data the platform reads at runtime, not a fixed list baked into a binary at compile time.

Goals:

- Design the job metadata shape (schedule expression, target — e.g. a workflow transition, a webhook, a query-driven bulk action — tenant scope, enabled/disabled) and where it's stored (likely the same Postgres instance, a new table, following the outbox/`records` precedent of "one shared DB" from `docs/architectures/04-strategy.md`).
- Design the scheduler/dispatch loop. Likely shape: a new ops binary/worker (`crates/cron-scheduler` or similar), following the `outbox-publisher`/`notification-worker` precedent — poll due jobs, execute, record run history — rather than an in-process `tokio::spawn` timer inside `crm-server` (same reasoning Phase 9 gives for keeping ops workers as separate deployable units: independent scaling/restart, no coupling to the HTTP process's lifecycle). Whether it reuses `EventBus`/outbox (enqueue a "job due" event, let a consumer act on it) or dispatches directly is an open design question.
- Concurrency/locking: only one instance of a given due job should actually run when multiple scheduler replicas exist — needs the same `SELECT ... FOR UPDATE SKIP LOCKED` pattern `outbox-publisher` already uses for its batch claim, or an equivalent.
- Admin API + `packages/platform-react` UI for CRUD on job definitions and viewing run history — mirrors the existing policy/role admin surface (`crates/metap-http/src/routes/admin.rs`).
- Failure handling: retry policy, max attempts, alerting on repeated failure — ties into whatever the eventual real notification channel becomes (`crates/notification-worker` is stdout-only today, see its doc comment).

No concrete trigger has fired for this yet (no operator has asked to schedule a recurring action) — flagged here as a known gap, not queued for immediate implementation.

## Phase 14: Multi-language (i18n)

**Status: Not started.** Two separable concerns, both currently absent:

- **Metadata-authored content (entity/field/list-view labels, workflow action names, validation messages)** is hard-coded single-locale strings in `EntityDefinition`/`EntityField`/etc. today (see `apps/crm-server/src/customer_entity.rs`). Making these translatable means either a `Record<locale, string>` shape on every label field (breaking change to `EntityField` and the OpenAPI-generated types in `crates/metap-metadata/src/openapi.rs`) or a separate translation-key indirection layer. Needs a design decision before implementation — this is a metadata-model change, not just a UI one, so it should be scoped alongside Phase 11's low-code metadata work rather than bolted on separately.
- **Frontend UI chrome** (`packages/platform-react`, `apps/crm-fe`) has no i18n library wired in yet (no `react-i18next`/`formatjs`/equivalent) — static strings throughout. Independent of the metadata-content question above; could start earlier since it doesn't require a backend metadata-model change.

Goals:

- Decide the metadata-label translation shape (blocks on Phase 11's metadata storage design, `docs/low-code-metadata-storage-design.md`, since translated labels are exactly the kind of runtime-editable content that design is meant to own).
- Wire an i18n library into `packages/platform-react` for static UI chrome strings, with locale switching in `apps/crm-fe`.
- Decide how record `data` (user-entered field values) relates to locale, if at all — likely out of scope (that's tenant business data, not platform-owned content), but worth stating explicitly so it isn't assumed later.

No concrete trigger has fired for this yet (no multi-locale deployment requirement exists today) — flagged here as a known gap, not queued for immediate implementation.


