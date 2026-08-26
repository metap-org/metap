## Phase 40: `metap_infra::HandlerRegistry` + fix bug mất `routing_key` khi retry (2026-08-26)

Việc còn lại cuối cùng trong list gốc Phase 37 ("delivery thật, handler registry chung,
retry-backoff per-message" — 2/3 đã xong ở Phase 39): **handler registry chung** — cơ chế để 1
process (`crm-server`/`jira-server`) tự đăng ký nhiều handler tùy biến trên cùng 1 subscription,
thay vì phải tự viết hẳn 1 consume loop mới (như `notification-worker` đã làm) hoặc dựng 1 ops
binary mới (như `cron-scheduler`). `metap-infra` chỉ cung cấp cơ chế (generic, entity-agnostic) —
handler cụ thể do binary sở hữu code viết và đăng ký, giống hệt cách `MetadataRegistry` tách
registry chung khỏi entity đăng ký vào nó.

### `HandlerRegistry` (`crates/metap-infra/src/event_bus.rs`)

- `HandlerRegistry::new().on(pattern, handler).on(pattern2, handler2)...run(queue, retry_policy,
  connect, shutdown)` — builder pattern. `pattern` dùng đúng cú pháp AMQP topic wildcard
  (`*`/`#`) `EventBus::subscribe`'s `routing_key` đã hỗ trợ.
- 1 subscription duy nhất (`routing_key = "#"`, catch-all — vì 1 registry phục vụ N pattern độc
  lập trên 1 queue, RabbitMQ binding không filter hộ được) — match từng handler's pattern trong
  process qua `topic_matches` (hàm mới, tự implement luật AMQP topic matching: `.`-separated,
  `*` = đúng 1 từ, `#` = 0 hoặc nhiều từ — 4 unit test cho các case: exact/`*`/`#`/prefix-only).
  Đây generalize hoá đúng cái `cron-scheduler::trigger`'s `classify_topic` đã hand-roll riêng.
- **1 event có thể khớp nhiều handler** — chạy đồng thời (`futures_util::future::join_all`), chỉ
  ack khi **tất cả** thành công; 1 handler fail → cả event fail (không có ack-một-phần) → retry
  qua `RetryPolicy` nếu có, không thì dead-letter ngay — cùng posture at-least-once với mọi
  consumer khác trong codebase (handler phải an toàn khi chạy lại toàn bộ).
- **Kiểm chứng sống thật qua RabbitMQ** (`crates/metap-infra/tests/handler_registry_rabbitmq.rs`,
  2 test `#[ignore]`): 1 event khớp 2 handler (pattern hẹp + pattern `#` rộng) → cả 2 đều chạy,
  ack đúng 1 lần (không có redelivery thừa); 1 handler fail lần đầu, thành công lần 2 → xác nhận
  event được retry đúng và handler được gọi lại lần 2 sau khi redeliver.

### Bug thật phát hiện lúc build test thứ 2 (đã sửa, ảnh hưởng ngược cả Phase 39)

Test "handler fail rồi thành công" ban đầu **fail** — `attempts` dừng ở 1 dù RabbitMQ xác nhận
(qua management API: `message_stats.deliver: 2, ack: 2`) message đã được deliver + ack **2 lần**.
Root cause: `.retry.1`'s `x-dead-letter-routing-key` được set = tên queue trần (cố ý, để publish
qua default exchange đi thẳng vào đúng 1 queue, không fan-out ra mọi consumer khác đang bind cùng
topic exchange) — nhưng AMQP **ghi đè `routing_key` của message bằng chính giá trị dead-letter
republish này**, nên message redeliver có `delivery.routing_key` = tên queue trần thay vì topic
gốc (`crm.customers.record.created`) → mọi logic dispatch dựa trên `routing_key`
(`HandlerRegistry`'s `topic_matches`, và **`cron-scheduler::trigger`'s `classify_topic` — đã
commit từ Phase 39**) đều fail match trên lần redeliver, event bị ack im lặng thay vì thực sự
retry lại logic gốc.

Test `retry_policy_rabbitmq.rs` (Phase 39) không bắt được vì chỉ check `retry_count()` +
payload, không check `routing_key` của bản redeliver — pass "may mắn" dù bug tồn tại.

**Fix**: thêm header AMQP riêng `x-original-routing-key`, `retry_or_give_up` tự stamp (từ
`self.routing_key`, đã đúng bất kể qua bao nhiêu hop) mỗi lần publish vào 1 tier; `subscribe`'s
`ConsumedEvent` construction đọc header này trước, fallback về `delivery.routing_key` khi không
có (mọi message chưa từng retry — trường hợp phổ biến nhất, không đổi hành vi). Không đổi cơ chế
default-exchange-direct-to-queue của retry tier (vẫn tránh fan-out) — chỉ thêm 1 header side-channel
để mang theo routing_key thật.

Thêm assertion `routing_key` vào cả `retry_policy_rabbitmq.rs` (Phase 39's test, giờ khoá chặt
đúng bug này) và test mới — cả 3 test (2 mới + 1 cũ) pass lại sau fix.

**Tác động thực tế lên Phase 39 đã commit**: `cron-scheduler::trigger.rs`'s retry-with-backoff
(lỗi DB tạm thời khi match trigger) trước fix này **không hoạt động đúng trên redeliver** — event
retry lần 2 sẽ bị `classify_topic` trả `None` → ack im lặng thay vì dispatch lại, nghĩa là lần
trigger đó **mất luôn** ở lần retry đầu tiên thay vì thử lại như thiết kế. Fix ở `metap-infra`
sửa tận gốc, không cần đổi gì ở `cron-scheduler` (đã build/test lại toàn workspace xác nhận).

`cargo build/fmt --check/clippy --workspace --all-targets -D warnings` sạch. `cargo test
--workspace` (74 test suite) sạch.

**Còn lại**: chưa có consumer thật nào dùng `HandlerRegistry` (đúng tinh thần "xây trước khi có
consumer cụ thể" như `metap-storage`/`metap-cache`) — sẵn sàng cho lần đầu `crm-server`/
`jira-server` cần đăng ký handler tùy biến cho 1 event mà không muốn viết consumer mới. 2 gap
khác đã ghi nhận lúc thảo luận (idempotency khi redeliver có side-effect đã chạy, SLA/time-based
transition) vẫn chưa làm.

Diff chưa commit.
