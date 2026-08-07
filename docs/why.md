# Why This Stack

Chosen stack (still what's actually deployed — the TS/Node stack this doc explains):

```txt
Fastify + Zod + Drizzle + PostgreSQL + RabbitMQ + Outbox Pattern
```

**2026-08-07:** `packages/core` is now decided to move to Rust (`docs/rust-core-viability.md`),
which reuses the same PostgreSQL/RabbitMQ/outbox-pattern choices below unchanged — only
`Fastify`/`Zod`/`Drizzle` (the framework/validation/ORM layer) are being replaced (with
`axum`/hand-rolled validation from field metadata/`sqlx`, respectively). This document's
reasoning for those three is historical context for why they were chosen originally, not a
still-open comparison.

## Why Fastify

Fastify is a good fit for a metadata-driven ERP core because it is:

- fast at runtime
- light at startup
- explicit
- plugin-friendly
- less ceremonial than NestJS
- easier to keep close to the platform architecture

NestJS is productive, but it adds decorators, reflection, module ceremony, and build/runtime overhead. Metap should keep framework overhead low and put architecture in our own core modules.

## Why Zod

Zod is familiar and readable for TypeScript teams.

Use it for:

- environment config validation
- route payload validation
- entity metadata input schemas
- generated API docs through JSON schema conversion

TypeBox is faster for JSON schema-first apps, but Zod is easier to onboard and flexible enough for this phase.

## Why Drizzle

Drizzle is selected over Prisma because this ERP core needs:

- fast build and runtime
- low magic
- SQL-friendly design
- strong TypeScript inference
- good PostgreSQL support
- easy JSONB usage
- direct control of query shape

Prisma is still a good choice for teams that want maximum onboarding comfort. The tradeoff is heavier generated client/runtime and less direct SQL control for complex ERP reports.

Drizzle fits the target better: productive, but close enough to SQL that performance tuning remains straightforward.

## Why PostgreSQL

PostgreSQL is the system of record.

Compared with MongoDB, it gives stronger support for:

- transactions
- constraints
- relational integrity
- reporting SQL
- row locks
- indexes
- materialized views
- JSONB for dynamic metadata fields

Metap still keeps a dynamic development style through `jsonb`, but uses PostgreSQL to make accounting, inventory, and permission-sensitive data safer.

## Why RabbitMQ

RabbitMQ is appropriate for ERP because modules need reliable integration events:

- workflow transitioned
- record created/updated
- notification requested
- export requested
- file uploaded
- webhook dispatch requested

RabbitMQ is better than an in-memory queue for multi-service ERP integration.

## Why Outbox Pattern

Directly publishing to RabbitMQ inside an API request can lose events:

1. DB commit succeeds.
2. RabbitMQ publish fails.
3. The business change exists, but other modules never hear about it.

Outbox fixes this:

1. Write business data and outbox event in the same DB transaction.
2. Background publisher drains outbox rows.
3. RabbitMQ receives events reliably.
4. Failed publishes can retry.

## Why Keep Metadata-driven Core

A metadata-driven core works well for ERP development speed. Metap keeps:

- generic CRUD
- generic list/form metadata
- workflow metadata
- reusable field definitions
- permission-aware generated behavior

The rewrite target is not less abstraction. The target is cleaner abstraction.
