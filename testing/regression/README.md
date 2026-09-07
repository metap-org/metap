# Regression coverage index

CI (`.github/workflows/ci.yml`) — 3 job tự động trên mọi push/PR (đã từng có `frontend`, gỡ
2026-08-31 khi `apps/crm-fe`/`apps/jira-fe` tách repo — xem `CLAUDE.md`'s "No example apps in this
repo": repo này từ đó không còn nội dung frontend nào để typecheck/lint/test — **cập nhật
2026-09-07**, bảng dưới trước đó vẫn ghi 4 job đã stale hơn 1 tuần):

| Job | Chạy gì | Khi nào |
|---|---|---|
| `rust` | build + unit test + `fmt --check` + `clippy -D warnings` | mọi push/PR |
| `security` | `cargo audit` (xem `testing/security/checklist.md`) | mọi push/PR |
| `semgrep` | SAST (logic/secrets scan) | mọi push/PR |

Ngoài `ci.yml`, còn 3 workflow riêng, không tính vào "3 job" ở trên vì không tự động trên push/PR:
`codeql.yml` (SAST report-only, push/PR/cron tuần — xem `testing/security/checklist.md`),
`nightly-benchmark.yml` (Criterion micro-benchmark report-only, cron 03:00 UTC — xem
`testing/performance/baseline.md`), và `e2e-manual.yml` ngay dưới đây.

