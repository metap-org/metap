# Vision

Date: 2026-08-02

Status: directional — not an as-built description. For what's actually shipped, see [`docs/architectures/01-introduction.md`](architectures/01-introduction.md) (arc42 Section 1), whose short "Vision" paragraph is the terse, as-built companion to this fuller statement.

## Core Idea

Metap is not meant to stop at being a single business application, or even just a metadata-driven CRM core.

Its direction is:

> a reusable platform core for building business applications, with low-code as the higher destination.

In practical terms, that means two nested goals:

1. build a strong metadata-driven execution core
2. evolve that core into a real low-code platform over time

## What Exists Today

Metap already has the foundation of a platform, not just a single app:

- metadata-defined entities, compiled and validated as a runtime artifact (not passive config)
- generic CRUD, metadata-constrained query planning, and metadata-driven workflow
- policy-driven, server-enforced permissions
- reusable frontend rendering primitives (`packages/platform-react`)
- clean boundaries between the reusable core and each business module (`crates/metap-*` + `apps/<module>`, e.g. `apps/crm-server`), and between the reusable frontend and its demo consumer (`packages/platform-react` + `apps/crm-fe`) — a workspace shape chosen specifically to keep this direction cheap, not a generic engineering preference (the core moved from TypeScript to Rust 2026-08-07, see `docs/rust-core-viability.md`; the boundary shape itself is unchanged)
- a generated (not hand-maintained) contract between backend and frontend for entity metadata, so the two can't silently drift the way described below in "What This Means For Decisions Now"

This is already larger than a single CRM app, but it is still primarily a developer-authored platform core: metadata lives in code (entity-definition Rust modules, e.g. `apps/crm-server/src/customer_entity.rs`), not in a database a non-developer could edit.

## Higher Destination

The higher destination is not just "more modules" or "more CRUD."

It is:

> a low-code platform where operators or advanced admins can define, publish, and govern business applications from metadata, without depending on source-code edits for the standard path.

That future system must preserve the backend guarantees Metap already has today:

- server-side tenant isolation
- server-side permission enforcement
- optimistic locking
- workflow integrity
- reliable business-event delivery

See [`docs/low-code-platform-v1.md`](low-code-platform-v1.md) for a concrete, phased path toward a first real low-code platform version — what's missing, what order to build it in, and what to deliberately not build too early.

## Architectural Direction

Metap should get there by evolving the *authoring and control-plane model*, not by replacing the runtime engine.

The intended progression:

- **current state** — code-authored metadata on top of a reusable runtime core
- **next state** — persisted, versioned metadata with validation and publish control
- **higher state** — low-code application design and governance on top of the same execution core

## What This Means For Decisions Now

When making architecture choices in the current project, prefer decisions that keep this path open:

- clean package and service boundaries
- shared, generated (not hand-copied) public contracts between packages that can't otherwise see each other's source
- metadata validation and versioning
- runtime safety over ad hoc flexibility
- explicit governance for schema, workflow, and permission changes

Avoid decisions that would make a future low-code control plane harder to add:

- coupling business behavior directly to app-specific code paths
- bypassing the server-side metadata runtime
- introducing uncontrolled user scripting too early

## Relationship To Other Docs

- [`docs/architectures/01-introduction.md`](architectures/01-introduction.md) is the terse, as-built vision statement, inside the arc42 documentation set that describes the architecture as it exists today — not a target that hasn't shipped.
- [`docs/roadmap.md`](roadmap.md) tracks the official, phased implementation roadmap for the current project scope.
- [`docs/low-code-platform-v1.md`](low-code-platform-v1.md) describes a practical, phased path from today's architecture toward a first real low-code platform version.

This document is intentionally shorter than the other two. Its job is only to state the direction clearly:

> Metap's higher destination is low-code, built on top of the metadata-driven core that exists today.
