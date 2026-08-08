# Modular-First Capability SPI — Target Architecture

Date: 2026-08-07

Status: exploratory (directional target, not a build commitment — see
"Relationship to Current Architecture" below for what is and isn't decided)

## Purpose

This document is not the main product roadmap. `docs/roadmap.md` tracks the core
platform's current build-out and official phase status; `docs/architecture-review-2026-08-07.md`
is a component-by-component review of what exists today. This document captures a
separate question, raised directly against that review's Part 2 (Runtime Abstraction) and
Part 3 (Deployment Profiles):

> If Metap wants to serve everything from a self-hosted SME customer to a distributed
> enterprise deployment off the *same* source, what target shape does the infrastructure
> boundary need, and how far ahead of today's triggers should it be built?

It should be read together with `docs/vision.md` and `docs/low-code-platform-v1.md`, which
describe the low-code destination the same way: directional, not as-built.

## The Core Bet

Most low-code/ERP platforms fail with SME customers not because the product is wrong, but
because running it requires deploying Kafka, Redis, RabbitMQ, a workflow engine, IAM,
GraphQL gateway, Elasticsearch, Temporal, Prometheus — a dozen services just to run a
simple CRM. A small customer won't do that.

The proposed direction is **modular-first, not microservice-first**: the same source runs
as a single binary for a small customer, and as a distributed system for a large one,
switching only *configuration*, never business or workflow code. This isn't a new
philosophy for Metap — it's the same trigger-based, evolution-over-rewrite stance already
in `docs/architectures/04-strategy.md` and `docs/roadmap.md`'s Phase 9 — but it names a
more specific target shape for the infrastructure boundary that stance has to grow into.

## Three-Layer Model

```
Level 1 — Programming Model
  Entity, Workflow, Permission, Event, Repository, API
  (what an entity/module author sees; never references Level 3)

Level 2 — Capability SPI
  Storage, EventBus, Scheduler, Identity, Cache, Search, WorkflowRuntime
  (interfaces — `EventBus` is now a real Rust trait, `crates/metap-infra/src/event_bus.rs`,
  not hypothetical — see "The Rust question" below. The pattern itself is
  language-agnostic ports-and-adapters, not tied to any one language by design)

Level 3 — Providers
  Memory, SQLite, Postgres, MySQL, RabbitMQ, Kafka, NATS, Redis, OpenFGA, Casbin,
  Elasticsearch, S3, ...
```

An `Order.emit("Paid")` call at Level 1 never knows whether Level 2's `EventBus` is
currently backed by an in-memory bus, RabbitMQ, or Kafka. A `Customer` entity's repository
never knows whether it's reading from SQLite or Postgres. This is the generalized version
of a pattern Metap already has two working examples of: `PolicyStore` (originally
`packages/core/src/core/permission/policy-store.ts` in the TS codebase, now the
`PolicyStore` trait in `crates/metap-permission`) — `PermissionService` depends on the
interface, `PostgresPolicyStore` is its only implementation today — and, since the Rust
port, `EventBus` (`crates/metap-infra`, `RabbitEventBus` its only implementation), built as
a trait from the start rather than retrofitted. Neither seam has cost anything beyond the
one interface/trait definition. The proposal is to extend the same shape to the other
infrastructure dependencies that don't have it yet.

### The Rust question — decided, and no longer hypothetical

This document's Capability SPI pattern is language-agnostic by design; whether it's
implemented in TypeScript or Rust was originally framed as a separate question from the
pattern itself. That question was decided in `docs/rust-core-viability.md`: `packages/core`
is moving to Rust (Option B, all profiles), and the Migration Order documented there is now
**complete** — `crates/metap-infra`'s `EventBus` trait (`trait EventBus { async fn
publish(...); async fn close(...); }`) is real, built, tested (unit + e2e), and is what
`crates/metap-workflow`/`crates/metap-crud` actually depend on today, matching the original
proposal's own `trait EventBus { ... }` sketch exactly. It still does not change how many
of the *other six* SPIs are worth building (still none — `Storage` in particular was
deliberately not extracted into a formal trait; every Rust crate that touches the DB uses
`sqlx::PgPool` directly, per Part 2 of `docs/architecture-review-2026-08-07.md`'s original
reasoning, which the Rust port followed rather than overrode) or the deployment-profile
decision in Part 3 of that review, which remains open on its own terms.

## Deployment Profiles

| Profile | Storage | EventBus | Scheduler | Notes |
|---|---|---|---|---|
| **Tiny** | SQLite | Memory | Memory | `./metap` — single binary, no external services. Self-host/SME target. |
| **Business** | Postgres | Memory | Background worker (in-process) | Today's actual deployment shape, roughly — see caveat below. |
| **Enterprise** | Postgres | RabbitMQ | Distributed worker | Adds Redis (cache), S3 (file storage), Elasticsearch (search) as needed. |
| **Cloud** | Postgres (managed) | Kafka/RabbitMQ | Scheduler cluster | Full separation: gateway, workflow cluster, notification, search, storage all as independent scaled services. |

Same source. Only the Level 2→3 wiring changes, driven by config:

```yaml
eventBus:
  provider: memory   # or: rabbit, kafka
storage:
  provider: sqlite    # or: postgres, mysql
