# Regression coverage index

CI (`.github/workflows/ci.yml`) — 4 job tự động trên mọi push/PR:

| Job | Chạy gì | Khi nào |
|---|---|---|
| `rust` | build + unit test + `fmt --check` + `clippy -D warnings` | mọi push/PR |
| `security` | `cargo audit` (xem `testing/security/checklist.md`) | mọi push/PR |
| `semgrep` | SAST (logic/secrets scan) | mọi push/PR |
| `frontend` | typecheck + lint + format check + vitest | mọi push/PR |

`rust-e2e` — toàn bộ suite `#[ignore]`d qua Postgres/RabbitMQ/Redis/Vault service container thật —
**không còn tự động chạy trên push/PR** (chuyển ra `.github/workflows/e2e-manual.yml`,
2026-08-28: quá chậm so với 4 job kia cộng lại, cộng vài test nhạy với timing/data-volume của môi
trường CI song song mà một lần chạy dev bình thường không gặp — xem file đó's doc comment). Cùng
nhóm với security checklist/performance benchmark ở dưới: coverage thật, chạy chủ động, không
phải gate tự động trên từng commit. Chạy tay: `cargo test --workspace -- --ignored` (dev, cần
`docker compose up -d postgres rabbitmq`) hoặc trigger `e2e-manual.yml` thủ công trên GitHub
Actions (`gh workflow run e2e-manual.yml`).

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
