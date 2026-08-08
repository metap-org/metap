# Rust for `packages/core` — Decision Record

Date: 2026-08-07

Status: **Decided — Option B.** `packages/core` moves to Rust, for every deployment
profile (not just a future Tiny-profile binary). This document is the record of how that
was reached: the case made, the spike that grounded it in measurement, and the concrete
follow-on questions (schema/codegen strategy, contributor accessibility) it raised. **All 9
Migration Order steps below are done** as of 2026-08-07 — see each step's entry for what
was built and how it was verified, and the closing note under step 9 for exactly what's
still not done (no ported business entity, no real boot-sequence binary, Phase 8 Hardening
concerns) so that isn't mistaken for oversight either.

## Origin

Raised during the 2026-08-07 architecture review discussion, after
`docs/architecture-review-2026-08-07.md` Part 5 recommended against a full rewrite of
`packages/core` to Rust on a first pass (no measured performance trigger). The question was
reopened with a more specific case (below), grounded with a real benchmark spike
(`experiments/rust-outbox-poc/`), and decided.

## The Case Made for Rust

Two reasons were raised, evaluated separately since they need different scrutiny.

### 1. Minimum infra footprint + speed — confirmed by measurement

If the goal is a genuinely minimal, fast deployment (low RAM, no GC pause, small
distributable artifact), Rust wins over Node — not a close call, and no longer just an
argument: the spike below measured it directly. Node's own single-executable options
(`node --experimental-sea-config`, `bun build --compile`) still carry the V8 runtime and
GC; they get "one file to distribute," not "minimal footprint."

### 2. Contributor draw — real dynamic, real risk, named rather than dismissed

A trending language draws contributors, but two costs come with it: contributors drawn by
a trend don't reliably stay once it cools, and metap's real difficulty is domain-shaped
(entity/workflow/permission modeling), not language-shaped. More concretely, a Rust core
next to business-module authoring changes *who* can touch what — see "Contributor /
Outsource Accessibility" below for how this decision mitigates it rather than ignoring it.

## Spike: Rust Outbox-Publisher Benchmark

**What was built:** a Rust reimplementation of `apps/crm/src/workers/outbox-publisher.ts`
(poll `outbox_events` with `FOR UPDATE SKIP LOCKED`, publish to RabbitMQ, mark
`published_at`), benchmarked against a matching standalone Node implementation. Chosen
because it's fully separable from Zod/OpenAPI/frontend codegen — proof the spike couldn't
accidentally become a full core rewrite before there was evidence to justify one. Full
methodology in `experiments/rust-outbox-poc/README.md`.

### Results (2026-08-07, against the repo's real dev Postgres/RabbitMQ)

| Metric | Rust | Node | Verdict |
|---|---|---|---|
| Release binary size | 3.1 MB, self-contained | needs the 118 MB Node runtime + `node_modules` | Rust wins decisively |
| Cold start | 31–38 ms | 147–151 ms | Rust ~4–5x faster |
| Idle RSS | 13.0–13.5 MB | 64.9–65.1 MB | Rust ~5x lower |
| Drain throughput (5 runs each, 500 fixture rows) | 738–800 events/sec (mean ≈ 785) | 737–740 events/sec (mean ≈ 738) | **Rust ~6–8% higher**, consistent across runs |

All four measured metrics favor Rust. The throughput result needed five runs each to say
that with confidence — the first two-run comparison showed them statistically tied, and a
buggy early version (awaiting an unused RabbitMQ publisher-confirm future not present in
the Node fire-and-forget path) briefly showed Rust 7x *slower*, which was an implementation
bug in the spike, not a real result. Worth keeping on record as the concrete failure mode a
real port needs to avoid: naively "idiomatic" async code that doesn't match existing
publish semantics can silently regress throughput.

Why a same-order-of-magnitude but real (~6–8%) throughput edge, on a workload dominated by
Postgres/RabbitMQ round-trip time rather than compute: the network round trip is the same
for both languages, but each language's driver/runtime still adds its own fixed
per-operation overhead (serialization, promise/task scheduling) on top of that round trip.
Over 500 sequential operations, Rust's lower per-operation overhead adds up to a small,
consistent edge even though neither implementation is CPU-bound. This also means the
percentage gap would likely *grow*, not shrink, against faster infrastructure (lower
network RTT leaves proportionally more room for the fixed per-op overhead difference to
show), and would likely be larger still under real concurrency (multiple in-flight
publishes) rather than this benchmark's one-at-a-time sequential pattern.

