# Architecture Review — 2026-08-07

Status: exploratory (review only — no code changed, no phase status changed)

**Superseded, 2026-08-07, same day:** this review's central recommendation (Part 1's Event
finding / Part 2 / Part 4 — extract an `EventBus` interface as a small TS refactor) was
overtaken by a much larger decision made later the same day: `packages/core` moves to Rust
entirely (`docs/rust-core-viability.md`). That Rust port's Migration Order built
`crates/metap-infra`'s `EventBus` trait as part of a full reimplementation, not as the
standalone TS extraction this review proposed — so treat this document's *observations*
about the TS codebase (still real, still accurate as a snapshot of what's actually
deployed) as historical record, and its *recommendations* as superseded rather than a
live TODO list. Part 3's deployment-profile question and Part 2's "don't build the other
six SPIs without a trigger" reasoning are the two things from this review still actually
open — see `docs/modular-spi-architecture.md` for where those landed.

## Purpose and Method

This is a "lead architect" review of Metap's current architecture, written against the
brief in the repo-root `READ.md`: understand what exists, identify strengths/bottlenecks/
missing abstractions, and recommend *incremental* improvements — never a rewrite — each
answering why it's needed, what problem it solves, whether it's backward compatible, and
how much work it is.

It is written after re-reading the full `docs/architectures/` set (arc42 sections 1-12),
`docs/roadmap.md`, `docs/why.md`, `docs/vision.md`, `docs/low-code-platform-v1.md`, and
cross-checking the docs' claims against the actual source (`container.ts`, the permission/
workflow/outbox services, the RabbitMQ publisher). Where a doc's claim and the code
disagreed, the code wins, and the disagreement is called out below.

This document does not replace `docs/roadmap.md` as the source of truth for phase status —
it is an input to it. Where a recommendation below implies new work, it should still be
designed and recorded (in the relevant `docs/*.md` file and `docs/architectures/09-adr.md`)
before anything is built.

## Executive Summary

Metap's core is well-built and unusually disciplined about trigger-based evolution — most
of what a generic "add abstraction layers" review would suggest has already been
deliberately *not* built, for good, documented reasons (no `BaseRepository`, no report
query path, no GraphQL gateway, no gRPC). That discipline should be preserved, not
overridden by this review.

The one place where that discipline has a real, near-term cost is **event publishing**:
every outbox-adjacent line of code depends on a concrete `RabbitPublisher` type, with zero
interface boundary — unlike permission storage, which already has exactly this kind of
seam (`PolicyStore`). This is the single highest-leverage recommendation in this review
(Part 2).

A second finding is a **documentation drift bug**: `CLAUDE.md`'s description of
`WorkflowEngine` says transition/guard logic "is not yet implemented," but
`docs/roadmap.md` (the authoritative phase tracker) marks Phase 5 (guard conditions,
atomic transitions, optimistic locking) as **Done**, and `POST
/api/:entity/:id/transitions/:action` exists and is tested. `CLAUDE.md` is the first file
read by any future session (human or agent) — a stale line there risks someone re-building
or avoiding already-shipped work. Fix is trivial (Part 4, item 1).

Everything else — Repository abstraction, WorkflowRuntime swapping, GraphQL, a Scheduler,
CacheProvider — is correctly *not* built yet, and this review recommends **not** building
it now either; each is covered below with the specific trigger that would justify it.

The one open question this review surfaces rather than answers is a **deployment-profile
decision**: `docs/architectures/02-constraints.md` binds Postgres and RabbitMQ as the only
datastore/broker. A "Tiny" (SQLite, single-binary) profile — the kind READ.md's prompt asks
about — would require amending that binding constraint. That's a product decision, not an
engineering gap, and this review deliberately stops short of making it (Part 3).

---

## Part 1: Component-by-Component Review

### Module

**Observation:** `apps/<module>` per pnpm workspace member; `apps/crm` is the only one
today; `packages/core` has zero business-entity knowledge (`buildApp(config, entities)`
takes entities as a parameter). Entity names are already dot-namespaced (`crm.customers`)
as the anchor for a future per-module service boundary.

