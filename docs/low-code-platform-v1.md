# Low-code Platform V1 Direction

Date: 2026-08-02

Status: exploratory

## Purpose

This document is not the main product roadmap.

`docs/roadmap.md` tracks the core platform's current build-out and official phase status. This document captures a separate question:

> If Metap keeps evolving beyond a metadata-driven app core, what would a practical path toward a real low-code platform look like?

It should be read together with `docs/vision.md`, which states the broader direction more plainly:

> low-code is the higher destination, not just an optional side branch.

The goal here is to describe:

- what already exists that supports that direction
- what is still missing
- a realistic 3-phase path to a "low-code platform v1"

## Starting Point

Metap already has several important low-code-friendly foundations:

- metadata-authored entities
- generic CRUD over a shared runtime
- metadata-constrained query planning
- metadata-driven workflow
- policy-driven permission enforcement
- reusable frontend rendering primitives in `packages/platform-react`

That means the system is already more than a single CRM app. It is a metadata-driven platform core.

What it is **not** yet:

- a self-serve builder
- a runtime that accepts user-authored metadata safely
- a versioned metadata control plane
- a platform where non-developers can define and publish apps without writing TypeScript

Today, metadata is still **code-authored**. That is the main line between the current system and the higher low-code destination.

## Product Goal

The practical V1 goal should be:

> Let an internal operator or advanced admin define and publish a basic business app from metadata, without editing application code, while preserving the server-side safety properties Metap already cares about.

That is intentionally narrower than "full Airtable + Retool + Salesforce builder."

This document therefore describes a reachable first platform version on the way toward the broader vision, not the final ceiling of the product.

V1 should support:

- entity modeling
- field configuration
- list and form generation
- workflow configuration
- permission policy setup
- publish/version history

V1 should **not** try to solve all of this yet:

- visual workflow automation builder
- arbitrary scripting by end users
- marketplace/plugin ecosystem
- cross-app analytics suite
- drag-and-drop page builder for every UI pattern

## Architectural Constraint

Metap should not throw away the current architecture to chase low-code.

The correct direction is:

- keep `packages/core` as the execution engine
- add a metadata control plane around it
- move from code-authored metadata to safely persisted metadata
- compile persisted metadata into the same kind of runtime contract the current code path already uses

In other words: evolve the authoring model, not the whole runtime model.

## What Must Exist Before V1 Is Real

### 1. Metadata persistence and versioning

Current state:

- metadata lives in `*.entity.ts`
- registration happens at boot

Needed:

- metadata stored in the database or a dedicated configuration store
- draft and published versions
- revision history
- rollback support
- validation before publish

Without this, there is no real low-code platform, only a code-first framework with metadata.

### 2. A metadata control plane

Current state:

- developers author metadata directly in TypeScript

Needed:

- API for managing metadata definitions
- UI for creating and editing entities, fields, list views, workflow, and policies
- review/publish flow

This is the minimum "builder" layer.

### 3. Runtime compilation from persisted metadata

Current state:

- metadata is already compiled and validated at boot by `MetadataCompiler`

Needed:

- a compile step from stored metadata into runtime-safe internal definitions
- clear publish-time validation errors
- protection against malformed or dangerous metadata
- deterministic version hashes for published metadata snapshots

This is where the existing compiler architecture helps a lot.

### 4. Safe extension boundaries

Current state:

- workflow guards are plain TypeScript functions in code

Needed:

- a V1-safe rule model that does not require arbitrary user code execution
- declarative conditions for workflow guards and policies
- optionally, a tightly-controlled catalog of built-in actions

Low-code V1 should avoid arbitrary runtime scripting if the platform wants to keep predictable safety and operability.

### 5. Tenant-facing administration model

Current state:

- admin APIs exist for role assignment and policy management

Needed:

- who is allowed to design schema
- who is allowed to publish app changes
- how tenants are isolated from each other's metadata and runtime
- how published changes are audited

This is not just UI work. It is part of the platform trust model.

## Suggested 3-Phase Path

## Phase A: Metadata Control Plane Foundation

