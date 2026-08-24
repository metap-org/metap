## Phase 20: Backend test kit — regression + performance + security (2026-08-23, đang làm)

Chủ dự án yêu cầu lên kế hoạch bộ test backend theo 3 trụ, kèm yêu cầu gom tool/kit vào folder
`testing/` riêng. Khảo sát hiện trạng trước khi làm (không suy đoán): CI (`rust`/`rust-e2e`/
`frontend` job) đã khá vững; gap thật lớn nhất là **security** — 0 dòng `cargo-audit`, không có
test tenant-isolation đúng invariant thiết kế đã ghim (§7 #4), RBAC/ABAC chỉ có unit test cô lập.
Plan đầy đủ + lý do từng quyết định: xem plan file phiên làm việc (`/home/minhtuan/.claude/plans/
snazzy-squishing-phoenix.md` tại thời điểm viết).

**`testing/` (mới, root)** — control tower tài liệu/checklist/baseline, **không chứa code Rust**
(test thật vẫn nằm trong `crates/*/tests/`, `crates/*/benches/` theo đúng convention Cargo, tránh
tạo abstraction dùng chung không cần thiết khi mỗi crate đã có helper cục bộ đủ dùng). Xem
`testing/README.md`.

**Trụ Security — done phần lớn:**
- `cargo audit` vào CI (job `security` mới, `.github/workflows/ci.yml`) — **verify sống**: tìm
  được 1 lỗ hổng thật (`RUSTSEC-2023-0071`, crate `rsa` qua `sqlx-mysql`), điều tra kỹ bằng
  `cargo tree`/`cargo metadata`/`cargo generate-lockfile` từ đầu trước khi kết luận: đây là
  dependency **có tên trong `Cargo.lock` nhưng không hề compile vào build thật** (`cargo-audit`
  quét toàn bộ lockfile, không quét theo feature đang active — đặc điểm đã biết của tool, không
  phải cargo update sửa được), không crate nào bật feature `mysql` của `sqlx`. Ghi nhận qua
  `.cargo/audit.toml` (ignore có lý do rõ ràng) thay vì chặn CI vô thời hạn vì lỗi chưa có bản vá.
- `crates/metap-control/tests/tenant_isolation_postgres.rs` — đúng invariant §7 #4
  (`max_connections=1`, 2 tenant thật luân phiên trên 1 connection, không rò `search_path`).
- `crates/metap-crud/tests/crud_service_postgres.rs`'s
  `concurrent_cross_tenant_list_calls_never_return_another_tenants_records` — bản CrudService-
  level, đúng hình dạng bug thật đã fix (commit `cc5f1ea`, thiếu filter `tenant_id`).
- `crates/metap-http/tests/jwt_security_postgres.rs` — 5 test (thiếu token, hết hạn, chữ ký sai,
  ký sai key, tenant khác không đọc được). **Phát hiện thật lúc verify**: test hết hạn ban đầu
  fail (200 thay vì 401) — không phải bug, `jsonwebtoken::Validation::new()` mặc định
  `leeway=60s`; sửa test ngủ qua leeway thay vì sửa code, ghi nhận leeway 60s vào checklist làm
  điểm cân nhắc siết sau (không tự ý đổi hành vi auth khi đang chỉ viết test).
- `crates/metap-permission/tests/rbac_abac_integration_postgres.rs` (crate này **trước đây không
  có thư mục `tests/`**) — 4 test: deny-by-default, role-gate, ABAC record-condition (non-admin,
  vì `is_admin()` mới bypass), deny-overrides-allow qua Postgres thật. Tự viết `TestPolicyStore`
  tái dùng `row_from_sql` đã export sẵn, không kéo `metap-control` vào làm dev-dependency (tránh
  dependency cycle, đúng lý do `PostgresPolicyStore` vốn đặt ở `metap-control` không phải ở đây).
- `testing/security/checklist.md` — sống, liệt kê đủ scenario đã cover + chưa cover.
- 1 hàng mới vào `docs/architectures/11-risks.md` ghi nhận gap đã lấp + rủi ro còn lại.

**Trụ Security — tiếp tục, SAST cho logic code tự viết:** chủ dự án hỏi riêng "tool check logic
vuln thì nên dùng gì" — `cargo audit` chỉ quét CVE dependency, không bắt lỗi logic tự viết
(injection, taint-flow...). Chọn dùng **cả 2** công cụ (yêu cầu rõ: "cả 2 dc k?"), mỗi cái một
vai trò khác nhau, không trùng lặp:
- `.github/workflows/codeql.yml` (mới) — GitHub-native, chạy CI (push/PR/cron thứ Hai hằng tuần),
  Rust. Report-only qua tab Security, không chặn build — đúng quy ước CodeQL cho ruleset mới trên
  codebase cũ (cần một vòng triage trước khi đủ tin để làm gate chặn).
- `.semgrep.yml` (mới) — dành cho quét **local** trên máy dev (yêu cầu rõ: "semgrep quét local"),
  chưa wire vào CI. **Phát hiện lúc verify sống**: `rules:` trong file `.semgrep.yml` không tự
  kéo registry pack — phải truyền `--config p/rust --config p/secrets` trên command line, merge
  với file. Verify thật: `semgrep scan --config p/rust --config p/secrets --config .semgrep.yml`
  trên toàn `crates/`+`apps/` → 1 finding, xác nhận là false positive
  (`crates/dev-tools/src/main.rs`'s `std::env::args()` dùng để chọn subcommand CLI, không phải
  mục đích bảo mật) — ghi vào `testing/security/checklist.md`, không sửa code.

**Trụ Performance — tiếp tục, bộ test tái sử dụng đa router (yêu cầu rõ: "phải xây bộ test tái sử
dụng dc cho nhiều router", "cần cả trực tiếp và http thật"):**
- `crates/metap-crud/tests/support/mod.rs` (mới) — trích xuất phần dùng chung (spawn worker, vòng
  lặp tới deadline, gom latency, tính percentile, in kết quả) từng bị lặp y hệt ở 2 test
  sustained-load cũ trong `crud_service_postgres.rs`, giờ chỉ còn phần khác biệt thật (setup
  entity/tenant, 1 iteration làm gì) ở call site. Refactor cả 2 test cũ dùng lại, cộng 1 test mới
  `sustained_concurrent_create_update_transition_delete_cycle` (create→update→transition→delete,
  60s/10 worker) — verify thật: 11.350 cycle thành công trong 60s, 0 lỗi, p50=52ms/p95=65ms/
  p99=78ms/max=155ms. Đây là "trực tiếp" (bỏ qua HTTP, đo thẳng `CrudService`, không bị giới hạn
  bởi rate-limiter tầng HTTP).
- `testing/performance/http_load_test.sh` (mới lúc đó) — tổng quát hoá từ
  `apps/crm-server/scripts/load-test.sh` cũ (vốn hardcode riêng `crm.customers`) thành tool tham
  số hoá theo `<entity> <seed_payload_template> <scenario>...`, dùng lại cho router/entity bất kỳ.
  Verify thật với 2 entity khác nhau (`crm.customers` qua wrapper, `inventory.movements` gọi
  thẳng) — cả 2 chạy sạch 0 lỗi. **Ngay sau đó bị thay hẳn bằng `crates/load-test-kit` (Rust) —
  xem mục dưới, cả 2 file `.sh` này không còn tồn tại.**

**Trụ Performance — tiếp tục, chuyển HTTP load-test từ `.sh` sang engine thật + monitoring view
(yêu cầu rõ: "viết bằng rust đi k viết bằng sh, nó k chuẩn... bộ test thì phải bao gồm cả
monitoring view dc thông số"):** một bài stress test nặng IO/CPU/RAM đúng ra cần một engine thật
với connection pooling và tính percentile đúng, không phải `curl` fork theo `xargs` + hậu xử lý
bằng `awk` — và phải xem được thông số trong lúc chạy, không chỉ đọc summary cuối cùng ở stdout.

Thử đầu tiên: tự viết `crates/load-test-kit` (binary crate Rust, `tokio`+`reqwest`, tự expose
`/metrics` cho Prometheus/Grafana). Build/verify sống thành công (chạy thật qua `crm-server`, cả
3 scenario 0 lỗi, Prometheus scrape đúng, Grafana tự nhận dashboard) — nhưng chủ dự án đánh giá
"k ok lắm rồi" và yêu cầu đổi hẳn sang **k6** (Grafana Labs' load-test engine, thay vì tự viết lại
một engine load-test từ đầu bằng tay). `crates/load-test-kit` đã bị xoá hoàn toàn (không giữ lại
dù đã verify được) — không có giá trị duy trì song song 2 công cụ làm cùng một việc.

**Giải pháp cuối cùng — k6 qua Docker** (yêu cầu tiếp: "chạy k6 bằng docker", sau khi cân nhắc cài
binary local trước rồi dừng lại theo đúng yêu cầu):
- `docker-compose.yml`'s service **`k6`** (image `grafana/k6`, `profiles: ["observability"]`,
  không phải service dài hạn — không có `ports`, chỉ chạy qua `docker compose run --rm k6 ...`),
  mount `testing/performance/k6/` vào `/scripts`. `prometheus` service thêm
  `--web.enable-remote-write-receiver` để nhận metric k6 push tới (`k6 run -o
  experimental-prometheus-rw`) — không phải scrape, vì k6 là tiến trình ngắn hạn, không phải
  target ổn định có `/metrics` port riêng.
- **`testing/performance/k6/`** — `seed.js` (seed song song qua `shared-iterations` executor) +
  `scenario.js` (bắn N request/M VU vào `<entity><querystring>` bất kỳ, hỗ trợ `LABEL=cursor` cho
  keyset pagination 2 bước qua `setup()`) — cả 2 hoàn toàn entity-agnostic, không hardcode
  `crm.customers`. `run.sh` chỉ còn vai trò orchestration mỏng (mint token qua
  `pnpm seed:admin`/`pnpm mint-token`, tuần tự hoá `docker compose run` cho seed + từng scenario,
  `sleep 65` giữa các lần vì rate limiter vẫn không tắt được cho path HTTP) — không còn tính
  percentile hay bắn request trong `.sh`, việc đó hoàn toàn do k6 làm. `pnpm loadtest:customers`
  giờ chỉ là `bash testing/performance/k6/run.sh`.
- **Monitoring view** — dashboard Grafana **"Metap — Load Test Generator (k6)"**
  (`docker/grafana/dashboards/metap-load-test.json`, tự provision) đọc `k6_http_reqs_total`,
  `k6_http_req_duration_p50/p95/p99`, `k6_http_req_failed_rate` — cạnh 2 dashboard tài nguyên đã
  có (`crm-server`, Postgres) trong cùng Grafana, xem được cả tải sinh ra lẫn CPU/RAM/DB cùng lúc.
  **Phát hiện lúc verify sống**: k6's Prometheus output mặc định chỉ export `p99` — cần
  `K6_PROMETHEUS_RW_TREND_STATS=p(50),p(95),p(99),min,max` (đúng cú pháp `p(50)`, không phải
  `p50` — thử `p50` bị k6 từ chối thẳng với lỗi "unknown format") để có đủ p50/p95 cho dashboard.
  **Phát hiện lúc verify sống thứ 2**: `${SEED_TEMPLATE:-'{"code":...{i}...}'}`  trong `run.sh` bị
  bash brace-matching parse sai — dấu `}` đầu tiên trong JSON default value bị hiểu nhầm là đóng
  `${...}`, làm hỏng JSON trước khi tới k6 (`JSON.parse` báo lỗi cú pháp). Sửa bằng
  `if [ -z "${SEED_TEMPLATE:-}" ]; then SEED_TEMPLATE='...'; fi` thay vì default value trực tiếp.
  **Verify sống cuối** (không chỉ đọc code suy luận): `docker compose --profile observability run
  --rm k6 run -o experimental-prometheus-rw /scripts/seed.js` và `/scripts/scenario.js` qua
  `crm-server` thật — 0 lỗi; `curl` Prometheus API xác nhận `k6_http_reqs_total`/
  `k6_http_req_duration_p50` có dữ liệu thật ngay sau khi remote-write; recreate `prometheus`
  container xác nhận `--web.enable-remote-write-receiver` bật đúng.

**Kiểm chứng**: toàn bộ test mới chạy thật qua Postgres dev (không mock), `cargo fmt --check` /
`cargo clippy --workspace --all-targets` / `cargo test --workspace` sạch, không regression cho
bất kỳ crate nào khác.

**Chưa làm — Trụ Regression (`testing/regression/README.md`, chỉ index, không việc mới) và phần
baseline/nightly tự động của Trụ Performance** (criterion micro-benchmark cho `plan_list`/
`diff()`, `testing/performance/{baseline.md,seed_10m.sql,run-nightly-benchmark.sh}`, workflow
`nightly-benchmark.yml`) — phần baseline hiện có (`testing/performance/baseline.md`) lấy số từ 2
benchmark direct-mode đã đo, chưa có seed/nightly workflow tự động so lệch. Semgrep cũng chưa wire
vào CI (đang chỉ dừng ở local theo đúng yêu cầu ban đầu).

**Trụ Security — tiếp tục, DAST bằng OWASP ZAP (2026-08-24, chủ dự án hỏi riêng "OWASP + open
source thì có nhanh không"):** 4 công cụ security đã có (cargo audit, CodeQL, Semgrep,
tenant/JWT/RBAC-ABAC test) đều là test nhắm đúng bug/invariant đã biết trước, không cover rộng
kiểu OWASP Top 10 (injection payload theo field, header thiếu...). `metap`'s router hoàn toàn
metadata-driven (`/api/:entity*`) nên không có danh sách route cố định để liệt kê tay cho một
scanner — giải pháp: trỏ OWASP ZAP (open-source) thẳng vào `GET /metadata/openapi.json` (route đã
public sẵn, không auth — phục vụ codegen frontend), để ZAP tự đọc ra toàn bộ entity/route rồi tự
sinh request tấn công.

`testing/security/zap/run.sh` (mới) — script orchestration mỏng, chạy tay qua `docker run
zaproxy/zap-stable`, **không CI theo đúng yêu cầu** ("chỉ cần có script... k cần tích hợp cicd").
Tự mint token (`pnpm mint-token`/`mint:jira-token`), tự inject `Authorization: Bearer` vào mọi
request ZAP bắn ra qua ZAP replacer rule (không có flag `--auth-header` sẵn). 2 chế độ: `MODE=api`
(mặc định, active scan đầy đủ qua OpenAPI import) / `MODE=baseline` (chỉ passive spider, nhanh
hơn); `APP=crm` (mặc định, port 3000) hoặc `APP=jira` (port 3100, cần `TENANT_ID`/`USER_ID` vì
tenant của jira-server không có default an toàn). Report HTML ra `testing/security/zap/reports/`
(gitignored).

**Verify sống**: chạy `MODE=api` thật nhắm `crm-server` — import 22 URL từ OpenAPI (80 URL sau khi
ZAP tự dò biến thể), active scan 119 rule (SQLi, XSS reflected/persistent/DOM, path traversal,
RCE/SSTI/XXE, CRLF, header leak...) → **0 FAIL, 0 WARN, 119 PASS**. Không thay thế 4 công cụ trên —
ZAP không hiểu multi-tenant ABAC/workflow guard của app này, chỉ là lớp phủ rộng bổ sung cho lỗ
hổng web chung chung. Ghi vào `testing/security/checklist.md`'s mục "Công cụ bổ sung".

