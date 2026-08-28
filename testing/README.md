# Backend test kit — regression, performance, security

Control tower cho 3 trụ test backend. **Không chứa code Rust** — code test/benchmark thật nằm
trong từng crate (`crates/*/tests/*.rs`, `crates/*/benches/*.rs`), đúng convention Cargo. Thư
mục này chỉ có tài liệu điều phối, checklist sống, baseline số liệu, script gọi tới test đã có.

Kế hoạch gốc: `docs/architectures/11-risks.md` (hàng ghi nhận gap test-coverage bảo mật).

## Regression

`.github/workflows/ci.yml`'s `rust`/`frontend` job (mọi push/PR) + `e2e-manual.yml`'s `rust-e2e`
job (chạy tay/trigger thủ công, 2026-08-28 — xem file đó's doc comment) đã cover phần lớn. Chi
tiết + gap còn lại: [`regression/README.md`](regression/README.md).

## Performance

Chưa có gate tự động dài hạn (baseline/nightly) — nhưng đã có 2 công cụ load-test **tái sử dụng
được cho nhiều router/entity**, không hardcode `crm.customers`, và không có logic stress-test nào
nằm trong file `.sh` (một bài stress test nặng IO/CPU/RAM cần engine thật, không phải `curl` fork
qua `xargs` + hậu xử lý `awk` — file `.sh` ở đây chỉ còn vai trò orchestration mỏng: seed token +
tuần tự hoá lệnh `docker`, không còn tính percentile hay bắn request):

- **Direct mode** (bỏ qua HTTP, đo thẳng `CrudService`) —
  [`crates/metap-crud/tests/support/mod.rs`](../crates/metap-crud/tests/support/mod.rs)'s
  `run_sustained_load`: spawn N worker, chạy vòng lặp tới deadline, gom p50/p95/p99/throughput.
  Dùng ở 3 test trong `crud_service_postgres.rs`
  (`sustained_concurrent_list_against_a_real_multi_entity_abac_workflow`,
  `sustained_concurrent_list_across_many_tenants_at_ten_million_rows`,
  `sustained_concurrent_create_update_transition_delete_cycle`). Đo capacity thật không bị giới
  hạn bởi rate-limiter tầng HTTP. Vẫn hit chung Postgres — dashboard "Metap — Postgres Resource
  Metrics" (bên dưới) là "monitoring view" đúng nghĩa cho chế độ này, xem trực tiếp trong lúc
  test chạy, không cần thêm gì.
- **HTTP mode** (qua thật `axum` router, có auth/rate-limit/CORS) —
  [`k6`](https://k6.io) (Grafana's own load-test engine, chạy qua Docker — `docker-compose.yml`'s
  service `k6`, image `grafana/k6`) với 2 script tái sử dụng ở
  [`performance/k6/`](performance/k6/): `seed.js` (seed dữ liệu song song) + `scenario.js`
  (bắn N request qua M VU vào `<entity><querystring>` bất kỳ, hỗ trợ keyset-cursor 2 bước). Chạy
  qua:
  ```bash
  pnpm loadtest:customers   # scenario set cũ (list/filter+sort/cursor) cho crm.customers
  ENTITY=inventory.movements SEED_TEMPLATE='{"sku":"SKU-{i}",...}' ./testing/performance/k6/run.sh
  ```
  `run.sh` chỉ làm 2 việc không phải "stress logic": mint token qua `pnpm seed:admin`/
  `pnpm mint-token` (đúng logic `dev-tools` đã có), và tuần tự hoá `docker compose run --rm k6`
  cho seed + từng scenario, `sleep 65` giữa các lần (**Rate limiter không tắt được** cho path này
  — mỗi scenario phải chờ bucket đầy lại). Toàn bộ việc bắn request thật, đếm percentile, format
  báo cáo cuối là k6 tự làm.

  **Monitoring view khi chạy HTTP mode** — k6 tự push metric qua Prometheus remote-write
  (`k6 run -o experimental-prometheus-rw`, cấu hình trong `run.sh`) tới cùng Prometheus đã có
  trong `observability` profile (`docker-compose.yml`'s `prometheus` service bật
  `--web.enable-remote-write-receiver`) — `k6_http_reqs_total{scenario,status}`,
  `k6_http_req_duration_p50/p95/p99`, `k6_http_req_failed_rate`. Dashboard Grafana **"Metap —
  Load Test Generator (k6)"** (tự provision, `docker/grafana/dashboards/metap-load-test.json`)
  hiển thị requests/sec theo scenario+status, failed rate, p50/p95/p99 client-side — cạnh 2
  dashboard tài nguyên đã có ("crm-server Resource Metrics", "Postgres Resource Metrics") trong
  cùng 1 Grafana (`http://localhost:3001`, cần `docker compose --profile observability up -d`),
  nên theo dõi được cả 3: tải sinh ra + CPU/RAM của `crm-server` + tài nguyên Postgres, cùng lúc,
  trong khi bài stress test đang chạy.

Baseline số liệu tham chiếu (2 benchmark direct-mode đã đo): [`performance/baseline.md`](performance/baseline.md).
Phần seed/nightly-workflow tự động so baseline (`seed_10m.sql`, `run-nightly-benchmark.sh`,
`.github/workflows/nightly-benchmark.yml`) trong kế hoạch gốc **chưa làm** — xem "Định hướng chưa
lên phase" trong `docs/roadmap.md`.

## Security

Trụ ưu tiên cao nhất — gap lớn nhất khi khảo sát (2026-08-23): 0 dòng `cargo-audit`, 0 test
tenant-isolation đúng invariant thiết kế đã ghim, RBAC/ABAC chỉ có unit test cô lập, 0 SAST cho
logic code tự viết. Checklist sống: [`security/checklist.md`](security/checklist.md).

```bash
# Chạy toàn bộ test bảo mật mới (cần Postgres dev đang chạy — docker compose up -d postgres):
DATABASE_URL=postgres://metap:metap@localhost:5433/metap cargo test --release \
  -p metap-control --test tenant_isolation_postgres -- --ignored --nocapture
DATABASE_URL=postgres://metap:metap@localhost:5433/metap cargo test --release \
  -p metap-crud --test crud_service_postgres concurrent_cross_tenant_list_calls_never_return_another_tenants_records -- --ignored --nocapture
DATABASE_URL=postgres://metap:metap@localhost:5433/metap cargo test --release \
  -p metap-http --test jwt_security_postgres -- --ignored --nocapture
DATABASE_URL=postgres://metap:metap@localhost:5433/metap cargo test --release \
  -p metap-permission --test rbac_abac_integration_postgres -- --ignored --nocapture

# Quét lỗ hổng dependency (cần cargo-audit: cargo install cargo-audit):
cargo audit

# Quét logic-vuln local trước khi push (cần semgrep: pipx install semgrep):
semgrep scan --config p/rust --config p/secrets --config .semgrep.yml
```

SAST cho logic code tự viết (khác `cargo audit`, vốn chỉ quét CVE dependency) dùng **cả hai**
công cụ, mỗi cái một vai trò:
- **CodeQL** (`.github/workflows/codeql.yml`) — chạy trong CI (push/PR/cron tuần), report-only
  qua tab Security của repo, không chặn build.
- **Semgrep** (`.semgrep.yml`) — chạy tay trên máy dev, phản hồi nhanh trước khi push, chưa wire
  vào CI.

### DAST — OWASP ZAP (chạy tay, không CI)

Cả 3 bộ trên đều là test nhắm đúng bug/invariant đã biết trước — không cover rộng kiểu OWASP Top
10 (injection payload theo từng field, header thiếu, v.v). `metap`'s router hoàn toàn
metadata-driven (`/api/:entity*`) nên không có danh sách route cố định để liệt kê tay cho một
scanner — thay vào đó trỏ [OWASP ZAP](https://www.zaproxy.org/) (open-source) thẳng vào
`GET /metadata/openapi.json` (route này vốn public, không auth — sinh cho bước codegen frontend,
xem CLAUDE.md's "Metadata types stay generated"), để ZAP tự đọc ra toàn bộ entity/route rồi tự
sinh request tấn công theo từng field.

```bash
./testing/security/zap/run.sh                                        # crm-server, api mode (đầy đủ)
MODE=baseline ./testing/security/zap/run.sh                          # crm-server, chỉ passive scan (nhanh)
APP=jira TENANT_ID=<uuid> USER_ID=<uuid> ./testing/security/zap/run.sh   # jira-server
```

Script chỉ orchestration mỏng (mint token qua `pnpm mint-token`/`mint:jira-token` có sẵn, tự inject
`Authorization: Bearer` vào mọi request ZAP bắn qua ZAP replacer rule, `docker run
zaproxy/zap-stable`) — không có logic scan nào tự viết. Report HTML ra
`testing/security/zap/reports/` (gitignored). Công cụ tay, **không** wire CI — chạy trước khi push
thay đổi lớn liên quan tới route/auth, không phải gate tự động. Không thay thế 4 bộ test tenant-
isolation/JWT/RBAC-ABAC ở trên — ZAP không hiểu multi-tenant ABAC/workflow guard của app này,
đây chỉ là lớp phủ rộng bổ sung cho các lỗ hổng web chung chung.
