## Phase 8: Hardening

**Trạng thái: Đang làm** — bắt đầu 2026-08-09. Bản port HTTP layer ban đầu đã có chủ đích
deferred toàn bộ gap phía Rust của phase này (header tương đương helmet, rate limiting,
requestId/traceId) ra khỏi phạm vi của nó;
gap đó là thứ được đóng lại đầu tiên, tiếp theo là các mục tiêu hạ tầng Docker/CI bên dưới.

Mục tiêu:

- **Tích hợp secret manager** — **Vault impl xong 2026-08-17** (`metap-control::VaultStore`, xem
  Phase 16 Giai đoạn 4; AppRole + auto-renewal thêm 2026-08-20), đúng theo hướng thiết kế đã ghi
  trước đó ở `docs/architectures/07-deployment.md`'s "Secret manager — hướng thiết kế":
  `metap-control::SecretStore` trait (xây cho Phase 16's `DedicatedDb`) chỉ cần thêm một impl
  mới của cùng trait, không phải thiết kế lại. **2026-08-28: thêm 2 impl cloud** —
  `AwsSecretsManagerStore` (credentials tường minh qua `Credentials`/`Region`/`Builder`, cùng
  style `metap-storage::S3ObjectStore` đã có, không dùng default credential chain của SDK) và
  `GcpSecretManagerStore` (Application Default Credentials — GCP không có khái niệm access
  key/secret key như AWS) — cả 2 kỳ vọng payload cùng shape JSON `{"dsn": "..."}` như Vault's
  `DsnSecret`, để một operator có 1 mental model chung dù chạy backend nào.
  `metap_control::build_secret_store(&AppConfig)` (hàm mới) gom logic chọn 1 trong 4 backend từ
  env — trước đó mỗi binary (`crm-server`/`jira-server`/`reconciler-orchestrator`) tự lặp lại
  đoạn `match` Vault/Env, giờ gọi chung 1 hàm nên không thể lệch nhau; thêm 4 field mới vào
  `metap-infra::AppConfig` (`aws_secrets_*`, `gcp_secrets_project_id`). `dev-tools
  {vault,aws-secrets,gcp-secrets}-put-dsn <dsnSecretRef> <dsn>` là bộ 3 lệnh ghi tay tương ứng.
  Vẫn chỉ mới bao phủ `dsn_secret_ref` của `DedicatedDb` (không phải AppRole/dynamic
  database-credentials engine cho Vault — vẫn deferred tới khi có trigger production thật, không
  đổi so với trước). `AppConfig` (đọc `DATABASE_URL`/`RABBITMQ_URL`/JWT key path từ env) là phạm
  vi rộng hơn còn chưa qua abstraction nào, cần mở rộng riêng — config hiện tại vẫn là file
  `.env` (phù hợp cho dev, không phải tư thế production). Vẫn chưa có production deployment
  topology nào được chốt (cloud secret manager của provider nào, nếu không self-host Vault) —
  quyết định đó vẫn thuộc về lúc chọn hạ tầng production thật, không chặn phần đã làm ở trên; cả
  4 backend chỉ mới build/clippy/fmt sạch + unit test, **chưa verify sống với 1 Vault/AWS/GCP
  thật** (sandbox lúc code không có mạng tới Vault/AWS/GCP để test, cũng không có tài khoản
  cloud thật để thử) — cần verify tay trước khi dùng backend nào đó cho production thật.
- ~~CORS allowlist theo environment~~ — **Đã xong**, có trước khi phase này được track:
  `CORS_ORIGINS` (`crates/metap-infra/src/config.rs`) là một env var theo từng environment,
  phân tách bằng dấu phẩy, chỉ mặc định rỗng (permissive `CorsLayer::new()`) khi không được
  set — xem doc comment của `metap_http::build_router` để biết ràng buộc `allow_credentials` +
  explicit-origin-list mà nó enforce.
- ~~Security header tương đương helmet~~ — **Đã xong (2026-08-09)**:
  `crates/metap-http/src/security_headers.rs`, áp dụng toàn cục trong `build_router` (bao phủ
  cả static SPA fallback của `apps/crm-server`, không chỉ `/api`/`/metadata`) —
  Content-Security-Policy (mặc định dựa trên `'self'` của helmet, an toàn cho một SPA
  same-origin), X-Frame-Options, X-Content-Type-Options, Referrer-Policy,
  Strict-Transport-Security, Cross-Origin-Opener/Resource-Policy, và phần còn lại của bộ mặc
  định của helmet.
- CSP — xem "Security header tương đương helmet" ở trên; gộp vào đó thay vì track riêng, vì
  axum không có crate tương đương helmet để configure một CSP directive.
- HTML sanitizer / File scanning hook — Chưa áp dụng được: đây là một API chỉ dùng JSON,
  không render HTML và không có endpoint upload file. Xem lại nếu một trong hai được thêm vào.
- ~~Rate limiting~~ (không phải mục tiêu gốc của Phase 8, thêm vào từ gap riêng của Rust ở
  trên) — **Đã xong (2026-08-09)**: `tower_governor`, key theo peer IP, ~300 req/phút (một
  xấp xỉ token-bucket của fixed-window mặc định cũ của `@fastify/rate-limit` — xem doc comment
  của `build_router`), trả 429 với cùng shape error-body `too_many_requests` như mọi error
  response khác. Cần binary phục vụ dùng
  `into_make_service_with_connect_info::<SocketAddr>()` — cả `apps/crm-server/src/main.rs`
  và e2e test của `metap-http` đều dùng.
- ~~Lan truyền requestId/traceId~~ (gap còn lại riêng của Rust) — **Đã xong (2026-08-09)**:
  `crates/metap-http/src/request_context.rs`, response header `x-request-id`/`x-trace-id`
  trên mọi request, `x-trace-id` được echo lại khi caller gửi một id hợp lệ, và cả hai id
  được inject vào mọi JSON error body 4xx/5xx một cách tập trung (không phải luồn qua ~30
  call site riêng lẻ của `service_error_response`/`internal_error_response`).
- ~~Docker image non-root~~ — **Đã xong (2026-08-09)**: `apps/crm-server/Dockerfile` —
  Dockerfile đầu tiên trong repo, đặt cạnh example app mà nó đóng gói thay vì ở repo root
  (cùng lý do như `keys/`/`.env` riêng của `apps/crm-server`: đây là Dockerfile riêng của
  example app này, không phải "cái" Dockerfile của repo — một downstream project tự build
  binary tương đương của riêng nó và tự viết Dockerfile tương tự cho nó, giống như tự viết
  `main.rs` riêng thay vì import cái này). Build context vẫn là repo root
  (`docker build -f apps/crm-server/Dockerfile .`) vì cả Cargo workspace lẫn pnpm workspace
  đều sống ở đó. Multi-stage (`node:24-slim` để build static cho `apps/crm-fe`,
  `rust:1-slim-bookworm` cho `crm-server --release`, `debian:bookworm-slim` làm runtime),
  không bake secret nào vào image (đường dẫn DB/RabbitMQ/JWT key đều được đọc từ environment
  lúc container start, giống convention `.env` local — bản thân JWT key được mount vào, không
  copy vào image), chạy dưới một user non-root cố định `metap` (uid/gid 10001). Đã verify
  bằng cách thực sự build image và chạy nó với một dev Postgres/RabbitMQ đang sống
  (`docker run --entrypoint id` xác nhận `uid=10001(metap)`, `curl /health` trả về 200 kèm
  đầy đủ mọi hardening header).
- ~~CI checks~~ — **Đã xong (2026-08-09)**: `.github/workflows/ci.yml`, ba job — `rust`
  (build + unit test + clippy, không cần DB), `rust-e2e` (service container Postgres/RabbitMQ
  mirror lại credential của `docker-compose.yml`, `db-migrate` trên một DB mới tinh, rồi chạy
  toàn bộ e2e suite `--ignored`), `frontend` (typecheck/lint/format:check/test). Đã verify
  bằng cách thực sự chạy cùng chuỗi đó local trên các container Postgres/RabbitMQ dùng một
  lần (migration trên DB mới + toàn bộ e2e suite pass) thay vì chỉ tin vào file YAML.
  `clippy -D warnings`/`fmt --check` đã strict thật từ 2026-08-16 (xem bullet "Clippy chưa gate"
  bên dưới) — dòng này ban đầu (2026-08-09) nói ngược lại, đã lỗi thời, sửa lại 2026-08-22. Vẫn
  còn thiếu: chưa enforce như một merge gate thật (chưa configure branch protection trên GitHub —
  một cấu hình repo, không phải code).
- ~~Structured logging / observability~~ (không phải mục tiêu gốc của Phase 8 — thêm vào
  2026-08-09 sau khi một audit phát hiện các crate core gần như không có logging:
  `metap-crud`, `metap-permission`, `metap-query`, `metap-workflow` không có gì cả, và nơi
  duy nhất có log — 500 handler của `metap-http` — thậm chí còn không mang theo
  `requestId`/`traceId` mà response body đã có sẵn, nên một id do client report không thể
  grep khớp với log server) — **Đã xong (2026-08-09)**: `tracing` + `tracing-subscriber`
  được wire qua `metap_infra::init_tracing()` (một init dùng chung, được gọi đầu tiên bởi mọi
  binary — `crm-server`, `outbox-publisher`, `notification-worker`, `db-migrate` — đọc
  `RUST_LOG`, mặc định `info`; `dev-tools` cố tình bị loại trừ, stdout của nó là CLI output —
  một token vừa mint, một usage message — không phải log stream).
  `crates/metap-http/src/request_id.rs` (mới, middleware ngoài cùng) sinh cặp
  request/trace id một lần vào request extension; `tower_http::trace::TraceLayer` (cũng mới,
  bọc quanh mọi layer khác) build một span cho mỗi request mang theo cả hai id cùng
  method/path/status/latency, nên **bất kỳ** `tracing` event nào được log ở downstream — một
  lần từ chối permission trong `metap-permission`, một lỗi validation trong `metap-crud`, một
  filter bị reject trong `metap-query` — đều tự động được correlate với cùng id mà client
  nhìn thấy, không cần luồn id qua chữ ký hàm của bất kỳ crate nào trong số đó.
  `request_context.rs` giờ đọc cùng các id đó từ extension thay vì tự sinh id riêng. Đã
  instrument các điểm quyết định trước đây im lặng: allow/deny permission
  (`metap-permission`), filter/sort field bị reject/bỏ qua và cursor không hợp lệ
  (`metap-query`), và trong `metap-crud::CrudService` — entity/record không tìm thấy, lỗi
  validation (kèm tên field vi phạm), version conflict, và toàn bộ chuỗi transition-rejection
  (không có workflow, không có transition định nghĩa, guard fail) cộng với log INFO-level cho
  các lần create/update/transition/delete thành công. Cố tình *chưa* làm: chưa có JSON/OTLP
  exporter (chỉ log ra stderr dạng plain text — chưa có aggregator nào để gửi tới, cùng gap
  với "chưa document production deployment topology" của các mục tiêu Docker/CI); xem lại khi
  có. Đã verify live trên một Postgres/RabbitMQ/crm-server thật (không chỉ `cargo build`): hit
  `/health`, một route chưa auth, một entity không tồn tại, và một `create` payload rỗng — xác
  nhận dòng access-log và các log quyết định của `metap-crud`/`metap-permission` đều mang
  cùng `request_id`/`trace_id` và nằm lồng trong cùng một span.
- **load test cho list/query/export** — **Đã xong 2026-08-17.** Không có endpoint export riêng
  (report/export vẫn là một `listView` thứ hai trên cùng `GET /api/:entity`, xem Phase 4 ở
  trên) nên load test nhắm vào chính path list/query đó:
  `apps/crm-server/scripts/load-test.sh` (script thủ công, cùng kiểu `smoke.sh`, không dùng
  binary ngoài như k6/hey — chỉ `curl` + `xargs -P`; **thay thế bằng k6 qua Docker ở Phase 20
  (`testing/performance/k6/`), xem bên dưới** — file `.sh` này không còn tồn tại). Chạy thật với 200 row seed +
  3 kịch bản × 250 request/kịch bản (limit=50; filter+sort `status=active&sort=-createdAt`;
  keyset pagination trang 2), concurrency 20, nhắm vào `crm-server` debug build + dev Postgres
  local: **p50 12-50ms, p95 66-118ms, p99 79-137ms, 0 lỗi** trên cả 3 kịch bản. Phát hiện đáng
  chú ý trong lúc build script: rate limiter Phase 8 (`tower_governor`, burst 300 @ 5/giây, key
  theo peer IP) dùng chung một token bucket cho *mọi* route — chạy seed + scenario liên tiếp từ
  cùng một IP (như một script thủ công trên một máy) sẽ tự làm cạn bucket của chính nó và gây
  429 hàng loạt, không phải lỗi ở query path. Script tự đợi bucket refill đầy (~65s) trước mỗi
  kịch bản để số đo là latency thật của query, không lẫn hiệu ứng rate-limit; ghi lại rõ trong
  comment của script cho lần chạy sau. Debug build (không phải `--release`), một máy dev — số
  liệu này là baseline tương đối, không phải benchmark production.
- **backup/restore drill** — **Đã xong 2026-08-17.** `apps/crm-server/scripts/backup-restore-drill.sh`
  — `pg_dump -Fc` dev Postgres (qua `docker compose exec postgres`) thật, `pg_restore` vào một
  database tạm trên cùng container, rồi diff row-count chính xác (`count(*)`, không dùng
  `pg_stat_user_tables.n_live_tup` — đó chỉ là ước lượng theo ANALYZE, không đáng tin cho một
  diff ngay-sau-restore) trên toàn bộ bảng ở cả 2 schema (`public` + `control`) giữa DB gốc và
  DB restore. Chạy thật, verify khớp tuyệt đối trên cả 13 bảng (bao gồm `control.tenants` từ
  Phase 16). Không phải pipeline backup production (không upload off-site, không retention
  policy, không lịch chạy định kỳ) — chưa có target triển khai production nào để wire vào,
  cùng gap với mục secret manager bên dưới; đây là drill xác nhận cơ chế `pg_dump`/`pg_restore`
  hoạt động đúng trên schema thật của repo, chạy tay khi cần.
- **TS `strict` tắt cả 2 tsconfig** (`apps/crm-fe`, `packages/platform-react`) — **Đã xong
  2026-08-16.** Bật `"strict": true` + `noUncheckedIndexedAccess` ở cả 3 tsconfig
  (`apps/crm-fe/tsconfig.app.json`, `tsconfig.node.json`, `packages/platform-react/tsconfig.json`).
  `tsc -b --force`/`tsc --noEmit` sạch ngay, không phát sinh lỗi type nào cần sửa.
- **`opt-level = "z"` cho server backend** (`Cargo.toml`) — **Đã xong 2026-08-16.** Đổi
  `[profile.release]` sang `opt-level = 3`.
- **Clippy chưa gate, thiếu `rustfmt.toml`** — **Đã xong 2026-08-16.** Thêm
  `[workspace.lints.clippy]` (`Cargo.toml` gốc), commit `rustfmt.toml` (`max_width = 120` — khớp
  độ rộng dòng thực tế của codebase, không dùng mặc định 100 để tránh diff cơ học quá lớn không
  cần thiết), dọn 5 warning clippy có sẵn (redundant guard, derivable impl, 2×result_large_err,
  1 warning mới tự phát sinh ở `metap-control`), chạy `cargo fmt --all` một lần cho toàn repo
  (diff cơ học lớn, thuần whitespace, đã verify build + toàn bộ test unit/e2e vẫn pass y hệt sau
  đó). CI (`​.github/workflows/ci.yml`) giờ chạy `cargo fmt --all --check` +
  `cargo clippy -- -D warnings` như gate thật, không còn "chỉ informational".
- **JWT không check `aud`/`iss`** (`metap-peripherals` mint/verify) — **Đã xong 2026-08-16.**
  Thêm hằng số `JWT_ISSUER`/`JWT_AUDIENCE` (`metap-peripherals::auth`), cả hai `Claims` struct
  (mint lẫn verify) thêm field `iss`/`aud`, `Validation::set_audience`/`set_issuer` ở phía verify
  (`metap-http::auth`). Verify qua e2e thật (`cargo test -p metap-http -- --ignored`) + smoke
  `pnpm mint-token` → gọi API thật → 200. Token đã mint trước ngày này (không có `aud`/`iss`,
  gồm cả `CRON_SERVICE_JWT` nếu đã set ở môi trường thật) sẽ cần mint lại.
- **`.claude/settings.local.json` từng bị commit kèm JWT** — **Đã xong một phần 2026-08-16.**
  Thêm `.claude/settings.local.json` vào `.gitignore`. KHÔNG rewrite lịch sử git — không tìm
  thấy dấu vết file này từng được commit trong lịch sử của clone local hiện tại; nếu sự cố có
  thật ở một remote/fork khác, cần tự kiểm tra và quyết định rotate key/rewrite history riêng
  (hành động phá hoại, không tự động hoá).