**Decomposed into 4 ordered sub-projects** (see `docs/roadmap.md`'s Phase 11 for status): (1) persisted metadata storage + draft/published versioning, (2) runtime loader that materializes published metadata through the existing `MetadataCompiler`/`MetadataRegistry` pipeline, (3) a publish validation pipeline layering deeper (cross-entity) checks on top of (1)'s shape validation, (4) a metadata admin API. Sub-project 1 has a written spec: `docs/low-code-metadata-storage-design.md`. Key scoping decisions locked in there: DB-authored metadata is global (not per-tenant) for Phase A, has no workflow support yet (needs Phase B's declarative-rule work first), and `crm.customers` is not migrated off `*.entity.ts` as part of Phase A — DB storage is proven on new entities first.

Objective:

Move metadata from source code into a versioned, persisted control plane without changing the runtime execution model more than necessary.

Deliverables:

- metadata storage schema
- draft/published metadata versions
- metadata admin API
- publish validation pipeline
- rollback to previous published snapshot
- runtime boot/load path that can read published metadata instead of only static code

Key principle:

Keep the existing `MetadataCompiler`, `CrudService`, `QueryPlanner`, `WorkflowEngine`, and `PermissionService` as the execution core. Replace the metadata source, not the engine.

Likely design shape:

- new metadata tables
- `published_metadata_versions`
- `draft_metadata_changes`
- loader service that materializes runtime `EntityDefinition`-like structures

Exit criteria:

- a developer can define an entity without editing `*.entity.ts`
- the server can validate and publish that metadata safely
- the generated CRUD/list/form experience still works from published metadata

## Phase B: Builder UI and Safe Runtime Rules

Objective:

Give operators a usable authoring surface and remove the remaining code-only configuration seams that block low-code adoption.

Deliverables:

- entity builder UI
- field builder UI
- list view builder UI
- workflow editor UI
- policy editor UI
- declarative workflow guard model
- publish preview / validation report

Key principle:

Do not introduce arbitrary scripting yet.

Instead:

- support condition builders
- support a catalog of built-in field types and rule operators
- support a small set of built-in actions and transitions

This keeps V1 safe, operable, and testable.

Exit criteria:

- an advanced admin can create and publish a simple CRM-style app from the UI
- no source code changes are required for the standard path
- permission and workflow behavior remain server-enforced

## Phase C: Platform Hardening for Real Low-code Use

Objective:

Make the low-code system governable enough to run real tenant-facing apps.

Deliverables:

- metadata audit log
- publish approval workflow if needed
- tenant-level schema isolation rules
- migration impact checks for destructive metadata changes
- stronger rollback and recovery tooling
- operational visibility for metadata publish events
- import/export of app definitions

Optional V1.5 candidates, only if demand is real:

- computed fields
- integration actions
- event-driven automations
- templated app starters

Exit criteria:

- metadata changes are auditable
- publish operations are reversible
- operators can understand and recover from bad metadata releases
- tenants can safely run different app definitions on the same platform core

## What Not To Do Too Early

To keep this effort realistic, avoid these traps:

### 1. Do not add arbitrary end-user scripting first

That would create:

- security risk
- debugging complexity
- operability problems
- unclear execution guarantees

Start with declarative rules and a bounded action catalog.

### 2. Do not build a generic drag-and-drop page builder first

Metap's real advantage today is:

- metadata-driven business entities
- server-side enforcement
- generated business UI

A page-builder-first direction would pull attention away from the platform's strongest core.

### 3. Do not bypass the existing server-side engine

The current runtime already has valuable guarantees:

- tenant scope
- permission enforcement
- optimistic locking
- outbox-backed business events

The low-code direction should reuse those guarantees, not recreate them in a parallel stack.

## Concrete Near-term Recommendations

If the project wants to preserve the option of becoming a low-code platform cleanly, the next architecture steps should be:

1. ~~Tighten permission defaults so the control plane will inherit a safer runtime.~~ **Done (2026-08-02)** — `PermissionService.scopedTenant` now fails loudly instead of silently defaulting a missing tenant; see `docs/architectures/08-cross-cutting.md#multi-tenancy`. The broader RBAC/ABAC "allow when no policy exists" model is a deliberate Phase 3 design choice (opt-in restriction, not default-deny), not something this fix touched — revisit only if the control plane specifically needs default-deny.
2. ~~Introduce a shared public contract package for metadata DTOs.~~ **Done (2026-08-02), different shape than proposed here** — instead of a hand-maintained `packages/contracts` package, the backend documents its entity-metadata wire contract as part of its OpenAPI doc (`GET /metadata/openapi.json`), and `packages/platform-react`'s types are generated from it via `openapi-typescript` (`pnpm --filter @metap/platform-react generate:types`). See `docs/architectures/09-adr.md`.
3. Design persisted metadata storage and publish semantics. **Spec written, not implemented** — `docs/low-code-metadata-storage-design.md` (Phase 11 sub-project 1). That spec predates the 2026-08-07 decision to move `packages/core` to Rust (`docs/rust-core-viability.md`); its data model/service contract still apply, but implementation should target Rust, not the TS file paths it names — see that spec's own status note.
4. ~~Refactor workflow guards toward declarative rule support, even if TypeScript guards remain temporarily.~~ **Done, in the Rust port only (2026-08-07)** — `crates/metap-metadata`'s `WorkflowTransition.guard` is a `metap_permission::PolicyCondition`, not a function, from the start (see `entity.rs`'s doc comment in that crate). The deployed TS system's guards (`WorkflowTransition.guard: (data, context) => true | string` in `entity.ts`) are unchanged and still function-based — this item is only resolved once/if the Rust core actually replaces the running TS system, not yet.
5. Separate runtime app startup from maintenance concerns such as index reconcile where useful for future control-plane operation. Tracked but not yet done — see `docs/architectures/11-risks.md`. Still true in the Rust port too: `apps/crm-server`'s boot sequence runs `metadata_drift::check`/`index_reconciler::reconcile` inline before serving, same shape as `app.ts`'s `buildApp`, not separated.

## Bottom Line

Metap already has the right foundation for a low-code platform:

- metadata as the source of behavior
- generic runtime services
- reusable generated UI
- explicit service boundaries

The real transition is not "build more CRUD."

It is:

> move from developer-authored metadata in code to operator-authored metadata with safe persistence, validation, publishing, and governance.

If that transition is done well, Metap can evolve into a credible low-code platform without discarding its current architecture.

Low-code is therefore best understood as the higher destination above today's metadata-driven core, and this document is one practical route toward that destination.
