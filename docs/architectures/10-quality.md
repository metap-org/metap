# 10. Quality Requirements

## Quality Tree

1. **Correctness / Data Integrity** — a write must never silently corrupt or lose data, even under concurrency or partial infrastructure failure.
2. **Security** — tenant isolation and permission enforcement must hold regardless of what the client sends; nothing security-relevant is client-trusted.
3. **Maintainability** — a broken entity definition or a layering violation must be caught early (at boot or in review), not discovered in production.
4. **Performance** — list/filter/search operations on hot fields must be backed by real indexes, not full scans, and must never return unbounded result sets.

## Quality Scenarios (concrete, testable)

| # | Scenario | Mechanism | Verified by |
|---|---|---|---|
| 1 | Two clients update the same record concurrently | `UPDATE ... WHERE version = $expected_version`; the loser gets `409 version_conflict`, never a silent overwrite | `metap-crud` tests |
| 2 | RabbitMQ is unreachable when a record is created | The business write commits regardless (outbox pattern); the event ships once the publisher can reach RabbitMQ again | `outbox-publisher`/`metap-infra` tests |
| 3 | An admin lists records while a record-level policy scoped to a non-admin role exists | Admin bypass is checked first in `record_policy_where_clause` — the admin's results are never emptied by a policy that doesn't apply to them | `metap-crud`/`metap-query` tests (regression test, originally added in the TS codebase 2026-08-01 after this was found as a real bug, reimplemented in the Rust port) |
| 4 | A field-level read policy denies a field that's mirrored into a top-level `records` column (`code`/`status`) | `CrudService`'s record-masking path masks both the JSONB copy and the mirrored column, not just one | `metap-crud` tests |
| 5 | A new entity module has a duplicate field name or a listView referencing an unknown field | `MetadataCompiler::validate` errors at `MetadataRegistry::register()` — the app fails to boot, not the first request | `metap-metadata` tests |
| 6 | The database is unreachable when `IndexReconciler`/`MetadataDriftService` run at boot | Both catch and log a warning; boot continues | `metap-peripherals` tests |
| 7 | A client sends a cursor generated under a different sort, or a garbage cursor string | `plan_list` returns an invalid-cursor error, `CrudService` maps it to `400 invalid_cursor` — never a `500` | `metap-query` tests |
| 8 | A client sends a hostile filter value (e.g. `"active' OR '1'='1"`) | Treated as literal data via bound SQL parameters (`sqlx`) — never string-concatenated into the query | `metap-query` tests |

Run unit tests (no DB needed) with `pnpm test:rs` (`cargo test --workspace`); e2e tests (real Postgres/RabbitMQ, live HTTP server, real RS256 JWT) with `pnpm test:rs:e2e` (`cargo test --workspace -- --ignored`).

## Notes

- No load/performance testing exists yet — quality scenario 4 (Performance) above is validated by design (index-usage regression tests using `EXPLAIN`) but not by measured throughput/latency under realistic load. Tracked in [11. Risks and Technical Debt](11-risks.md).
- Test scope in this codebase is deliberately minimal and targeted (a handful of important cases per feature), not an exhaustive matrix — see [09. Architecture Decisions](09-adr.md) and `docs/rust-core-viability.md` for the Rust port's test counts (51 unit + 19 e2e as of 2026-08-08).
