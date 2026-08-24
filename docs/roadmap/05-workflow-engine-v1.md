## Phase 5: Workflow Engine V1

**Trạng thái: Đã xong.** Atomic transition, optimistic locking, guard condition (các predicate TypeScript trên `WorkflowTransition`), một audit log `workflow_events` append-only, và outbox side effect được implement qua `WorkflowEngine` + `CrudService.transition`, expose tại `POST /api/:entity/:id/transitions/:action`. "Notification integration" ban đầu được ship dưới dạng một outbox topic publish-only, dạng stub (`<entity>.workflow.transitioned`) không có consumer. 2026-08-09: `EventBus` có thêm phía `subscribe` (`crates/metap-infra/src/event_bus.rs` — bind một durable queue vào một routing key của topic-exchange, ack/nack) và `crates/notification-worker` là consumer thật đầu tiên, log mọi transition. Cố tình để tối giản (chỉ stdout, không email/SMS/webhook) vì chưa có kênh notification thật nào được yêu cầu; nó có thể chạy như một process riêng (`pnpm worker:notification:rs`, mặc định, cùng kiểu với `outbox-publisher`) hoặc inline bên trong `crm-server` qua `NOTIFICATION_WORKER_INLINE=true` cho các deployment single-process — cả hai đều gọi cùng `notification_worker::run`. Delivery semantics, cùng ngày: at-least-once (durable queue, manual ack), một DLQ theo từng queue (`<queue>.dlq`, wire qua `x-dead-letter-exchange`/`x-dead-letter-routing-key` — một message bị nack sẽ rơi vào đó thay vì biến mất, đã verify live trên một broker thật) và `basic_qos` prefetch (20) để backpressure; `notification_worker::run` giờ propagate lỗi (thay vì exit sạch) khi event stream đóng bất ngờ (bus disconnect) để process manager phân biệt được điều đó với một tín hiệu shutdown thật, khớp với contract "propagate and let the process manager restart" của `outbox-publisher`. Cố tình *chưa* build: retry-with-backoff — chưa có call site nào nack với `requeue: true` (không có gì trong `notify()` có thể fail), nên một chuỗi delay-queue/attempt-counter sẽ là hạ tầng suy đoán trước khi có trigger thật; doc comment của `EventBus::subscribe` đánh dấu đây là gap đã biết cho consumer tương lai nào cần bounded retry.

Mục tiêu:

- Atomic transition.
- Optimistic locking.
- Guard condition.
- Append-only workflow event.
- Side effect sau commit qua outbox.
- Notification integration.

Deliverables:

- `WorkflowTransitionService`
- `WorkflowGuard`
- `WorkflowEvent`
- workflow test

