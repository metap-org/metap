## Phase 37: EventBus — reconnect-with-backoff cho phía subscribe (2026-08-25)

Chủ dự án cân nhắc phần "event handler của event bus", cụ thể là phía `subscribe` của RabbitMQ.
Rà `crates/metap-infra/src/event_bus.rs` + 3 consumer thật (`notification-worker`,
`cron-scheduler`'s executor và trigger-listener): nền tảng ack/nack, DLQ tự động cho message hỏng,
prefetch/backpressure đều ổn — nhưng **mất kết nối RabbitMQ làm chết cả process**, chỉ `bail!`
rồi trông chờ process manager restart từ đầu, không có reconnect tự động nào ở tầng
`EventBus`/consume loop. Đây đúng là điểm yếu ở "phần subscribe" chủ dự án nhắc tới — làm trước
3 ý còn lại (delivery thật, handler registry, retry-backoff per-message) vì cả 3 đó đều xây trên
message đã nhận được, còn reconnect là vấn đề của chính cơ chế subscribe.

- **`metap_infra::run_resilient_consumer`** (hàm mới, `event_bus.rs`) — thay hẳn shape cũ
  "subscribe 1 lần, bail khi mất kết nối" ở cả 3 consumer. Nhận `connect: Fn() -> Future<Result
  <B>>` (gọi lại mỗi lần cần (re)connect — reconnect thật cần `Connection` mới, không phải retry
  `subscribe` trên bus đã chết) + `handle: FnMut(ConsumedEvent) -> Future<()>` (logic ack/nack
  riêng của từng consumer, hàm này không tự ack/nack) + `shutdown`. Backoff exponential capped
  30s (1s→2s→4s→8s→16s→30s...). Tự đóng bus cũ trước khi reconnect, tự đóng khi shutdown.
- **3 consumer refactor thành wrapper mỏng gọi hàm trên** thay vì tự viết loop riêng:
  `notification_worker::run`, `cron_scheduler::run_executor`, `cron_scheduler::run_trigger_listener`
  — cả 3 trước đó có shape giống hệt nhau (do cùng "mirror" nhau, đúng như doc comment cũ đã ghi),
  giờ logic reconnect/backoff nằm đúng 1 chỗ thay vì lặp lại 3 lần. Tác dụng phụ tốt:
  `run_executor`/`run_trigger_listener` trước đây **share 1 bus instance** — giờ mỗi cái tự
  connect/reconnect độc lập, 1 consumer mất kết nối không còn làm gián đoạn consumer kia.
- Đổi chữ ký `run()`/`run_executor()`/`run_trigger_listener()`: nhận `connect` closure thay vì
  `&impl EventBus` đã kết nối sẵn — hàm giờ tự quản lý vòng đời bus (connect, close), 3 call site
  (`notification-worker/src/main.rs`, `apps/crm-server/src/main.rs`'s inline mode,
  `cron-scheduler/src/main.rs`) đều cập nhật theo.

**Kiểm chứng sống thật** (không chỉ đọc code suy luận): chạy `notification-worker` binary thật,
`docker restart metap-rabbitmq-1` giữa chừng — log xác nhận đúng chuỗi: mất kết nối lúc
15:56:36 → 4 lần retry với backoff tăng dần (attempt 0/1/2/3, đúng khoảng cách thời gian tăng dần
theo công thức) → RabbitMQ sống lại → tự `connected`/`subscribed`/`reconnected to event bus
attempt=4` lúc 15:56:51, **không cần restart process nào**. `cargo build/fmt --check/clippy
--workspace --all-targets -D warnings` + `cargo test --workspace` (72 test suite) sạch.

**Còn lại** (chủ dự án xác nhận cũng đáng làm, chưa làm trong phase này): delivery thật (email/
SMS/webhook thay vì chỉ log), cơ chế đăng ký handler chung (không hardcode 1 handler/consumer),
retry-with-backoff cho từng message riêng lẻ (khác backoff kết nối vừa làm — cái này là khi
`nack(requeue: true)` một message cụ thể, hiện vẫn "redeliver ngay không backoff không giới hạn"
đúng như doc comment gốc của `subscribe()` đã ghi, chưa đổi).

Diff chưa commit.