**Problem:** None blocking. Phase 7 (Module Migration Strategy) hasn't started, so the
`apps/<module>` pattern is untested with a second real module — unknown unknowns like
shared cross-module frontend concerns (e.g. does `apps/demo`'s dev harness scale to
multiple modules' entities cleanly?) won't surface until then.

**Recommendation:** No change now. When the second module (Phase 7) is built, watch for
repeated boilerplate across `apps/<module>/{main.ts,.env.example,workers/}` — only
introduce a generator/template once a *third* module confirms the pattern repeats
(consistent with this project's trigger-based stance, not ahead of it).

**Impact:** None (deferred).
**Compatibility:** N/A.
**Future Evolution:** Directly prepares for Phase 9's multi-service split trigger.

---

### Entity

**Observation:** `EntityDefinition` = Zod schema + fields + list views + workflow,
code-authored in `*.entity.ts`, validated and hashed by `MetadataCompiler` at
`MetadataRegistry.register()` time (boot-time failure, not first-request failure).

**Problem:** 100% code-authored today — this *is* Phase 11's gap, not a bug. No other
issue found.

**Recommendation:** Proceed with Phase A sub-project 1 as already scoped in
`docs/low-code-metadata-storage-design.md` (spec already
written, no plan yet — see Part 4). Keep `crm.customers` code-authored; prove the
DB-authored path on a new entity first, exactly as that spec already locks in.

**Impact:** Medium — adds a new metadata source path; additive to `MetadataRegistry`, not
a replacement of the code-authored path.
**Compatibility:** Fully backward compatible by the spec's own design.
**Future Evolution:** This is the on-ramp to the entire low-code control plane (Phase 11).

---

### Repository

**Observation:** No `Repository`/`StorageProvider` interface anywhere. The Drizzle-backed
`Database` type is injected directly, as a concrete type, into ~9 services (`CrudService`,
`OutboxService`, `PostgresPolicyStore`, `QueryPlanner`/`condition-to-sql.ts`,
`IndexReconciler`, `MetadataDriftService`, `HealthService`, `RoleAssignmentService`,
`container.ts`). Phase 1 of the roadmap explicitly chose **not** to build a
`BaseRepository`/`TransactionManager`, using Drizzle's own `db.client.transaction()`
inline instead — a deliberate YAGNI call, not an oversight.

**Problem:** None *today*. This is only a real problem if either (a) a second datastore is
actually needed, or (b) a Tiny/SQLite deployment profile becomes a real target (Part 3).
Neither has fired.

**Recommendation:** Do **not** introduce a Repository/StorageProvider abstraction now —
there is no trigger, and it would directly contradict the reasoning that already killed
`BaseRepository` in Phase 1. If the Tiny-profile decision in Part 3 is ever made
affirmatively, note that the real seam isn't "CRUD verbs" — it's `QueryPlanner`'s generated
SQL, which is Postgres-dialect-specific in several places (`jsonb_extract_path_text`,
`plainto_tsquery('simple', ...)`, and the keyset-pagination `WHERE` construction). A future
`StorageProvider` abstraction should be scoped around that surface, not a generic
per-entity repository interface.

**Impact:** N/A unless triggered.
**Compatibility:** N/A.
**Future Evolution:** Tied to the Tiny-profile decision (Part 3), not independent of it.

---

### API

**Observation:** REST via Fastify, one generic `/api/:entity` route family, generated
OpenAPI at `/metadata/openapi.json`. No GraphQL.

**Problem:** None. Single frontend (`apps/demo`), no cross-service data aggregation need
exists yet.

**Recommendation:** Keep REST. GraphQL BFF stays exactly as trigger-based as
`docs/architectures/04-strategy.md` already states (≥2 modules whose data one frontend
screen needs to aggregate) — this review found no reason to move that trigger earlier.

**Impact / Compatibility / Future Evolution:** Reaffirms the existing decision; no change
recommended.

---

### Workflow

**Observation:** `WorkflowEngine` is a metadata-driven state machine — state field, initial
state, terminal states, transitions, TypeScript-predicate guards, atomic transitions with
optimistic locking, an append-only `workflow_events` audit log, and a post-commit outbox
event. `docs/roadmap.md` Phase 5 marks all of this **Done**, tested, and exposed at `POST
/api/:entity/:id/transitions/:action`.

**Problem (documentation drift — real finding):** `CLAUDE.md`'s "Core services and their
fixed boundaries" section still describes `WorkflowEngine` as "Currently only assigns
initial status and emits a `<entity>.record.created` outbox event on create; transition/
guard logic is not yet implemented." That's stale relative to Phase 5's actual, tested,
shipped state. Since `CLAUDE.md` is the first document loaded into every future session
(human or agent), this drift risks someone either re-implementing already-shipped guard
logic, or avoiding building on it out of a mistaken belief it doesn't exist.

**Problem (architectural, not urgent):** Guards are plain TypeScript predicate functions.
Phase 11's Phase B (Builder UI + Safe Runtime Rules) needs a declarative condition model
instead — "no arbitrary user code execution" is an explicit V1 constraint in
`docs/low-code-platform-v1.md`. Not urgent: Phase B hasn't started.

**Recommendation:**
1. Fix the `CLAUDE.md` line now (Part 4, item 1) — doc-only, no design needed.
2. When Phase B starts, reuse `PolicyCondition` (`src/core/permission/policy-condition.ts`)
   as the workflow guard's declarative shape instead of inventing a second condition
   language. It already solves exactly this problem — "declarative condition, no
   scripting" — for policies; a `guard: WorkflowGuardFn | PolicyCondition` union lets
   plain-function guards keep working during migration, so this is additive, not a
   breaking change, whenever it happens.

**Impact:** Item 1 is trivial. Item 2 (deferred) would touch `WorkflowTransition`'s type
and `WorkflowEngine.runGuard`, but only additively.
**Compatibility:** Fully backward compatible on both.
**Future Evolution:** Item 2 directly enables Phase B; reusing `PolicyCondition` also means
`PolicyExplainer`-style guard debugging becomes possible almost for free later.

---

### Permission

**Observation:** RBAC + ABAC, dynamic DB-backed role assignment, field/record-level
enforcement, `PolicyExplainer` for debugging. `PolicyStore` is a real interface
implemented by `PostgresPolicyStore` — the **one existing seam** in the entire codebase
that separates a service from its concrete storage.

**Problem:** None found. `PermissionService` itself is a concrete class, not behind an
interface — but there is exactly one implementation and no plausible second one, so that's
correctly not abstracted.

**Recommendation:** No change. This component is the model to imitate elsewhere (see Part
2), not something to modify itself.

**Impact / Compatibility / Future Evolution:** N/A — no change proposed.

---

### Event (Outbox + RabbitMQ)

**Observation:** `OutboxService` writes to `outbox_events` in the same transaction as the
business write; the publisher worker polls every 1s, claims rows with `FOR UPDATE SKIP
LOCKED` (already fixed to prevent double-publish), and publishes via a concrete
`RabbitPublisher` (`packages/core/src/infra/messaging/rabbitmq.ts`, hardcoded exchange name
`"metap.events"`, direct `amqp` import). `OutboxService`'s constructor takes
`RabbitPublisher` by concrete type. No `EventBus`/`MessagePublisher` interface exists
anywhere.

**Problem 1 (already tracked):** The outbox worker holds its DB transaction open for the
entire RabbitMQ publish call — `docs/architectures/11-risks.md` already flags this,
citing an external review's suggestion (claim-short-tx / publish-outside / lease-reclaim
on failure) as not yet done, for lack of measured contention. This review has nothing to
add beyond confirming it's real and correctly still untriggered.

**Problem 2 (new finding — the main recommendation of this review):** There is no
`EventBus` interface anywhere, unlike `PolicyStore`. This matters specifically because
Phase 9's multi-service trigger and any future broker change (e.g. Kafka once a second
module exists and throughput actually matters) currently has *nothing* to build on — every
call site would need to change, not just one injection point.

