# Metap

Metap is a metadata-driven platform core.

Stack:

- Go (module at the repo root, packages under `pkg/`), chi for the HTTP runtime.
- pgx for PostgreSQL access.
- PostgreSQL as the system of record.
- RabbitMQ for integration events.
- Outbox Pattern for reliable event publishing.

The backend moved from TypeScript to Rust on 2026-08-07, then from Rust to Go — see
`docs/architectures/09-adr/00-index.md` for the decision record and `docs/roadmap.md` for status
(both moved to `../metap-docs`, see "Docs" below). **The Rust implementation (`crates/`,
`Cargo.toml`, `templates/metap-app/`) was removed from this repo once the Go port reached feature
parity** — `go/` is the sole backend implementation from that point on. A sibling repo still on
the Rust `metap` as a path dependency needs its own migration to the Go module before it will
build against this repo again.

This repo is a single Go module at the repo root (module `github.com/metap-org/metap`; it lived
under `go/` as `github.com/metap-org/metap-go` until 2026-11-07). Layout: `pkg/*` — the reusable
library packages (import as `github.com/metap-org/metap/pkg/crud` etc.); `cmd/*` — the
`outbox-publisher`/`db-migrate`/`dev-tools`/`cron-scheduler`/`notification-worker` ops binaries;
`migrations/` — the SQL migrations, also embedded into binaries as `migrations.FS`
(`cmd/db-migrate` applies the embedded set unless `MIGRATIONS_DIR` is set). No example app, no
SaaS low-code control-plane, no frontend, no Node/pnpm live here (see `CLAUDE.md`'s "No example
apps in this repo" and "No SaaS low-code control-plane crates in this repo" notes) —
`../metap-demo-crm`/`../metap-demo-jira`/`../metap-demo-waf` are real, running sibling-repo
examples, and `../metap-lowcode` is the low-code entity builder + self-service tenant provisioning
layer. Every command below runs from the repo root.

Build/test:

```bash
go build ./...
go vet ./... && go vet -tags integration ./...
gofmt -l .                                # should print nothing
golangci-lint run ./...                   # depguard: pkg/kit stays standard-library-only
go test ./...                             # unit tests only — no services needed

docker compose up -d postgres dragonfly   # integration tests: real Postgres + Redis
REDIS_URL=redis://127.0.0.1:6379 go test -tags integration ./...
# pkg/testkit gives every test its own freshly migrated database on the compose Postgres
# (localhost:5433; override with METAP_TEST_DATABASE_URL) and FAILS — never skips — when it
# can't reach it.

go generate ./pkg/crud/...                # regenerate mockery mocks (.mockery.yml)
```

To actually run something end to end (mint a token, migrate a DB, serve HTTP), go to
`../metap-demo-crm`/`../metap-demo-jira`/`../metap-demo-waf` and follow that repo's own README.

**An entity on a dedicated table needs a `reconcile()` call at boot, or its table never gets
created.** The moment you set `EntityDefinition.TableName` to a dedicated one
(`reconciler.QualifiedTableNameIn(entityName, "your_app_schema")`), your own binary's `main.go`
must call `reconciler.Reconcile(ctx, pool, tenantID, entity, nil)` for that entity before serving
traffic — see `../metap-demo-waf`'s `zones-service` for a multi-entity example with FK ordering.
Nothing in this library calls `Reconcile` for you automatically, and nothing warns you if you
forget — the table just silently never exists until you add the call.
(A low-code-authored entity, `../metap-lowcode`, is different: publishing one enqueues it and a
separate `reconciler-orchestrator` process applies it asynchronously — see that repo's own docs.)

Docs: moved to a separate repo, [`../metap-docs`](../metap-docs) (2026-08-31,
`docs/roadmap/54-docs-repo-split.md` in that repo) — every `docs/...` path below (and everywhere
else in this repo, including `CLAUDE.md`) resolves relative to that repo now, e.g.
`docs/roadmap.md` means `../metap-docs/docs/roadmap.md`.

- Architecture: `docs/architectures/index.md`
- Why This Stack: `docs/why.md`
- Roadmap: `docs/roadmap.md`
- Architecture Decisions: `docs/architectures/09-adr/00-index.md` — decision log, including why the backend moved to Rust and later to Go
- Vision: `docs/vision.md` — where this is headed (low-code), and why
- Low-code Platform V1 Direction: `docs/low-code-platform-v1.md` — a concrete phased path there