## Decision

**Option B — Rust for all of `packages/core`, every deployment profile.** Not scoped to a
future Tiny-profile binary alone (Option A); this is a full replacement of the TS/Zod
execution engine.

This raised two concrete follow-on questions, resolved below: how schema/type generation
survives the language change, and how to avoid narrowing the contributor pool the decision
itself named as a risk.

## Schema & Codegen Strategy

The frontend type-generation chain turns out to already be more language-agnostic than the
original framing of this document assumed — worth correcting explicitly, since it changes
how much this decision actually costs.

**What actually happens today:** `packages/platform-react`'s `generate:types` script runs
`openapi-typescript http://localhost:3000/metadata/openapi.json` — it consumes a JSON
OpenAPI document over HTTP, not TypeScript/Zod source directly. And
`packages/core/src/core/metadata/openapi-generator.ts`'s `generateOpenApiDocument()` is
itself a plain function from `EntitySummary`/`EntityField[]` (a generic, serializable data
shape: field name, kind, required, enum values) to a JSON Schema fragment — it doesn't
touch Zod's types at all, just the same field-metadata shape `MetadataCompiler` already
treats as the wire contract.

**What this means for Rust:** the interchange contract was already OpenAPI JSON, not Zod.
A Rust `packages/core` needs to serve an equivalent `/metadata/openapi.json` — built the
same way, as a plain function from entity field metadata to JSON Schema (`serde_json::json!`
is sufficient; no derive-macro OpenAPI crate like `utoipa` is needed, since this project's
metadata is already dynamic data, not per-entity Rust structs). `packages/platform-react`'s
`generate:types` command **does not change** — it has no idea what language served the
document it's consuming.

