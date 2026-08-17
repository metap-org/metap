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
- **Tenant isolation cho SaaS multi-tenant: schema-per-tenant (trial) / DB-per-tenant (paid),
  không phải Postgres RLS trên một bảng `records` dùng chung.** Chọn vì tách bạch trial/paid rõ
  ràng hơn (teardown trial = `DROP SCHEMA`, backup/PITR/xóa per-client trivial cho paid) và vì
  data-plane cũng đang chuyển sang table-per-entity (xem điểm dưới) — lúc đó RLS trên bảng chung
  không còn là seam đúng chỗ. RLS vẫn có thể bật thêm như defense-in-depth, không phải cơ chế
  chính. Chi tiết: `docs/multi-tenant-platform-design.md` §2.1. Giai đoạn 1 (control-plane
  skeleton: `crates/metap-control`'s `Router` + `control.tenants` registry, `CrudService` đã
  refactor để đi qua `Router::begin(tenant)`) và Giai đoạn 2 (`dev-tools provision-tenant`,
  `SecretStore`/`EnvStore`, `DedicatedDb` strategy hoạt động thật — verify isolation vật lý qua
  HTTP thật) đã triển khai 2026-08-16. `DedicatedDb` (paid) đã có "răng" thật; `schema` (trial)
  vẫn ghim `schema_name='public'`, chưa có isolation thật cho tới khi data plane evolution (§3)
  xong. Giai đoạn 3 (2026-08-17): `POST /platform/tenants` — provisioning giờ có cả HTTP lẫn
  CLI, gate bởi `PlatformAdminContext` (một tenant sentinel `PLATFORM_TENANT_ID` + role
  `"platform_admin"`, không phải bảng/claim mới) chứ không phải `AdminContext` (tenant-scoped).
  Chi tiết: `docs/roadmap.md` Phase 16 Giai đoạn 3.
- **Bảng `records` JSONB dùng chung sẽ được thay bằng table-per-entity khi có tín hiệu scale
  (@ ~10M row/entity), không phải ngay bây giờ.** Giữ nguyên chiến lược hiện tại
  (xem Data Model Strategy, [05. Building Block View](05-building-blocks.md)) cho tới khi trigger
  đó xảy ra; khi xảy ra, dùng một reconciler DDL level-triggered (`reconcile = diff(desired,
  actual) → plan → execute`, tự lành sau crash, không cần rollback vì DDL online không rollback
  được) thay vì migration một-lần. Chi tiết: `docs/multi-tenant-platform-design.md` §3-§5.
- **Không tách microservice cho hướng SaaS multi-tenant.** Modular monolith + Dispatch contract
  sạch (`CrudService`) đã "distributed-ready" mà chưa trả giá phân tán (mất ACID xuyên
  audit/outbox/lock). Tách một mảnh cụ thể khi có tín hiệu cụ thể — cùng tinh thần trigger-based
  của Phase 9 ([04. Solution Strategy](04-strategy.md)), không phải quyết định trả trước. Chi
  tiết: `docs/multi-tenant-platform-design.md` §10.
