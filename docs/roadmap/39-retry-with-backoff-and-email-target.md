## Phase 39: retry-with-backoff per-message + `TargetType::Email` (delivery thật) (2026-08-26)

Tiếp nối 2 việc còn lại từ Phase 37/38 ("chủ dự án xác nhận đáng làm"): retry-with-backoff cho
từng message riêng lẻ (khác backoff-*kết nối* đã làm ở Phase 37), và delivery thật thay vì
`notification-worker` chỉ log stdout không cấu hình được. Cả 2 đã lên plan riêng rồi được yêu cầu
làm luôn (không đợi phiên sau).

### Feature 1: `metap_infra::RetryPolicy` / `ConsumedEvent::retry_or_give_up`

Cơ chế TTL+DLX "delay queue" kinh điển (`rabbitmq:3.13-management-alpine` không có plugin
delayed-message-exchange sẵn): mỗi tier retry là 1 queue `<queue>.retry.<n>` khai
`x-message-ttl`/`x-dead-letter-exchange=""`/`x-dead-letter-routing-key=<queue>` — không consumer
nào gắn vào các queue này, hết TTL thì RabbitMQ tự dead-letter message về lại queue gốc. Số lần
đã retry đọc thẳng từ header AMQP `x-death` (RabbitMQ tự thêm, không cần header riêng).

- `EventBus::subscribe` thêm tham số `retry_policy: Option<&RetryPolicy>` (mặc định `None` — mọi
  call site cũ trước tham số này không đổi hành vi); `RabbitEventBus::subscribe` khai thêm các
  queue tier khi `Some`.
- `ConsumedEvent::retry_count()` (đọc `x-death`) + `retry_or_give_up(&policy)` (còn lượt → publish
  vào tier tương ứng rồi ack bản gốc; hết lượt → `nack(false)` như cũ, vào DLQ cuối).
- Nơi dùng đầu tiên: `cron-scheduler::trigger.rs`'s `finish_dispatch` — lỗi DB tạm thời khi match
  trigger trước đây **dead-letter ngay lập tức**, giờ retry 3 tier (5s/30s/2m) trước khi bỏ.
  `notification-worker`/`cron-scheduler::executor` giữ `None` (lý do: `notify()` không thể fail;
  executor đã có retry riêng ở tầng DB — `max_attempts`/`retry_backoff_seconds` — 1 tầng retry
  message-level nữa là thừa).

**Kiểm chứng sống thật** (không chỉ đọc code): viết `crates/metap-infra/tests/retry_policy_rabbitmq.rs`
(e2e, `#[ignore]`, cần RabbitMQ thật) — publish 1 message, fail lần 1 (`retry_or_give_up`, còn 1
tier) → xác nhận qua `.retry.1` (TTL 800ms) → tự quay lại queue chính, `retry_count()==1` đúng →
fail lần 2 (hết tier) → xác nhận final give-up qua `basic_get` trực tiếp trên `.dlq` (kết nối độc
lập, không qua `EventBus` đang test) — payload đúng bản gốc. Pass.

### Feature 2: `TargetType::Email`

Chốt cùng chủ dự án: không sửa `notification-worker` (log *mọi* transition, không cấu hình được
ai nhận gì) — thêm `TargetType::Email` thứ 4 cạnh `webhook`/`workflow_transition`/
`bulk_query_action`, tái dùng đúng hệ trigger `on_transition`/`on_record_event` (Phase 38) — admin
tự cấu hình "entity X event Y → gửi email cho Z" qua `POST /admin/cron-jobs`, không cần code mới.

- `metap-cron::model::TargetType::Email` — `target_config: { to: string | string[], subject,
  body }`, validate hình dạng lúc chạy (`run_email`), không validate ở admin API tạo job (đúng
  tiền lệ 3 target cũ).
- **Gap vá cùng lúc**: `CronJobDuePayload`/`ClaimedDirectJob` trước đây không mang theo "record
  nào gây trigger" — thêm `trigger_record_id`/`trigger_entity` (optional, `None` cho job
  `schedule` và cho 1 retry qua `claim_due_retries` — trigger context của lần bắn gốc không được
  lưu lại đâu để retry rejoin, ghi rõ trong doc comment `dispatch_claimed`, chưa vá). Thread từ
  `cron-scheduler::trigger.rs` (đọc `recordId` từ payload event, giống `tenantId` đã đọc) →
  `metap_cron::dispatch_on_transition_matches`/`dispatch_on_record_event_matches` (thêm tham số
  `record_id: Uuid`) → `fire_matched_jobs` → `dispatch_claimed` → cả 2 nơi build
  `CronJobDuePayload`. `run_email` tự chèn "Triggered by: {entity} (record {id})" vào cuối body.
- Gửi qua SMTP bằng `lettre` (`AsyncSmtpTransport<Tokio1Executor>`) — có credentials thì
  `relay()` (TLS bắt buộc, cho provider thật); không có thì `builder_dangerous()` (plaintext, cho
  Mailhog local dev không hỗ trợ STARTTLS).
- `metap_infra::AppConfig` thêm `smtp_host`/`smtp_port`(mặc định 1025)/`smtp_user`/
  `smtp_password`/`smtp_from` — tất cả optional, unset thì job `email` fail rõ ràng lúc chạy chứ
  không crash lúc boot.
- `docker-compose.yml` thêm service `mailhog` (opt-in, cùng nhóm với `dragonfly`/`vault` — không
  nằm trong `docker compose up -d postgres rabbitmq` mặc định).
- `crates/metap-http/src/routes/cron.rs`'s `validate_trigger` — message lỗi liệt kê target type
  cập nhật thêm `email` (logic validate không đổi, `TargetType::parse` đã tự nhận `"email"`).

**Kiểm chứng sống đầy đủ qua HTTP + RabbitMQ + SMTP thật** (không dùng thư viện giả lập):
`docker compose up -d mailhog` → start `crm-server`+`outbox-publisher`+`cron-scheduler` thật (env
`SMTP_HOST=localhost`/`SMTP_PORT=1025`/`SMTP_FROM=cron@metap.local` trong `apps/crm-server/.env`)
→ `POST /admin/cron-jobs` tạo job thật (`on_record_event`, entity=`crm.customers`, event=
`created`, target=`email`, `to: sales@metap.local`) → `POST /api/crm.customers` tạo record thật →
log `cron-scheduler` xác nhận `cron job triggered` rồi `cron job executed` (không fail) → gọi
`GET :8025/api/v2/messages` xác nhận **email thật đã đến** Mailhog: đúng `from`/`to`/`subject`,
body có đúng dòng `Triggered by: crm.customers (record <id thật vừa tạo>)`. Dọn sạch: xoá cron job
test + record test, purge queue test rỗng còn sót từ Feature 1's e2e test, dừng container
`mailhog`.

`cargo build/fmt --check/clippy --workspace --all-targets -D warnings` sạch. `cargo test
--workspace` (unit) + e2e (`metap-cron`/`metap-workflow` qua Postgres dev thật, `-- --ignored`)
sạch — không regression cho Phase 37/38.

Diff chưa commit.
