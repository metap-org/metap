## Phase 38: `metap-cron` — generic record-event triggers, không chỉ workflow transition (2026-08-25)

Chủ dự án hỏi "handler RabbitMQ phải dynamic và customize được, nhưng chưa biết add logic kiểu
gì vì phụ thuộc nghiệp vụ nhiều". Rà lại thấy **cơ chế này đã tồn tại** — `metap-cron`'s
`on_transition` trigger (Phase 17): admin tạo 1 dòng `cron_jobs` (không cần code) khai trigger
(entity+action nào khớp) và target (`workflow_transition`/`bulk_query_action` gọi ngược API
business app, hoặc `webhook` gọi URL bất kỳ) — logic nghiệp vụ nằm ở phía target, ops binary
(`cron-scheduler`) không biết gì về entity. Giới hạn duy nhất: chỉ bắt được
`<entity>.workflow.transitioned`, chưa bắt `<entity>.record.{created,updated,deleted}` (đã publish
sẵn từ lâu, chỉ chưa ai lắng nghe dynamic). Chốt: mở rộng đúng cơ chế này thay vì xây hệ thống mới.

- **Fix tiên quyết**: `metap_workflow::emit_created`/`emit_updated`/`emit_deleted` **thiếu
  `tenantId` trong payload** (chỉ `emit_transitioned` có) — phát hiện khi thiết kế
  `dispatch_on_record_event_matches` cần tenant để scope query `cron_jobs` an toàn multi-tenant.
  Thêm tham số `tenant_id: Uuid` vào cả 3 hàm + payload, cập nhật 3 call site trong
  `CrudService::create/update/delete` (tenant_id đã có sẵn tại chỗ gọi).
- **`TriggerType::OnRecordEvent`** + `OnRecordEventTriggerConfig{entity, event}` (`event` ∈
  created/updated/deleted) — thêm cạnh `OnTransition` sẵn có, không đổi hành vi dòng `cron_jobs`
  cũ. `metap_cron::dispatch_on_record_event_matches` mirror `dispatch_on_transition_matches`,
  tách phần chung (ghi `cron_job_runs`, `dispatch_claimed`, log) vào `fire_matched_jobs` dùng
  chung cho cả 2 loại trigger.
- **`cron-scheduler`'s trigger-listener mở routing key từ `#.workflow.transitioned` sang `#`**
  (bắt mọi event trên exchange) — tự phân loại theo suffix routing key
  (`.workflow.transitioned`/`.record.created`/`.record.updated`/`.record.deleted`), dispatch đúng
  hàm tương ứng. Routing key không khớp gì (vd `cron.job.due`, dành cho queue của executor) →
  **ack im lặng**, không nack/DLQ — dưới subscription catch-all, nhận topic không liên quan là
  bình thường, không phải lỗi dữ liệu.
- **HTTP admin API** (`crates/metap-http/src/routes/cron.rs`'s `validate_trigger`) thêm nhánh
  validate cho `triggerType: "on_record_event"` — `triggerConfig` phải là `{entity, event}` với
  `event` ∈ created/updated/deleted.
- **Bug thật phát hiện ngoài lề, đã sửa**: `metap-workflow`'s `Cargo.toml` thiếu feature `sqlx
  "macros"` dù crate dùng `#[derive(FromRow)]` (từ Phase 31) — chỉ "chạy được" nhờ feature
  unification tình cờ khi build cả workspace cùng lúc (`cargo test --workspace` không lộ ra),
  lộ rõ khi chạy `cargo test -p metap-workflow -p metap-cron` độc lập. Đã thêm feature còn thiếu.

**Kiểm chứng sống đầy đủ** (không chỉ đọc code suy luận):
- 4 e2e test mới (`crates/metap-cron/tests/cron_store_postgres.rs`): fire đúng khi khớp
  entity+event, không fire khi event không khớp, không fire cho tenant khác,
  `on_transition`/`on_record_event` không cross-fire nhau dù cùng entity — cả 10 test (6 cũ + 4
  mới) pass qua Postgres dev thật.
- **Full round-trip qua HTTP + RabbitMQ thật**: start `crm-server`+`cron-scheduler`+
  `outbox-publisher` thật → `POST /admin/cron-jobs` tạo job `on_record_event` (entity=
  crm.customers, event=created, target=webhook) qua API thật → `POST /api/crm.customers` tạo
  record thật → log `cron-scheduler` xác nhận đúng job_id vừa tạo bị "cron job execution failed"
  với lý do webhook gọi URL giả (`example.invalid`, cố tình) — **đúng nghĩa "trigger đã bắn",
  chỉ target test là giả**, xác nhận toàn chuỗi routing key → classify → dispatch → execute hoạt
  động đúng.
- **Phát hiện phụ lúc verify** (không phải bug, hành vi đúng thiết kế): mở routing key `#` lần
  đầu tiên khiến 154 event `record.created`/`record.deleted` **cũ từ trước khi có fix `tenantId`**
  (rác từ các lần test e2e trước trong phiên này) bị dead-letter đúng như thiết kế (thiếu
  `tenantId` → nack). Đã xác nhận qua RabbitMQ management API (payload thật thiếu `tenantId`), dọn
  sạch DLQ vì là rác dev cũ, không phải dữ liệu cần giữ.

`cargo build/fmt --check/clippy --workspace --all-targets -D warnings` + `cargo test --workspace`
(72 test suite) sạch.

**Còn lại** (đã ghi nhận ở Phase 37, chưa làm): retry-with-backoff cho từng message riêng lẻ
(khác backoff-kết-nối đã làm ở Phase 37).

Diff chưa commit.