**Recommendation:** Extract an `EventBus` interface now, following the `PolicyStore`
precedent exactly: `RabbitPublisher`'s existing `publish(topic, payload)` shape is already
correct — just promote its type to an interface (`EventBus`) and change
`OutboxService`'s constructor parameter from `RabbitPublisher` to `EventBus`. This is a
pure refactor: same runtime behavior, one call site (`container.ts`) wired to the same
concrete `RabbitPublisher` implementation. It directly answers READ.md's ask — "the
framework should expose stable interfaces so infrastructure providers can be swapped with
minimal code changes" — with the cheapest possible version of that: do it while there is
still exactly one call site, before Phase 9 multiplies them.

**Impact:** Low blast radius: a new interface file, `container.ts`, and
`outbox-service.ts`'s constructor signature. No schema change, no migration, no API change,
no behavior change.
**Compatibility:** Fully backward compatible.
**Future Evolution:** Makes a future Kafka/NATS/Redis-Streams swap (Phase 9, once a second
module is actually deployed independently) a new `EventBus` implementation plus a
`container.ts` wiring change — not a `CrudService`/`WorkflowEngine`/`OutboxService`
rewrite.

---

### Metadata

**Observation:** `MetadataRegistry` + `MetadataCompiler` — boot-time validation, dangling
reference checks, deterministic hashing, drift detection, OpenAPI generation. This is the
most solid, most reused component in the codebase.

