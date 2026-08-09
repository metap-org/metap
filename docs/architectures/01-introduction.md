# 1. Introduction and Goals

Metap keeps a fast metadata-driven development model: declare metadata once, then get CRUD, list, workflow, audit, export, and UI metadata consistently.

The difference is that helpers are a facade, not the architecture. The platform is split into explicit services, each with a fixed boundary — see [05. Building Block View](05-building-blocks.md).

## Vision

Metap is meant to be the backbone of a low-code platform usable to build ERP, CRM, and more — not a single-purpose ERP app. `crates/metap-*` (metadata, permission, query planner, workflow, outbox infra) is the reusable core platform — a Cargo workspace of library crates, entity-agnostic; each business subsystem is its own consumer binary (`apps/crm-server` today, sales/inventory/accounting later), depending on `crates/metap-*` and registering only its own entities (see [04. Solution Strategy](04-strategy.md) and [07. Deployment View](07-deployment.md)).

This is the terse, as-built version of that statement — for the fuller directional picture (why low-code is the higher destination, what that implies for decisions made now) see `docs/vision.md`; for a concrete phased path toward a first low-code platform version, see `docs/low-code-platform-v1.md`. Both are deliberately outside this arc42 set, since they describe a target, not what has shipped.

## Requirements Overview

- Declare an entity once (fields, list views, workflow) and get generic CRUD, list/filter/sort, permission enforcement, and workflow behavior for it — no per-entity route/handler/repository boilerplate.
- Every business record is tenant-scoped; no query, read, or write can cross a tenant boundary.
- Field- and record-level access control is metadata/policy-driven, not hardcoded per entity.
- Reliable event delivery for downstream consumers (workflow transitions, record changes) without losing events when the message broker is briefly unavailable.
- `docs/roadmap.md` tracks the phased build-out in detail; this document describes the architecture of what's actually built, not a target that hasn't shipped yet.

## Stakeholders

| Role | Concern |
|---|---|
| End User | Uses a business app (CRM today) built on Metap — records, lists, workflow actions |
| Admin | Manages role assignments and permission policies for their tenant |
| Entity Author (developer) | Adds a new business entity by writing one entity-definition Rust module (see `apps/crm-server/src/customer_entity.rs` for the pattern) — needs the metadata contract to be predictable and validated at boot |
| Operator | Runs the API server (`apps/crm-server`), the outbox publisher worker (`outbox-publisher`), PostgreSQL, and RabbitMQ — needs graceful degradation on partial outages |

## Quality Goals (top 3, detail in [10. Quality Requirements](10-quality.md))

1. **Correctness / data integrity** — optimistic locking on every write, transactional outbox so a business change and its event never diverge.
2. **Security** — tenant scope and permission enforcement happen server-side, always; nothing is trusted from the client beyond what metadata explicitly allows.
3. **Maintainability** — metadata is validated as a first-class runtime artifact (fails at boot, not the first request), and every core service has a boundary fixed from day one, even while its internals were still a scaffold.