`rust-e2e` — toàn bộ suite `#[ignore]`d qua Postgres/RabbitMQ/Redis/Vault service container thật —
**không còn tự động chạy trên push/PR** (chuyển ra `.github/workflows/e2e-manual.yml`,
2026-08-28: quá chậm so với 4 job kia cộng lại, cộng vài test nhạy với timing/data-volume của môi
trường CI song song mà một lần chạy dev bình thường không gặp — xem file đó's doc comment). Cùng
nhóm với security checklist/performance benchmark ở dưới: coverage thật, chạy chủ động, không
phải gate tự động trên từng commit. Chạy tay: `cargo test --workspace -- --ignored` (dev, cần
`docker compose up -d postgres rabbitmq`) hoặc trigger `e2e-manual.yml` thủ công trên GitHub
Actions (`gh workflow run e2e-manual.yml`).

`e2e-manual.yml` provision 4 service thật (postgres/rabbitmq/redis/vault) nhưng **không**
seaweedfs — `--exclude metap-storage` loại hẳn `crates/metap-storage/tests/s3_seaweedfs.rs`'s 3
test khỏi lần chạy đó (chúng cần `docker compose up -d seaweedfs`, không có service nào cho chúng
ở CI) thay vì để chúng luôn đỏ "connection refused". Muốn chạy 3 test đó: tay, dev machine, sau
`docker compose up -d seaweedfs` — `cargo test -p metap-storage --test s3_seaweedfs -- --ignored`.

## File `tests/*.rs` hiện có (theo crate) — snapshot 2026-09-07

Danh sách dễ lệch theo thời gian (lần trước viết 2026-08-25, thiếu 17/31 file khi soát lại) — lấy
lại bằng `find crates -path "*/tests/*.rs" -type f | sort` bất cứ khi nào nghi ngờ bảng này cũ,
đừng tin tuyệt đối vào ngày ghi ở đây.

- `metap-auth`: `oidc_e2e.rs` — round-trip OIDC authorize/callback, JIT provisioning, nonce
  replay defense
- `metap-control`: `postgres_policy_store.rs`, `provisioning_postgres.rs`, `router_postgres.rs`,
  `tenant_isolation_postgres.rs`, `vault_store.rs` (AppRole login/renewal — cần `VAULT_ADDR`/
  `VAULT_TOKEN`, roles do `e2e-manual.yml` tự provision qua Vault HTTP API)
- `metap-cron`: `cron_store_postgres.rs`, `wait_event_postgres.rs` (bao gồm tenant-isolation cho
  `WaitEvent` chain), `workflow_runs_postgres.rs`
- `metap-crud`: `crud_service_postgres.rs` (bao gồm
  `concurrent_cross_tenant_list_calls_never_return_another_tenants_records` + 3 test sustained-
  load bị `--skip` trong `e2e-manual.yml`, xem `testing/performance/baseline.md`)
- `metap-graphql`: `graphql_schema_postgres.rs` (bao gồm depth-limit — audit 04 A#7)
- `metap-graphql-gateway`: `gateway_e2e_postgres.rs`
- `metap-graphql-http`: `graphql_http_postgres.rs`
- `metap-grpc`: `grpc_backend_client_postgres.rs`, `grpc_crud_postgres.rs` (cả 2 chạy CRUD/JWT
  thật qua gRPC, không phải chỉ mock)
- `metap-http`: `http_server.rs`, `jwt_security_postgres.rs`, `cookie_session_postgres.rs`
  (session cookie + CSRF double-submit), `platform_config_postgres.rs`,
  `tenant_config_postgres.rs`, `tenant_secret_postgres.rs`
- `metap-infra`: `handler_registry_rabbitmq.rs`, `retry_policy_rabbitmq.rs` (cần `RABBITMQ_URL`)
- `metap-peripherals`: `peripherals_postgres.rs`
- `metap-permission`: `rbac_abac_integration_postgres.rs`
- `metap-query`: `query_planner_postgres.rs`
- `metap-reconciler`: `migrate_postgres.rs` (mới, `docs/features/12-migration-generic-to-dedicated-table.md`),
  `migration_postgres.rs`, `orchestrator_postgres.rs`, `reconcile_postgres.rs`
- `metap-storage`: `s3_seaweedfs.rs` (3 test, cần `docker compose up -d seaweedfs` — **không**
  chạy trong `e2e-manual.yml`, xem trên)
- `metap-workflow`: `workflow_engine_postgres.rs`

Convention: `#[ignore]`d (một `cargo test` trần không đụng DB), chạy qua
`cargo test -p <crate> -- --ignored`, cần `DATABASE_URL` (và cho vài crate là `RABBITMQ_URL`/
`REDIS_URL`/`VAULT_ADDR`+`VAULT_TOKEN`).

### `#[ignore]`d e2e test nằm trong `src/` (không phải `tests/*.rs`, dễ bị bỏ sót khi rà soát)

- `crates/metap-cache/src/redis_cache.rs`'s `#[cfg(test)] mod tests` — 3 test (`put_get_delete_
  round_trip_against_real_redis`, `same_key_different_tenants_never_collide`,
  `entries_expire_after_ttl`), cần `REDIS_URL` — `e2e-manual.yml`'s `redis` service chạy chúng.
  Tên `same_key_different_tenants_never_collide` trùng với `metap-storage`'s SeaweedFS test cùng
  tên (cùng invariant, khác backend) — đây là lý do `e2e-manual.yml` phải `--exclude metap-storage`
  thay vì `--skip` theo tên khi muốn loại chỉ 1 trong 2.

## Gap còn lại (chưa lấp)

- Không có coverage tracking (`cargo-tarpaulin`/`llvm-cov`) — chưa cần, theo hướng test tối
  giản/có mục tiêu của dự án, không phải phủ 100%.
- Không có test an toàn migration (áp migration mới lên bản sao dữ liệu "giống prod") — `rust-e2e`
  job đã áp migration lên DB rỗng mỗi lần chạy, đủ cho mục đích hiện tại.
- Không có load/perf test nào cho đường gRPC (`metap-grpc`)/GraphQL (`metap-graphql`) list/query —
  `testing/performance/`'s k6/direct-mode benchmark chỉ nhắm REST. Cả 2 đã ship và là đường tải
  thật cho `../metap-demo-waf`/`crates/metap-graphql-gateway` — ghi nhận, chưa phải việc cần làm
  ngay (2026-09-07).