**Problem:** None found.

**Recommendation:** No change. This is exactly the foundation Phase A sub-project 2
(runtime loader for persisted metadata) needs to build on — reuse it as-is rather than
parallel-building a second compiler path for DB-authored metadata.

**Impact / Compatibility / Future Evolution:** N/A — no change proposed.

---

### Scheduler / GraphQL

**Observation:** Neither exists. `docs/architectures/04-strategy.md` already states
GraphQL's trigger (≥2 modules aggregated in one frontend screen); no doc or code anywhere
references a scheduler/timer-driven workflow action, though `WorkflowEngine` conceptually
could support a `Timer`-kind action later (per READ.md's own "Event / Command / Action /
Timer" framing).

**Recommendation:** Correctly deferred in both cases — no trigger for either exists today.
No action.

---

## Part 2: Runtime Abstraction — What to Actually Build

READ.md asks about `EventBus`, `WorkflowRuntime`, `PermissionProvider`, `StorageProvider`,
`CacheProvider`. Reviewed against actual triggers, not hypothetically:

| Interface | Recommend now? | Why |
|---|---|---|
| **EventBus** | **Yes** | Only interface with a near-term, cheap-now/expensive-later asymmetry. See Part 1's Event section. |
| StorageProvider | No | No second datastore need exists; would contradict Phase 1's already-made `BaseRepository` decision. Revisit only if the Tiny-profile decision (Part 3) goes affirmative. |
| WorkflowRuntime | No | No distributed-workflow requirement exists anywhere in the docs or roadmap. Recommend explicitly **against** evaluating Temporal/Camunda/BPMN engines for the foreseeable roadmap — they solve a distributed-orchestration problem this single-process system doesn't have, and would fight Phase B's stated direction (simple declarative guards, not a general workflow runtime swap). |
| PermissionProvider | No | `PolicyStore` is already the correctly-sized seam (storage, not the service). Wrapping `PermissionService` itself adds a layer with no second implementation to justify it. |
| CacheProvider | No | No measured latency problem exists. `PermissionSnapshot`'s per-call (not cross-request) design is a deliberate, already-documented choice — introducing Redis here solves a problem nobody has measured. |

---

## Part 3: Deployment Profiles — An Open Decision, Not a Recommendation

`docs/architectures/07-deployment.md` documents only the local dev topology (docker
compose, two bare processes, no orchestrator, no LB, no secrets manager — Phase 8
Hardening, not started, owns that gap already). `docs/architectures/02-constraints.md`
binds Postgres as "the only datastore" and RabbitMQ as "the only message broker" as
**technical constraints**, not defaults.

READ.md's deployment-profile framing (Tiny / Business / Enterprise / Cloud, "a small
customer should still run Single Binary + SQLite + Memory EventBus") directly implies
amending that binding constraint. This review deliberately does not make that call — it's
a product-scope decision (does Metap target self-hosted/on-prem low-code customers who
can't run Postgres+RabbitMQ?), not an engineering gap to silently fix.

**Option 1 — keep one deployment philosophy (recommended default):** Business/Enterprise/
Cloud profiles differ by scale and HA (replica counts, secrets backend, autoscaling — all
already-scoped under Phase 8 Hardening), never by swapping Postgres/RabbitMQ out. Zero new
work beyond finishing Phase 8 as already planned.

**Option 2 — add a real Tiny profile (SQLite + in-memory bus, single binary):** Requires,
in order: (a) formally amending `02-constraints.md`'s binding language, (b) the `EventBus`
interface from Part 2 (build anyway — it's recommended regardless) plus a cheap in-memory
`EventBus` implementation, (c) a `QueryPlanner` dialect audit — this is the actually
expensive part, since `jsonb_extract_path_text`, `plainto_tsquery('simple', ...)`, and the
keyset-pagination SQL are all Postgres-specific, not just the driver, (d) the
`StorageProvider` abstraction Part 2 just argued against building without a trigger.

**Recommendation:** Option 1 for now. A Tiny profile is legitimate future product
direction (useful once Phase 11's low-code platform has a concrete self-host customer
scenario), but per this project's own trigger-based philosophy, it shouldn't be decided
speculatively. If you do want to commit to it, sequence it as: EventBus interface (build
regardless) → in-memory EventBus impl (cheap) → QueryPlanner dialect audit (the real cost,
do this before touching storage) → StorageProvider → SQLite impl — each step shippable and
valuable on its own, none of it wasted if the SQLite step never happens.

---

## Part 4: Migration Strategy

**Current state:** Concrete Postgres + RabbitMQ everywhere except `PolicyStore`. Phase 11
(low-code control plane) has one spec'd, unimplemented sub-project.

**Intermediate state (recommended next, all low-risk, all independently shippable):**
1. Fix `CLAUDE.md`'s stale `WorkflowEngine` description (Part 1's Workflow finding).
2. Extract the `EventBus` interface (Part 1's Event finding / Part 2).
3. Write the implementation plan for Phase A sub-project 1 (already spec'd — needs
   `writing-plans`, not a new design).

**Target state (where Phase 9 and Phase 11 converge):** A second module is actually
deployed independently (Phase 7), firing Phase 9's real multi-service triggers for real;
Phase 11's low-code control plane (sub-projects A through C) is complete; the
deployment-profile decision (Part 3) has been made explicitly and recorded, not defaulted
into by accident.

---

## Part 5: Technology Recommendations

Held to READ.md's own bar — recommend only where a real, current problem exists, not
because something is popular:

- **Reuse `PolicyCondition` for workflow guards** (Part 1, Workflow) — not new technology,
  reuse of an already-built one. Highest value-per-effort item in this whole review besides
  the `EventBus` extraction.
- **No new message broker, no orchestrator, no GraphQL federation, no Temporal/Camunda, no
  OpenFGA/Casbin.** The in-house RBAC/ABAC + `PolicyExplainer` already covers every
  documented permission need; OpenFGA's relationship-graph model solves deep hierarchical/
  relationship-based authorization, a problem this system hasn't encountered.
- **One to watch, not act on:** if Part 3's Tiny profile is ever chosen, Drizzle already
  ships a SQLite driver — reuse the existing ORM rather than introducing a second one. Not
  a recommendation to act on now.

---

## Closing: Prioritized Action List

In order, each independently valuable and small:

1. Fix `CLAUDE.md`'s stale `WorkflowEngine` line to match `docs/roadmap.md` Phase 5's
   actual (done) state. Doc-only.
2. Extract an `EventBus` interface in front of `RabbitPublisher`, following the
   `PolicyStore` precedent. Pure refactor, one call site today.
3. Write the implementation plan for Phase A sub-project 1 (metadata storage &
   versioning) — the spec already exists.
4. Decide the deployment-profile direction (Part 3) explicitly, and record the decision
   (as an ADR-style entry indexed from `docs/architectures/09-adr.md`) once made.
5. Everything else surfaced in this review (Repository, WorkflowRuntime,
   PermissionProvider, CacheProvider, GraphQL, Scheduler, Tiny profile itself) is correctly
   not-yet-triggered — leave it alone until its stated trigger fires.
