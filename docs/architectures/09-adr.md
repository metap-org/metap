# 9. Architecture Decisions

Ghi lại các quyết định kiến trúc **đang có hiệu lực** — lý do *tại sao*, không phải một nhật
ký thay đổi qua từng phase. Dự án hiện ở giai đoạn thiết kế + thử nghiệm: nội dung dưới đây là
trạng thái mới nhất, quyết định cũ bị thay thế thì bị xóa thẳng chứ không giữ lại làm lịch sử.
Từ v1.0.0 trở đi, thay đổi tiếp theo sẽ được ghi kèm ngày/lý do đổi cụ thể hơn; trước mốc đó,
việc đó là thừa.

- **Backend: Rust (axum + sqlx) + PostgreSQL + RabbitMQ, outbox pattern.** Chọn vì dấu chân hạ
  tầng tối thiểu, tốc độ, và event publishing đáng tin cậy qua transactional outbox. Chi tiết ở
  [02. Architecture Constraints](02-constraints.md).
- **Không có abstraction Repository/StorageProvider.** `sqlx::PgPool` được inject trực tiếp,
  kiểu cụ thể, vào mọi core service — YAGNI có chủ đích, chưa có trigger (chưa cần datastore
  thứ hai, chưa có deployment profile Tiny/SQLite). Nếu trigger đó xảy ra, seam đúng chỗ là bề
  mặt SQL do `QueryPlanner` sinh ra (`jsonb_extract_path_text`, `plainto_tsquery`,
  keyset-pagination `WHERE`), không phải các động từ CRUD theo từng entity.
- **`EventBus` là một trait** (`metap-infra::EventBus`, `RabbitEventBus` implementation duy
  nhất). Swap broker sau này (Kafka/NATS) là thêm một implementation mới, không phải viết lại
  call site.
- **Layering `crates/metap-* -> apps/<consumer>`, một chiều.** Không crate thư viện nào được
  biết business-entity cụ thể — đăng ký entity là việc của binary tiêu thụ (`apps/crm-server`).
- **Expression của index phải khớp chính xác với expression của query.** Postgres khớp
  expression-index theo cú pháp, không theo ngữ nghĩa — `IndexReconciler` build index và
  `QueryPlanner` sinh filter/sort đều thống nhất dùng `jsonb_extract_path_text`.
- **`IndexReconciler` inline SQL literal đã escape** (Postgres DDL không chấp nhận bind
  parameter). An toàn vì literal chỉ đến từ metadata do server tự viết, đã qua
  `MetadataCompiler` validate — không bao giờ từ request input.
- **`PermissionService.scopedTenant` throw khi `tenantId` rỗng**, không fallback về một tenant
  mặc định — trường hợp đó chỉ có thể là bug ở phía trên. Xem
  [08. Cross-cutting Concepts](08-cross-cutting.md#multi-tenancy).
- **Capability SPI (`docs/modular-spi-architecture.md`) là một đích đến có tên gọi, chưa phải
  cam kết xây dựng.** Ngoài `EventBus`, không SPI nào khác (Storage/Scheduler/Identity/Cache/
  Search/WorkflowRuntime) có trigger hiện tại — không xây trước khi có.
