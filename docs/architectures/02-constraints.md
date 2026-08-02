# 2. Architecture Constraints

## Technical Constraints

- **Stack is fixed**: Fastify + Zod + Drizzle ORM + PostgreSQL + RabbitMQ (outbox pattern). See `docs/why.md` for the reasoning behind each choice — not repeated here.
- **Node >=24.15.0, pnpm** as the package manager (`packageManager: pnpm@9.15.0`), **ESM throughout** (`"type": "module"`) — no CommonJS interop assumptions.
- **TypeScript strict mode**, no implicit `any`, `noUncheckedIndexedAccess` and `exactOptionalPropertyTypes` on.
- **One generic `records` table**, not per-entity tables. Every business entity's data lives in `records.data jsonb`; there is no schema migration per new entity, only a new `*.entity.ts` metadata module. See [05. Building Block View](05-building-blocks.md#data-model).
- **PostgreSQL is the only datastore.** No Redis/cache layer, no separate search engine (full-text search is Postgres `tsvector`/GIN, not Elasticsearch).
- **RabbitMQ is the only message broker** for outbound events — no Kafka, no SNS/SQS.

## Organizational Constraints

- **Every non-trivial change goes through a spec → plan → implementation cycle** before code is written (`docs/superpowers/specs/*.md` / `docs/superpowers/plans/*.md`) — see [09. Architecture Decisions](09-adr.md), which indexes these specs as this project's decision log.
- **`docs/roadmap.md` is the single source of truth for what phase the project is in** — this document describes the architecture of what has actually shipped, cross-referencing roadmap phases where relevant, not a target that hasn't been built.
- **Trigger-based evolution**: speculative infrastructure (multi-service split, dedicated per-entity tables, a report/analytics query path) is not built ahead of a concrete trigger. See [04. Solution Strategy](04-strategy.md) and [11. Risks and Technical Debt](11-risks.md).

## Conventions (binding, from `CLAUDE.md`)

- Module/route code must not import the Drizzle client or RabbitMQ publisher directly — go through `CrudService`/`OutboxService` via the container (`src/core/container.ts`).
- Frontend/client query input must never map directly to SQL operators — it goes through `QueryPlanner`, constrained by entity metadata.
- Workflow side effects are emitted through the outbox, never published to RabbitMQ directly from a service.
- Every business route assumes tenant scope.
