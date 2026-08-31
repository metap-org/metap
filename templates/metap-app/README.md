# {{project-name}}

Generated from [metap](https://github.com/tuannm99/metap)'s `templates/metap-app` (via
`cargo generate`) — a metadata-driven ERP/platform core. `src/example_entity.rs` is a
starting point; replace it with your own [`EntityDefinition`], register more of them in
`src/main.rs`, and everything else (CRUD, list/filter/sort, permissions, workflow, the
outbox pattern) comes from the `metap` crate.

## First-time setup

```bash
cp .env.example .env                      # then edit DATABASE_URL/RABBITMQ_URL for your own Postgres/RabbitMQ
docker compose -f <your-compose-file> up -d   # or point .env at existing instances

# metap-* isn't on crates.io yet, so its dev tooling is installed via git too:
cargo install --git https://github.com/tuannm99/metap --branch poc/rust-core db-migrate dev-tools

db-migrate                                # applies metap's crates/migrations/*.sql to a fresh DB
dev-tools gen-keys                        # generates ./keys/dev-jwt-{private,public}.pem
dev-tools mint-token                      # mints a dev JWT (fixed dev tenant/user by default)
dev-tools seed-admin <tenantId> <userId>  # grants the 'admin' role to the tenant/user you minted a token for
```

(Match `--branch poc/rust-core` to whatever `metap_rev` you answered when generating this
project — see `Cargo.toml`'s `metap` dependency.)

## Run

```bash
cargo run                                 # binary name is fixed as `app`, see Cargo.toml
```

Paste the minted token as a `Bearer` token against `http://localhost:3000/api/example.tasks`
(or whatever entity/port you configured). `GET /health` and `GET /metadata/openapi.json` are
public, no token needed.

## Test

```bash
cargo test                                # unit tests, no DB needed
cargo test -- --ignored                   # e2e tests in tests/, needs DATABASE_URL/RABBITMQ_URL
```

## Docker

```bash
docker build -t {{project-name}} .
docker run -p 3000:3000 \
  -v ./keys:/app/keys:ro \
  -e DATABASE_URL=... -e RABBITMQ_URL=... \
  -e AUTH_JWT_PUBLIC_KEY_PATH=/app/keys/dev-jwt-public.pem \
  {{project-name}}
```

No secrets are baked into the image — see the `Dockerfile`'s own comments.

## Writing a custom route/handler beyond entity declaration

Not every feature fits the metadata-driven entity model. `src/main.rs`'s `PlatformParts` (from
`metap::app::bootstrap_platform`, re-exported at `metap::prelude`) gives you a tenant-aware
`Router` and `PermissionService` you can build a real hand-written handler on:
- Mount a custom `axum::Router<AppState>` via `build_router`'s `extra_routes` argument.
- Resolve the caller's tenant-scoped `PgPool` with `state.router.pool_for(tenant_id)` — never a
  bare pool.
- Extract `metap::http::{AuthContext, AdminContext}` in the handler signature for a
  permission-aware route — no separate middleware needed.
- Publish an event with `metap::infra::outbox::enqueue(executor, &event)` in the same
  transaction as your write; `metap::infra::EventBus::subscribe` reads them back.

See `metap-lowcode-http` in the `metap` repo's sibling `metap-lowcode` repo for a full working
example of this exact pattern, not just a hypothetical.

## What's not included here

This template covers the backend only. For a frontend, see
[`packages/platform-react`](https://github.com/tuannm99/metap/tree/poc/rust-core/packages/platform-react)
in the metap repo (not yet published as an installable package — vendor it or depend on it
via a git dependency the same way `metap` itself is depended on above).
