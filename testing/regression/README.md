# Regression coverage index

CI (`.github/workflows/ci.yml`) — 4 job:

| Job | Chạy gì | Khi nào |
|---|---|---|
| `rust` | build + unit test + `fmt --check` + `clippy -D warnings` | mọi push/PR |
| `security` | `cargo audit` (xem `testing/security/checklist.md`) | mọi push/PR |
| `rust-e2e` | toàn bộ suite `#[ignore]`d qua Postgres/RabbitMQ service container thật | mọi push/PR |
| `frontend` | typecheck + lint + format check + vitest | mọi push/PR |

## File `tests/*_postgres.rs` hiện có (theo crate)

- `metap-control`: `provisioning_postgres.rs`, `router_postgres.rs`, `tenant_isolation_postgres.rs` (mới)
- `metap-cron`: `cron_store_postgres.rs`
- `metap-crud`: `crud_service_postgres.rs` (bao gồm `concurrent_cross_tenant_list_calls_never_return_another_tenants_records`, mới)
- `metap-http`: `http_server.rs`, `jwt_security_postgres.rs` (mới)
- `metap-peripherals`: `peripherals_postgres.rs`
- `metap-permission`: `rbac_abac_integration_postgres.rs` (mới — crate này trước đây không có thư mục `tests/`)
- `metap-query`: `query_planner_postgres.rs`
- `metap-reconciler`: `migration_postgres.rs`, `orchestrator_postgres.rs`, `reconcile_postgres.rs`
- `metap-workflow`: `workflow_engine_postgres.rs`

Convention: `#[ignore]`d (một `cargo test` trần không đụng DB), chạy qua
`cargo test -p <crate> -- --ignored`, cần `DATABASE_URL` (và cho vài crate là `RABBITMQ_URL`).

## Gap còn lại (chưa lấp)

- Không có coverage tracking (`cargo-tarpaulin`/`llvm-cov`) — chưa cần, theo hướng test tối
  giản/có mục tiêu của dự án, không phải phủ 100%.
- Không có test an toàn migration (áp migration mới lên bản sao dữ liệu "giống prod") — `rust-e2e`
  job đã áp migration lên DB rỗng mỗi lần chạy, đủ cho mục đích hiện tại.
