# Metap

Metap is a metadata-driven platform core.

Chosen stack:

- axum for the HTTP runtime.
- sqlx for PostgreSQL access.
- PostgreSQL as the system of record.
- RabbitMQ for integration events.
- Outbox Pattern for reliable event publishing.

The backend moved from TypeScript to Rust on 2026-08-07 — see [`docs/architectures/09-adr.md`](docs/architectures/09-adr.md) for the decision record, and [`docs/roadmap.md`](docs/roadmap.md)'s Phase 12 for status.

This repo is a pure Cargo workspace — `crates/` holds the `metap-*` library crates plus the
`outbox-publisher`/`db-migrate`/`dev-tools`/`graphql-gateway`/... ops binaries built on them. No
example app, no frontend, no Node/pnpm live here (see `CLAUDE.md`'s "No example apps in this
repo" note) — `templates/metap-app/` is the `cargo generate` starting point for a new downstream
project, and `../metap-demo-crm`/`../metap-demo-jira`/`../metap-demo-waf` are real, running
sibling-repo examples built on this one via a `path` dependency. Every command below runs from
the repo root.

Build/test:

```bash
docker compose up -d postgres rabbitmq   # needed for the e2e tests below
cargo build --workspace
cargo fmt --all --check
cargo clippy --workspace --all-targets -- -D warnings
cargo test --workspace                   # unit tests, no DB needed
cargo test --workspace -- --ignored      # e2e tests — needs DATABASE_URL + the dev Postgres/RabbitMQ up
```

To actually run something end to end (mint a token, migrate a DB, serve HTTP), go to
`../metap-demo-crm`/`../metap-demo-jira`/`../metap-demo-waf` and follow that repo's own README.

Docs:

- [Architecture](docs/architectures/index.md)
- [Why This Stack](docs/why.md)
- [Roadmap](docs/roadmap.md)
- [Architecture Decisions](docs/architectures/09-adr.md) — decision log, including why the backend moved to Rust
- [Vision](docs/vision.md) — where this is headed (low-code), and why
- [Low-code Platform V1 Direction](docs/low-code-platform-v1.md) — a concrete phased path there
