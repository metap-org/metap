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
- **Permission engine: deny-by-default cho non-admin, deny-overrides-allow, không phải
  opt-in-restriction fail-open.** (Review 2026-08-21, do chủ dự án yêu cầu.) Mô hình cũ ("chưa có
  policy nào = ai cũng được phép") là một khoảng hở fail-open thật — một tenant/entity mới, chưa
  kịp cấu hình policy, mặc định mở toang thay vì đóng. Đổi sang: một `(entity, action)` chưa có
  policy nào cho non-admin thì bị từ chối; kèm `POST /admin/policies/seed-defaults` (bulk-tạo
  policy allow cho nhiều action cùng lúc) làm escape hatch nhanh lúc onboard, để tránh đánh đổi
  "an toàn hơn" lấy "chậm cấu hình". Mỗi policy thêm một cột `effect` (`allow`/`deny`, mặc định
  `allow` — dữ liệu cũ không cần migrate ý nghĩa) — chọn deny-overrides-allow (không phải một
  `PolicyCondition::Not`, vốn không giải được phủ định *xuyên* nhiều policy độc lập, chỉ phủ định
  được điều kiện *trong* một policy) vì đây là ngữ nghĩa quen thuộc kiểu IAM, dễ suy luận khi
  nhiều policy chồng nhau. Kèm tách `EntityAction::Transition` khỏi `Update` — sửa field và chuyển
  workflow state giờ là hai quyền độc lập. Chi tiết: [05. Building Block View](05-building-blocks.md#permission-service), `docs/roadmap.md` Phase 3.
- **Cross-record permission condition: dotted attribute path, resolve 1-hop ở `CrudService`, `metap-permission` giữ nguyên thuần túy/đồng bộ.** (Cùng review 2026-08-21 — "cách tốt nhất cho
  tương lai, performance ưu tiên hàng đầu".) Cân nhắc để `metap-permission` tự làm I/O (fetch
  record liên quan bên trong khi evaluate) nhưng bác bỏ — sẽ phá vỡ tính pure-function của toàn bộ
  crate, thứ mọi call site khác (bao gồm cả bản dịch sang SQL của `metap-query`) đang dựa vào.
  Chọn: `metap-permission` chỉ báo tên field quan hệ cần (`required_relation_fields`, đọc segment
  đầu của path, không I/O); `CrudService` là nơi duy nhất fetch, chỉ 1-hop qua field kiểu
  `Reference`, chỉ khi thật sự cần (rỗng thì không tốn gì), chỉ cho 4 method single-record — không
  áp dụng `list()` vì cần `QueryPlanner` JOIN (chưa xây, không phải mục tiêu của thay đổi này).
  Chi tiết: [05. Building Block View](05-building-blocks.md#permission-service).
- **Vault AppRole auth ưu tiên `renew_self`, chỉ fallback login lại khi renew thất bại.**
  (`VaultStore`, Phase 16 Giai đoạn 4.) Renewal ban đầu luôn login lại bằng AppRole + `secret_id`
  đã lưu — tự vỡ với một role cấu hình `secret_id_num_uses=1` (secret_id một lần dùng, login lại
  lần hai sẽ bị Vault từ chối). Đổi sang thử `vaultrs::token::renew_self` trước (không tốn
  `secret_id`), chỉ login lại từ đầu khi renew thất bại thật (token đã hết hạn cứng, không thể
  renew). Client Vault chỉ được build mới bên ngoài phạm vi giữ lock — lock chỉ giữ trong lúc kiểm
  tra "token có sắp hết hạn không", tránh serialize hoá mọi lần resolve DSN đồng thời của nhiều
  tenant `dedicated_db` qua cùng một round-trip HTTP tới Vault.
- **Tenant delete chỉ detach routing, không tự động xoá dữ liệu vật lý.** (`DELETE
  /platform/tenants/{id}`, Phase 16.) Với `dedicated_db`: không tự `DROP DATABASE`. Với `schema`:
  không tự xoá row nào trong bảng `records`/`users`/... Chỉ set `status: Deleted` (terminal,
  không đảo ngược qua API) và đóng dedicated pool đang cache nếu có — một request tới tenant đó
  sau khi xoá bị từ chối ngay ở `Router::begin` (404 `tenant_not_found`), nhưng dữ liệu vẫn còn
  nguyên trên đĩa để khôi phục thủ công nếu cần. Chọn vì `DROP DATABASE`/xoá hàng loạt qua một lời
  gọi API là hành động không thể hoàn tác — quyết định đó nên là một thao tác vận hành tường minh,
  riêng biệt, không phải side effect ẩn của một endpoint xoá tenant.
- **Enrich `RequestContext` bằng attribute ngoài field cố định: opt-in qua config
  (`AUTH_CONTEXT_ENTITY`), có cache — khác nguyên tắc "role không bao giờ cache".**
  (`docs/features/03-organization-identity.md`, 2026-08-22.) Organization & Identity (phòng
  ban, chức vụ...) không cần bảng lõi mới — vẫn là business entity thường qua low-code, chỉ
  thiếu một chỗ để ABAC đọc được attribute đó (`departmentId` của caller) trong điều kiện
  `fromContext`. Chọn: một biến env đặt tên đúng một entity quy ước có field `userId` trỏ tới
  user hiện tại (`AuthContext` tự tra record đó, generic — không biết `entity_name` là gì, giữ
  đúng ranh giới "không crate nào biết business entity"); `None` (mặc định) là no-op tuyệt đối,
  không query thêm, không đổi hành vi cho mọi deployment hiện tại. Khác với role (`get_roles_for_user`,
  luôn tra mới mỗi request, không bao giờ cache — giữ nguyên) — `context_attributes` **được
  cache** (`metap_http::cache::ContextAttributesCache`, cùng mẫu `metap-control::RegistryCache`:
  `moka::future::Cache`, TTL cố định `AUTH_CONTEXT_CACHE_TTL_SECONDS`, `try_get_with` de-dupe
  concurrent miss) vì đây đọc một record nghiệp vụ thường, không phải role — cache để tránh một
  round-trip DB thêm trên *mọi* request khi bật tính năng. Đánh đổi chấp nhận: một thay đổi
  `departmentId` của user có độ trễ hiệu lực tối đa bằng TTL — bù lại bằng đường invalidate tường
  minh (`POST /admin/users/{userId}/context/invalidate`) để admin ép hiệu lực ngay sau khi sửa.
  Best-effort: lỗi lookup không chặn login, vì đây là enrichment bổ sung, không phải điều kiện
  xác thực.