**What's actually lost:** Zod's role as the *runtime validator* for create/update payloads
— that's a real, separate concern from OpenAPI generation, and needs a Rust equivalent.
Recommended shape: a **generic validator built directly from `EntityField[]`** (kind,
required, enum values) rather than a hand-authored per-entity schema (a `validator`/`garde`
derive struct, or Zod's `schema` field today). This is not just parity — it's an
improvement over the current design, which requires a developer to keep a hand-authored
Zod `schema` in sync with the `fields` array by hand (an existing, TS-specific duplication
risk, not something Rust introduces). It also directly matches the shape Phase 11's
low-code control plane needs anyway: once entities are DB-authored rather than
code-authored, there is no per-entity source file to hand-write a schema in, in either
language — only a generic metadata interpreter. Building that generic interpreter now, in
Rust, is straight-line work toward Phase 11, not a detour from it.

## Migration Order

Strangler approach: `apps/crm`'s Node API keeps serving real traffic throughout. Each step
ports one deployable unit, independently reversible (`v0.1.0` tags the pre-Rust TS/Node
baseline on `master`, pushed to `origin`, if any step needs to be abandoned) because every
step reads/writes the same Postgres schema and RabbitMQ contract regardless of which
language wrote it.

1. **Outbox-publisher worker — done (2026-08-07).** Real crate at
   `crates/outbox-publisher/` (Cargo workspace member, binary `outbox-publisher`; the workspace
   root `Cargo.toml` was later hoisted to the repo root — see "Repo Structure" note below),
   superseding the `experiments/rust-outbox-poc/` spike. Retry/backoff parity with
   `OutboxService.publishPending` (per-row `attempts`/`last_error` on failure, batch left
   for the next poll cycle) and with `runOutboxPublisherLoop`'s crash-on-unhandled-error
   contract (an unrecoverable batch failure propagates and exits non-zero — a process
   manager is expected to restart it, exactly like the Node worker today, not silently
   retried in place). Real config loading via `dotenvy::dotenv()` from the current working
   directory, matching `packages/core`'s `import "dotenv/config"` resolution exactly,
   including the `OUTBOX_DATABASE_URL` override falling back to `DATABASE_URL`. Wired as
   `pnpm worker:outbox:rs` (root `package.json`) alongside the untouched `pnpm worker:outbox`
   — both exist; which one a deployment actually runs is a config/ops choice, not a code
   change, per the strangler principle above. Verified against the repo's real dev
   Postgres/RabbitMQ: connects, polls, shuts down cleanly on SIGTERM. Zero HTTP risk —
   separate process; rollback is just running `pnpm worker:outbox` instead.
2. **Shared infra — done (2026-08-07).** `crates/metap-infra/`: `EventBus` trait +
   `RabbitEventBus` impl (the interface `docs/architecture-review-2026-08-07.md` Part 2
   recommended, following the `PolicyStore` precedent), a `connect_db` Postgres pool
   wrapper, and `load_config`/`AppConfig` mirroring `packages/core/src/server/config.ts`
   field-for-field (same env vars, same `OUTBOX_DATABASE_URL` fallback). `outbox-publisher`
   refactored onto it — rebuilt, retested against real dev Postgres/RabbitMQ, still clean.
3. **Metadata layer — done (2026-08-07).** `crates/metap-metadata/`: entity types
   (`entity.rs`, deliberately no `schema` field — see its doc comment), `MetadataCompiler`
   (`compiler.rs`: hash + validate, same issue messages, same stable-JSON-over-sorted-keys
   hashing approach), `MetadataRegistry` (`registry.rs`), and the OpenAPI generator
   (`openapi.rs`, hand-written `EntitySummary` JSON Schema mirroring
   `entity-wire-schema.ts` since there's no Zod-equivalent reflection step in Rust).
   14 unit tests, covering every case the original `metadata-compiler.test.ts`/
   `metadata-registry.test.ts` do (duplicate fields, unknown listView/defaultSort fields,
   implicit system fields, unknown `refEntity`, unknown `refDisplayField`, hash
   determinism/sensitivity). All passing.
4. **Permission service — done (2026-08-07).** `crates/metap-permission/`: `PolicyCondition`
   (`eq`/`neq`/`in`/`notIn`, `all`/`any`, `fromContext`/`literal`, deserializes the same
   wire JSON shape the TS policies table already stores), `PolicyStore` trait +
   `PostgresPolicyStore` (hand-written SQL, not an ORM — verified against the repo's real
   dev Postgres, not just compiled: create/list/delete round-trip and JSONB `condition`
   round-trip both pass as live integration tests), `PermissionSnapshot`
   (field/record-level read-mask, write-gate, admin bypass — same logic, same admin
   short-circuit at every entry point as the TS version), `PermissionService`
   (`scopedTenant`'s fail-loud-on-empty-tenant behavior preserved), and `PolicyExplainer`.
   10 unit tests on the pure condition/role-gate logic + 2 live-DB integration tests, all
   passing.
5. **QueryPlanner — done (2026-08-07).** `crates/metap-query/`: `cursor.rs`
   (encode/decode, same UUID-shape check), `condition_to_sql.rs` (`recordPolicyWhereClause`
   + `conditionToSql`, same admin-bypass fix as the TS ADR entry — with one deliberate
   deviation, flagged in its module doc comment: per-column-typed, fallible parameter
   binding instead of relying on node-postgres's implicit text-parameter coercion, since
   sqlx's `Encode`-based binding doesn't replicate that inference), and `query_planner.rs`
   (`planList`: tenant/entity/soft-delete scoping, substring/FTS/exact filters, sortable-field
   resolution with `defaultSort` fallback, limit clamping, keyset cursor validation +
   pagination). 11 unit tests plus **8 integration tests executing real generated SQL
   against the repo's dev Postgres** (`tests/query_planner_postgres.rs`) — tenant scoping,
   soft-delete exclusion, ILIKE substring matching, exact-match filtering, default-sort +
   limit-clamp, ascending sort on a declared-sortable field, fallback when the requested
   sort field isn't sortable, two-page keyset pagination with disjoint results, and
   cursor/sort-mismatch rejection. All passing. This satisfies the Migration Order's original
   commitment to verify this module against real query results, not just unit-test the
   pure logic in isolation.

   **Testing convention, fixed from this step onward:** unit tests (pure logic, no I/O) live
   in each crate's `src/*.rs` under `#[cfg(test)]` and run on a plain `cargo test` with no
   external dependency, ever. DB-touching tests are a separate concern — e2e, not unit —
   and live in each crate's `tests/*.rs`, `#[ignore]`d so `cargo test`/`cargo test
   --workspace` never opens a database connection by default; run them explicitly with
   `cargo test -- --ignored` once the dev DB is up. Verified both ways: a plain `cargo test
   --workspace` with `DATABASE_URL` unset passes 35/35 unit tests and reports the DB tests
   as `ignored` (never attempted, not skipped-at-runtime); `cargo test --workspace --
   --ignored` with the dev DB up passes all 10.
6. **WorkflowEngine — done (2026-08-07).** `crates/metap-workflow/`: no `WorkflowEngine`
   struct (the TS class holds no real state — its one dependency, `OutboxService`, is only
   ever used to reach `enqueue`, which itself ignores `this`), so this is a plain function
   module — `get_initial_status`, `find_transition`, `run_guard`, `record_event` (the
   append-only `workflow_events` audit write), and `emit_transitioned`/`emit_created`/
   `emit_deleted`/`emit_updated` (outbox enqueues). `WorkflowTransition::guard` is now a
   `metap_permission::PolicyCondition` (see `entity.rs`'s doc comment) — the declarative
   shape `docs/rust-core-viability.md`'s original Workflow finding recommended, live from
   this step rather than deferred, since Rust has no equivalent to a TS function-guard to
   port in the first place. Also added `metap-infra::outbox::enqueue` (the write half of
   `OutboxService` — the read/publish half was already `crates/outbox-publisher/`, step 1).
   6 unit tests (initial-status resolution, transition lookup, guard-less and guarded
   evaluation) + 2 e2e tests writing real rows to the dev Postgres (audit log row shape,
   outbox row topics and payload shape for two consecutive emits). All passing.
7. **CrudService — done (2026-08-07).** `crates/metap-crud/`: `list`/`get`/`create`/
   `update`/`transition`/`delete`, plus `validate_payload` (the generic, `EntityField`-driven
   validator replacing per-entity Zod — see `validation.rs`'s doc comment for its one known
   simplification: JSON-type checking, not per-field string formats like email/UUID shape,
   since `EntityField` metadata has no format concept to drive that from) and
   `mask_record_for_read`/`compute_capabilities` (the `code`/`status` mirrored-column
   masking and per-transition guard-availability logic). 7 unit tests on the validator +
   **3 e2e tests running the full lifecycle against the dev Postgres**: create → get
   (capabilities/guard-availability correct) → update (stale-version 409, then success,
   state field provably unchanged) → transition (guard pass, then invalid-from-state 409)
   → soft-delete → post-delete 404, plus asserting the exact `workflow_events` count and
   `outbox_events` topic sequence; a second test for tenant-scoped `list`; a third
   exercising a real non-admin field-write policy end-to-end through `PostgresPolicyStore`.

   **A real bug was caught by the e2e test, not the unit tests**, worth recording as the
   concrete case for why this crate's own testing convention insists on both: `create`'s
   initial-status value was landing in the top-level `status` column but not inside the
   `data` JSONB blob, because TS's per-entity Zod schemas commonly default the state field
   (`status: z.enum([...]).default("draft")`), silently pre-filling `data.status` before
   `getInitialStatus` ever runs — a behavior this crate's simpler, non-defaulting validator
   doesn't replicate. Fixed by having `create` write the resolved initial status into `data`
   itself when absent, which is more explicit than depending on a per-entity schema default
   line to exist and agree with `workflow.initialState`.
8. **HTTP layer — done (2026-08-07), scope narrowed as noted below.** `crates/metap-http/`:
   `axum` routes mirroring `records.ts`/`metadata.ts`/`health.ts` (list/get/create/update/
   delete/transition, `/metadata/openapi.json` + `/metadata/entities(/:entity)`, `/health`),
   a `AuthContext` extractor (RS256 JWT verification via `jsonwebtoken` + a live
   `user_roles` lookup per request — the read half of `RoleAssignmentService`, pulled
   forward from step 9 because no route can authenticate without it), and an error-response
   shape mirroring `error-handler.ts`'s `SERVICE_ERROR_MESSAGES` table. **Not** in this
   step's scope, deliberately: `helmet`/rate-limiting (Phase 8 Hardening, not this step's
   "thin wiring" goal), the admin routes (policy CRUD, `RoleAssignmentService`'s write
   side, `IndexReconciler`, `MetadataDriftService` — genuine step 9 Peripherals), and
   `requestId`/`traceId` in error bodies (a minor, deliberate simplification — see
   `error.rs`'s doc comment). 1 e2e test — a **real axum server bound to a real socket,
   a real RS256 JWT minted and verified, real Postgres** — exercising the entire stack in
   one HTTP-driven pass: public `/health` and `/metadata/openapi.json`, 401 without a
   token, create (201), get (200, with capabilities/guard-availability correctly computed
   through the whole stack), transition (200), stale-version update (409 with the exact
   error-body shape), delete (200), post-delete get (404). All passing. This is
   `packages/core`'s Rust equivalent — `apps/crm`'s Rust equivalent (a thin binary
   registering real business entities and calling `metap_http::build_router`) is not part
   of this Migration Order; no Rust-authored business entity exists yet.
9. **Peripherals — done (2026-08-07).** `crates/metap-peripherals/`: `index_reconciler.rs`
   (per-entity partial expression indexes — `idx_`/`uniq_`/`gin_` — via `CREATE INDEX
   CONCURRENTLY IF NOT EXISTS`, checked against `pg_indexes` first so a re-run only pays for
   a build when something actually changed; same graceful-degradation-on-DB-hiccup stance
   as `metadata_drift.rs`), `metadata_drift.rs` (first-boot/drift logging +
   `metadata_versions` upsert), and `role_assignment.rs` (`get_roles_for_user`/
   `assign_role`/`revoke_role`/`list_users` — the write side `metap-http`'s `AuthContext`
   extractor didn't need in step 8; that extractor now calls this crate's
   `get_roles_for_user` instead of the inline copy it started with, so there's one
   implementation, not two that could drift). `HealthService` and JWT verification were
   already done in steps 2/8 respectively, listed here in the original plan but not
   duplicated. 3 unit tests (index-name construction, SQL-literal/identifier escaping) + 3
   e2e tests against the dev Postgres: role assignment round-trip (including the
   ON-CONFLICT-DO-NOTHING double-assign case), drift detection across two `check()` calls
   with different hashes, and — matching the original TS test suite's own rigor, not just
   "the index exists" — an `EXPLAIN` assertion that Postgres's planner actually **selects**
   the created index for `QueryPlanner`'s exact `jsonb_extract_path_text` expression form.
   All passing.

   **All 9 Migration Order steps are now done.** `crates/` is a 9-crate Cargo workspace
   (`metap-infra`, `metap-metadata`, `metap-permission`, `metap-query`, `metap-workflow`,
   `metap-crud`, `metap-http`, `metap-peripherals`, plus the `outbox-publisher` binary),
   51 unit tests (zero DB dependency, verified by running with `DATABASE_URL` unset) and
   19 e2e tests (real Postgres, real RabbitMQ where relevant, one real HTTP server bound to
   a real socket with a real RS256 JWT) all passing, `cargo build --release --workspace`
   clean. Porting the real `crm.customers` entity and deleting `apps/crm`/`packages/core`
   entirely were both explicitly out of this Migration Order's original scope — both
   happened anyway, the same day, once the "Live Demo" section below proved the port
   complete; see that section and `docs/roadmap.md` Phase 12 for what changed. Phase 8
   Hardening's concerns (helmet-equivalent headers, rate limiting, `requestId`/`traceId`
   propagation) remained explicitly deferred, not silently dropped — closed 2026-08-09, see
   `docs/roadmap.md` Phase 8.

`packages/platform-react`/`apps/demo` needed no changes throughout — the `/metadata/openapi.json`
contract (see "Schema & Codegen Strategy" above) stayed stable across every step.

## Live Demo: `crates/crm-server`

Built immediately after the Migration Order to answer "does this actually work" directly,
not just via test suites — a real `apps/crm`-equivalent binary (`crates/crm-server/`) wiring
`metap-http`'s router to a real boot sequence (register `crm.customers`, `validate_references`,
`metadata_drift::check`, `index_reconciler::reconcile`, serve), matching `app.ts`'s
`buildApp`. It runs the **real** `crm.customers` entity (`src/customer_entity.rs`, a direct
port of `apps/crm/src/modules/crm/customer.entity.ts`), not a `test.*` fixture — the same
entity the TS app serves, against the same `records` table.

**Run it:** `pnpm dev:rs` from the repo root (builds + runs from `crates/crm-server/`, its own
self-contained `.env`/`keys/`). Mint a token with `pnpm mint-token`. Both `apps/crm` and
`packages/core` — including their `.env`/`keys/`/dev scripts — were deleted once this stack
was proven complete; see `docs/roadmap.md` Phase 12 and `crates/dev-tools`/`crates/db-migrate`
(replacing `packages/core/scripts/*.mjs` and Drizzle's `db:generate`/`db:migrate`) and
`crates/migrations/` (the same `.sql` Drizzle originally generated, copied over verbatim and
verified by re-running the full e2e suite against a freshly `db-migrate`'d database before
anything was deleted).

**Verified live** (2026-08-07), full CRUD over real HTTP against the running binary:
`POST /api/crm.customers` (create), `GET /api/crm.customers/:id` (capabilities/transitions
computed correctly), `GET /api/crm.customers` (list, real data), `POST
/api/crm.customers/:id/transitions/activate` (guard-checked transition, `draft` → `active`)
— all against the actual dev database, not a throwaway fixture.

**A second real bug, caught only by actually running the binary** (neither the unit nor the
e2e test suites exercise this): `build_router`'s CORS layer panicked at startup —
`allow_credentials(true)` combined with `allow_headers(Any)` is invalid per the CORS spec,
and `tower-http` enforces this as a hard panic, not a compile error. The `metap-http` e2e
test always passed an empty `cors_origins`, which takes a different, untested branch.
Fixed by using an explicit header allowlist (`Authorization`, `Content-Type`, `Accept`)
instead of a wildcard, and the e2e test now passes a real origin list so this branch stays
covered. Worth naming as the second data point (after step 7's `data`/status defaulting
bug) for why this port kept insisting on live verification over trusting compiled/unit-green
as sufficient — some failure modes only exist at runtime, under real configuration.

## TS Removal: `apps/crm` and `packages/core` Deleted (2026-08-07)

Once the live demo above proved the Rust stack complete against the real business entity,
`apps/crm` and `packages/core` were deleted outright — `master` and the `v0.1.0` tag both
still have the full TS history if this ever needs reverting. Before deleting, three gaps
the Rust stack hadn't needed until this point were closed, so deleting the TS side didn't
strand anything the Rust side was silently still depending on:

- **JWT keys** — `apps/crm/keys/`'s public key and `packages/core/keys/`'s private key
  (the Node app split them across two directories; the public halves were identical, so
  they were safe to consolidate) moved to `crates/crm-server/keys/`, gitignored like the
  originals.
- **Dev tooling** — `packages/core/scripts/{generate-dev-jwt-keypair,mint-dev-token,
  seed-admin}.mjs` (three tiny scripts) became `crates/dev-tools`'s `gen-keys`/`mint-token`/
  `seed-admin` subcommands — `seed-admin` calls the same `metap_peripherals::assign_role`
  its own e2e tests already verify, not a new hand-rolled query.
- **Schema migrations** — Drizzle's `db:generate`/`db:migrate` had no Rust equivalent.
  `packages/core/src/infra/db/migrations/*.sql` (the actual generated SQL, not
  `schema.ts`, which had no reason to be ported — nothing reads a schema *definition* file
  at runtime, only the SQL it already produced) copied verbatim into `crates/migrations/`,
  with `crates/db-migrate` (`sqlx::migrate!`) added to apply them. **Verified before
  deleting anything**: ran `db-migrate` against a brand-new scratch database, confirmed all
  6 tables appeared, then ran the *entire* e2e suite (all 19 tests) against that
  from-scratch database — passed, proving the Rust stack no longer needs `packages/core` to
  exist for a new environment to stand up the schema. New migrations from here on are
  hand-written `.sql` files in `crates/migrations/`, no diffing tool.

`package.json`'s scripts and `CLAUDE.md` were updated to match (new commands, new file
paths, the stack description itself). `packages/platform-react`/`apps/demo` were untouched
— confirmed via grep that neither referenced `packages/core`/`apps/crm` by path (the
frontend was always HTTP-only, never a direct import), then `pnpm install` regenerated the
lockfile to drop the two removed workspace members cleanly.

**What didn't exist yet at this point, named plainly:** admin HTTP routes (policy CRUD, role
grant/revoke over HTTP — `metap_peripherals::assign_role`/`revoke_role`/`list_users` existed
as functions and were covered by e2e tests, but nothing in `metap-http` exposed them as
endpoints yet) and Phase 8 Hardening's application-layer concerns (see above). Both were
already-known gaps before this deletion, not new ones created by it, and both have since
been closed — admin routes 2026-08-08 (`crates/metap-http/src/routes/admin.rs`), Hardening's
app-layer piece 2026-08-09 (see `docs/roadmap.md` Phase 8).

Per "Contributor / Outsource Accessibility" below: steps 1–3 are the right scope for a
small team to validate real Rust proficiency on before anyone touches `CrudService`/the
HTTP surface (steps 7–8), where a mistake has the most blast radius.

## Contributor / Outsource Accessibility

The real, named risk from "Contributor draw" above, addressed directly rather than argued
away: keep entity/workflow/permission **authoring** (what an outsourced contributor or
business-module author touches) declarative and data-shaped — a list of fields, a workflow
transition table — not idiomatic Rust code. Concentrate the Rust expertise the engine
itself needs (`CrudService`, `QueryPlanner`, `WorkflowEngine`, the eventual `EventBus`
SPI from `docs/architecture-review-2026-08-07.md` Part 2) in a smaller core team. This is
close to today's actual shape already — `*.entity.ts` files are simple declarative objects
even though the engine underneath is more complex TypeScript — the decision changes the
engine's language, not the shape of what a module author writes day to day.

## Repo Structure: Cargo.toml Hoist + `rust/` → `crates/` Rename (2026-08-08)

Two structural follow-ups, both scoped narrowly after evaluating (and declining) a broader
`justfile`/`Makefile` command-orchestration proposal as premature per this project's own
trigger-based/YAGNI stance — with only two ecosystems (Cargo, pnpm) and `package.json`
already working as the orchestrator, a third layer had no concrete trigger.

- **Cargo workspace root hoisted.** `[workspace]` moved from a nested `Cargo.toml` to a
  repo-root `Cargo.toml`, matching pnpm's existing root-level ergonomics — `cargo
  build`/`test`/`clippy` now work from the repo root with no `--manifest-path`. `Cargo.lock`
  moved with it. `resolver = "2"` kept (not `"3"`, which needs edition 2024; the workspace is
  still edition 2021).
- **`rust/` renamed to `crates/`.** Purely a directory rename, no code changes — every crate
  path (`crates/metap-*`, `crates/crm-server`, etc.) updated in root `Cargo.toml`'s `members`,
  root `package.json`'s scripts, and every doc/source-comment path reference across the repo.

Both verified after the change, not just compiled: `cargo build --release --workspace` (12
crates, clean), 51 unit tests (hermetic, `DATABASE_URL` unset), the full e2e suite against
the real dev Postgres, and a live `pnpm dev:rs` + `pnpm mint-token` round-trip (health check,
authenticated `GET /api/crm.customers` → 200) against the new paths.

**A third, unrelated bug was caught during this verification pass**, worth recording
alongside the two in the Migration Order above: `crates/metap-crud/tests/crud_service_postgres.rs`'s
`cleanup()` helper deleted `outbox_events` by `aggregate_type = 'test.orders'` — unscoped by
tenant — so under `cargo test --workspace -- --ignored`'s default parallelism, one test's
cleanup could delete another concurrently-running test's still-in-flight outbox rows. Passed
every time run in isolation (`--test-threads=1`), failed intermittently under full-workspace
parallel execution — the same "only real, concurrent execution finds this" pattern as the
CORS panic above. Fixed by scoping the delete to `aggregate_id IN (SELECT id FROM records
WHERE tenant_id = $1)`; reran the full e2e suite 3 times after the fix, all green (19 e2e
tests each run — see the note below on the Migration Order's test count).

**Test-count correction:** the Migration Order (step 9's closing note) and `docs/roadmap.md`
Phase 12 both state "20 e2e tests" in one place and "19" in another — an inconsistency
present before this rename. Recounting directly (`grep -rc '#\[ignore' crates/*/tests/*.rs`
over-counts by one per file, since each file's own doc comment mentions `` `#[ignore]`d ``
in prose): the correct figure is **19**, confirmed by three consecutive full green
`cargo test --workspace -- --ignored` runs after the fix above. The "20" figures in both
docs are stale and should read 19.

## Relationship to Other Docs

- `docs/modular-spi-architecture.md`'s Capability SPI pattern (Level 1/2/3) was framed as
  language-agnostic when written. With `packages/core` now committed to Rust, a future
  `EventBus`/`Storage` SPI (if and when its own trigger fires — see that document's Part 2/
  sequencing, unchanged by this decision) would naturally be a Rust trait rather than a
  TypeScript interface, matching the original proposal's own `trait EventBus { ... }`
  sketch. That document's SPI-count/trigger discipline is otherwise unaffected: this
  decision is about `packages/core`'s implementation language, not a reason to build the
  other six SPIs ahead of their triggers.
- Logged in `docs/architectures/09-adr.md`'s "Notable decisions not covered by a dedicated
  spec" section.