```

**Caveat, stated plainly:** Metap's actual "Business" profile today is Postgres + RabbitMQ
(not an in-process memory bus) — `docs/architectures/02-constraints.md` currently binds
both as the *only* datastore/broker, not as one profile among several. This table describes
the target, not the current state; reconciling it requires the decision in "Relationship to
Current Architecture" below.

## Module Deployment: `deployment: remote`

The mechanism that makes "modular monolith → distributed monolith → selective
microservices" (rather than a rewrite) concrete: a module boundary is a config switch, not
a code fork.

```yaml
module:
  order:
    deployment: remote   # was: local (the default — same process as everything else)
```

When a module is `local`, its `EventBus`/`Repository`/workflow calls resolve in-process.
When it's `remote`, the same calls resolve to a network client behind the same Level 2
interfaces — the module's own business logic, workflow definitions, and permission rules
are unchanged either way. This is the concrete "how" behind
`docs/architectures/04-strategy.md`'s Multi-Service Split section and
`docs/roadmap.md`'s Phase 9 trigger ("a second module actually needs to be built as its own
deployable unit") — that section currently says *that* it will happen; this adds *how* it
would happen without a rewrite.

The failure mode this avoids, named explicitly because it's the actual bug pattern in a lot
of engines: a workflow step written as `publishToRabbit(...)` directly. The moment you want
that step to run in-process instead (or vice versa), you rewrite it. A workflow step written
as `emit(event)` against the `EventBus` interface doesn't care.

## Per-Module Migration

Each module owns its own `metadata.yaml` (or code-authored entity, per today's model) +
`migration/` + `workflow/` + `permission/`:

```
crm/
  migration/001.sql
  migration/002.sql
inventory/
  migration/001.sql
```

The platform computes the merged, ordered migration plan across modules at deploy time
(`crm` needs 003-005, `inventory` needs 003` → runs in dependency order) rather than a
developer manually tracking cross-module migration ordering.

This maps onto `docs/architectures/05-building-blocks.md`'s already-documented Data Model
Strategy Step 3 ("dedicated tables for accounting/inventory critical paths") — today, one
shared `records` JSONB table serves every entity/module, so per-module migration folders
don't yet have separate schemas to migrate. This mechanism becomes concrete once Step 3
actually fires, not before.

## Relationship to Current Architecture

This is the section that matters most — what this document changes and doesn't.

**What this document is:** a named target shape, so that near-term decisions (starting
with the `EventBus` extraction — done, see "The Rust question" above) aim at a coherent
destination instead of
being decided one at a time with no shared picture.

**What this document is not:**
- **Not a commitment to build the other six Capability SPIs** (Storage, Scheduler,
  Identity, Cache, Search, WorkflowRuntime). `docs/architecture-review-2026-08-07.md`
  Part 2 evaluated each against a real trigger and found none yet except `EventBus`. That
  finding is unchanged by this document. Building all seven now would be exactly the kind
  of build-ahead-of-trigger this project has repeatedly and explicitly declined to do
  (Phase 1 rejected `BaseRepository` for this reason; Phase 4 rejected a report query
  boundary for this reason).
- **Not a change to `docs/architectures/02-constraints.md`.** That file currently binds
  Postgres and RabbitMQ as the *only* datastore/broker — the Tiny profile's SQLite/Memory
  row in the table above directly contradicts that binding language. Adopting Tiny as a
  real target requires an explicit, separate decision to amend that constraint (this is
  exactly Part 3's "Option 2" in the architecture review) — this document names the target
  shape that decision would produce, but does not make the decision itself.
- **Not evidence to start Phase 9 early.** Phase 9's triggers (a second module needing
  independent deployment, cross-module aggregation, real service-to-service calls) are
  unchanged. `deployment: remote` is documentation of *how* Phase 9 will work when
  triggered, not a reason to trigger it now.

**The honest tension:** this target shape asks for infrastructure abstraction *ahead* of
most of its triggers, betting that a self-host/SME customer segment will eventually need
it. The project's track record so far (every phase in `docs/roadmap.md`) has consistently
declined that bet in favor of building exactly what's triggered. Adopting this document as
a real target doesn't resolve that tension — it just means the tension is now named and
visible instead of implicit, and each future SPI still gets evaluated against a real
trigger before it's built, per the sequencing below.

## Sequencing

Not a new roadmap phase — maps onto Phase 9 (Multi-Service Evolution) and the open
deployment-profile decision named in `docs/architecture-review-2026-08-07.md` Part 3.
Each step below is independently valuable; none commits to the next.

1. **`EventBus` SPI — done.** Built as `crates/metap-infra`'s `EventBus` trait +
   `RabbitEventBus` impl, as part of the full Rust Migration Order
   (`docs/rust-core-viability.md`), not as a standalone TS extraction.
2. **Document `deployment: remote`** in `docs/architectures/04-strategy.md`'s Future
   Evolution section — no code, just names the mechanism Phase 9 will use.
3. **Decide the Tiny-profile / SQLite question explicitly** (architecture review Part 3) —
   only once decided does a `Storage` SPI + SQLite provider become worth building.
4. **Remaining SPIs** (Scheduler, Identity, Cache, Search, WorkflowRuntime) — each
   evaluated independently against its own real trigger when it appears, never as a bundle.
5. **Per-module migration merge** — waits on the Data Model Strategy's Step 3 trigger
   (a module actually needing dedicated tables), same as today.
