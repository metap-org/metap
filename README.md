# Metap

Metap is a metadata-driven platform core.

Chosen stack:

- axum for the HTTP runtime.
- sqlx for PostgreSQL access.
- PostgreSQL as the system of record.
- RabbitMQ for integration events.
- Outbox Pattern for reliable event publishing.

The backend moved from TypeScript to Rust on 2026-08-07 — see [`docs/rust-core-viability.md`](docs/rust-core-viability.md) for why, and [`docs/roadmap.md`](docs/roadmap.md)'s Phase 12 for status.

This repo is a Cargo workspace (`crates/` — the `metap-*` library crates plus the `outbox-publisher`/`db-migrate`/`dev-tools` ops binaries) and a pnpm workspace (`packages/platform-react`, the reusable frontend library), with sample/example consumers of both living under `apps/` (`apps/crm-server`, `apps/crm-fe`) — not the product itself, just proof the library surface works. Every command below runs from the repo root.

Start locally:

```bash
pnpm install
cp apps/crm-server/.env.example apps/crm-server/.env
docker compose up -d postgres rabbitmq
pnpm db:migrate
pnpm auth:dev-keys
pnpm dev:rs
```

Quality commands:

```bash
pnpm lint
pnpm format:check
pnpm format
pnpm typecheck
pnpm test:rs
pnpm test:rs:e2e   # needs DATABASE_URL + the dev Postgres/RabbitMQ up
```

Docs:

- [Architecture](docs/architectures/index.md)
- [Why This Stack](docs/why.md)
- [Roadmap](docs/roadmap.md)
- [Rust Core Decision Record](docs/rust-core-viability.md) — why the backend moved to Rust, and how
- [Vision](docs/vision.md) — where this is headed (low-code), and why
- [Low-code Platform V1 Direction](docs/low-code-platform-v1.md) — a concrete phased path there
