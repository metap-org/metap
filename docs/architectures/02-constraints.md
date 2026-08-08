# 2. Architecture Constraints

## Technical Constraints

- **Stack is fixed**: Rust (axum + sqlx) + PostgreSQL + RabbitMQ (outbox pattern). See `docs/why.md` for the reasoning behind the choices that predate the Rust migration (PostgreSQL, RabbitMQ, the outbox pattern) and `docs/rust-core-viability.md` for the decision to move the execution engine itself from TypeScript/Fastify/Zod/Drizzle to Rust/axum/sqlx — not repeated here.
- **A recent stable Rust toolchain**, no pinned MSRV yet; workspace edition 2021 (`Cargo.toml`'s `resolver = "2"`). No CommonJS/ESM concerns on the backend — that constraint applies only to the frontend (`apps/crm-fe`/`packages/platform-react`, Node >=24.15.0, ESM throughout).
- **One generic `records` table**, not per-entity tables. Every business entity's data lives in `records.data jsonb`; there is no schema migration per new entity, only a new entity-definition Rust module (see `apps/crm-server/src/customer_entity.rs`). See [05. Building Block View](05-building-blocks.md#data-model).
- **PostgreSQL is the only datastore.** No Redis/cache layer, no separate search engine (full-text search is Postgres `tsvector`/GIN, not Elasticsearch).
- **RabbitMQ is the only message broker** for outbound events — no Kafka, no SNS/SQS. (`metap-infra::EventBus` is a trait with one implementation, `RabbitEventBus`; see [05. Building Block View](05-building-blocks.md#event-bus) — this is an existing seam, not a plan to add a second broker.)

## Organizational Constraints

- **Non-trivial architectural decisions are recorded**, not just coded silently — see [09. Architecture Decisions](09-adr.md), this project's decision log. (Until 2026-08-07 this ran through a formal spec → plan → implementation cycle under `docs/superpowers/{specs,plans}/`; that directory was removed to cut ceremony/context overhead, and decisions are now recorded directly in `09-adr.md` or the relevant `docs/*.md` file instead.)
- **`docs/roadmap.md` is the single source of truth for what phase the project is in** — this document describes the architecture of what has actually shipped, cross-referencing roadmap phases where relevant, not a target that hasn't been built.
- **Trigger-based evolution**: speculative infrastructure (dedicated per-entity tables, a report/analytics query path) is not built ahead of a concrete trigger. The one deliberate exception: the workspace/module-packaging split (`crates/metap-*` + `apps/<consumer>`) was pulled forward ahead of its originally-documented trigger (a real second module) — see [04. Solution Strategy](04-strategy.md) and [11. Risks and Technical Debt](11-risks.md).

## Conventions (binding, from `CLAUDE.md`)

- Route/handler code must not import `sqlx`/`lapin` directly — go through `CrudService` (`metap-crud`) / `metap-infra`'s `EventBus`.
- Frontend/client query input must never map directly to SQL operators — it goes through `QueryPlanner` (`metap-query`), constrained by entity metadata.
- Workflow side effects are emitted through the outbox, never published to RabbitMQ directly from a service.
- Every business route assumes tenant scope.
- No `metap-*` library crate gets business-entity knowledge — that's `apps/crm-server`'s job (or a future second binary's).
