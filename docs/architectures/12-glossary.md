# 12. Glossary

| Term | Meaning |
|---|---|
| **Entity** | Một kiểu đối tượng nghiệp vụ được khai báo một lần dưới dạng `EntityDefinition` (một module Rust, ví dụ `apps/crm-server/src/entities/customer_entity.rs`) — field, list view, workflow. Không có bảng database riêng; được lưu trong bảng `records` chung. |
| **`records` table** | Bảng chung duy nhất mà dữ liệu của mọi entity nằm trong đó — các cột tenant/entity/status/code cùng một cột `data jsonb` cho các field do metadata quyết định. |
| **Tenant** | Một ranh giới cô lập; mọi row nghiệp vụ, mọi query, mọi permission check đều được scope theo `tenant_id`. |
| **`Router`** | `metap-control::Router` — điểm duy nhất mở transaction tenant-scoped, thay cho `CrudService` nhận thẳng `PgPool`. Quyết định route đi đâu dựa trên `TenantStrategy` tra từ `control.tenants` (qua `RegistryCache`, TTL 30s). |
| **`TenantStrategy`** | `Schema { schema_name }` (trial, `SET LOCAL search_path`, thực tế luôn ghim `"public"`) hoặc `DedicatedDb { dsn_secret_ref }` (paid, một `PgPool` riêng resolve DSN qua `SecretStore`) — cột `control.tenants.strategy`. |
| **`SecretStore`** | Trait của `metap-control` để resolve DSN cho tenant `DedicatedDb` — `EnvStore` (đọc biến môi trường) hoặc `VaultStore` (HashiCorp Vault, KV v2, token hoặc AppRole auth kèm auto-renewal). |
| **Outbox pattern** | Ghi một event vào một bảng DB trong cùng transaction với thay đổi nghiệp vụ, rồi drain nó sang RabbitMQ (qua trait `EventBus`) từ một process riêng biệt (`outbox-publisher`) — tránh mất event khi broker bị down. |
| **`EventBus`** | Một trait của `metap-infra` mà event được publish thông qua đó (`RabbitEventBus` là implementation duy nhất) — chính là seam cho phép một broker tương lai (Kafka, NATS, ...) được thay thế vào phía sau `outbox-publisher` mà không cần đụng tới các service enqueue event. |
| **`MetadataCompiler`** | Kiểm tra hợp lệ một `EntityDefinition` tại thời điểm đăng ký (field trùng lặp, tham chiếu treo, workflow sai định dạng) và tính một hash tất định cho hình dạng của nó. |
| **`MetadataDriftService`** | So sánh hash metadata hiện tại của một entity với hash được ghi nhận lần gần nhất ở mỗi lần boot; cảnh báo (không bao giờ crash) khi có drift. |
| **`IndexReconciler`** | Đọc `EntityField.indexed`/`unique`/`searchMode` và tạo các index partial expression / GIN tương ứng trên `records`, một cách idempotent, lúc boot và qua một lần gọi reconcile thủ công. |
| **RBAC** | Role-Based Access Control — một policy cấp quyền dựa trên các role được gán cho người gọi. |
| **ABAC** | Attribute-Based Access Control — quyền cấp bởi một policy còn phụ thuộc thêm vào một điều kiện thuộc tính (`PolicyCondition`), được đánh giá dựa trên request context hoặc bản thân record. |
| **`PermissionSnapshot`** | Một batch policy của một tenant/entity, tính theo từng lời gọi `CrudService`, được load một lần rồi tái sử dụng — không phải một cache xuyên request. |
| **`PolicyExplainer`** | Tạo ra một trace chỉ-đọc của mọi policy được xem xét cho một request giả định, phục vụ debug ở phía admin. |
| **`PolicyEffect`** | `allow` hoặc `deny` — cột `policies.effect`. Deny-overrides-allow: `metap_permission::evaluate_policies` trả `Deny` nếu có ít nhất một policy `deny` khớp, bất kể có bao nhiêu `allow` cũng khớp. |
| **Cross-record condition** | Một điều kiện policy record-level tham chiếu sang một record khác qua dotted attribute path (vd `"referredBy.status"`), resolve 1-hop qua field kiểu `Reference`. Chỉ áp dụng cho thao tác trên một record đơn (`get`/`update`/`transition`/`delete`), không áp dụng `list()`. |
| **Keyset pagination** | Phân trang theo kiểu "cho tôi các row sau giá trị cursor này," không phải theo offset dạng số — vẫn hiệu quả trên bảng lớn và ổn định khi có insert đồng thời, không như `OFFSET`. |
| **Cursor** | Một token mờ (opaque), mã hóa base64, mã hóa giá trị field sort + id + hướng sort của row cuối cùng, dùng để lấy trang tiếp theo. |
| **`searchMode: "fts"`** | Cờ opt-in theo từng field, chuyển cách match filter của field đó từ substring (`ILIKE`) sang full-text search thật của Postgres (`tsvector`/`plainto_tsquery`). |
| **Workflow transition** | Một thay đổi state có gác cổng (một `PolicyCondition`, không phải một hàm), atomic, trên `stateField` của một entity, được ghi log vào `workflow_events` và phát ra như một outbox event. |
| **`validator` (workflow)** | Điều kiện (`PolicyCondition`) chạy trên dữ liệu *sau khi* payload của transition đã được merge và field-validate — khác `guard`, vốn chỉ thấy dữ liệu record trước khi transition. Lỗi trả `422 validator_failed`. |
| **`set_fields` (post-function)** | Một `WorkflowTransition` post-function thuần khai báo (`HashMap<String, PolicyValue>`), chạy sau `validator`, gán field hệ thống (vd `closedBy` từ `context.userId`) — cố ý không cho chạy code tuỳ ý để giữ `metap-workflow` entity-agnostic. |
| **Table-per-entity** | Chiến lược lưu trữ thay thế cho bảng `records` JSONB dùng chung: một entity được cấp bảng vật lý riêng, schema-qualified vào schema `entities` (không bao giờ `public`). `metap-reconciler::reconcile()` (`introspect → diff → plan → execute`) đưa entity lên bảng riêng đó một cách idempotent; `EntityDefinition.table_name` là cái công tắc chọn `"records"` (mặc định) hay bảng riêng — `CrudService`/`QueryPlanner` xử lý cả hai như nhau. `apps/jira-server` (Phase 21) là consumer đầu tiên gọi `reconcile()` thật. |
| **C4 Model** | Ký hiệu diagram cấu trúc bốn tầng của Simon Brown: Context → Container → Component → Code. Được dùng ở đây cho Context ([03](03-context.md)), Container + Component ([05](05-building-blocks.md)). |
| **4+1 View Model** | Mô tả hệ thống theo năm góc nhìn của Philippe Kruchten: Logical, Process, Development, Physical, cộng thêm Scenarios. Được lồng vào các mục arc42 của tài liệu này — Logical + Development vào [05](05-building-blocks.md), Process + Scenarios vào [06](06-runtime.md), Physical vào [07](07-deployment.md). |
| **arc42** | Một khung mẫu tài liệu kiến trúc phần mềm gồm 12 mục (không phải một ký hiệu diagram) — cấu trúc mục mà toàn bộ thư mục `docs/architectures/` này tuân theo. |
| **ADR** | Architecture Decision Record — dự án này ghi quyết định trực tiếp vào [09](09-adr.md) (trước đây qua một quy trình design-spec `docs/superpowers/specs/*.md`, đã ngừng sử dụng ngày 2026-08-07). |
