# 8. Cross-cutting Concepts

Các pattern và nguyên tắc áp dụng xuyên suốt nhiều building block, không thuộc sở hữu riêng của bất kỳ khối nào.

## Metadata-Driven Development

Field, list view, validation schema, workflow, và index/search hint của mọi entity đều được khai báo một lần duy nhất (`EntityDefinition`) rồi được biên dịch/kiểm tra hợp lệ như một artifact runtime (`MetadataCompiler`), thay vì được xem như config thụ động. Xem [05. Building Block View](05-building-blocks.md).

## Transactional Outbox

Một business write và (các) event nó sinh ra được commit trong cùng một transaction PostgreSQL; một process publisher riêng biệt (`outbox-publisher`) drain và gửi chúng tới RabbitMQ thông qua `EventBus` trait (`metap-infra`). Đây là cơ chế duy nhất để side effect chạm tới RabbitMQ — không có service nào publish trực tiếp. Xem [06. Runtime View](06-runtime.md).

## Multi-Tenancy

Mọi bảng nghiệp vụ đều mang `tenant_id`; mọi lời gọi `QueryPlanner`/`CrudService` đều được scope theo nó (`PermissionService::scoped_tenant`). Không tồn tại đường query xuyên tenant nào trong toàn bộ codebase. `scoped_tenant` nhận vào một `RequestContext` đầy đủ và báo lỗi thay vì âm thầm fallback về một tenant mặc định nếu `tenant_id` từng rỗng — một tenant rỗng tại điểm này chỉ có thể là một bug thật sự ở phía trên (auth extractor luôn suy ra một `tenant_id` thật từ một JWT đã verify trước khi bất kỳ đoạn code query-planning nào chạy), và một giá trị mặc định âm thầm sẽ biến bug đó thành kết quả query sai-nhưng-im-lặng trông giống như xuyên tenant, thay vì một lỗi rõ ràng, ồn ào — xem [09. Architecture Decisions](09-adr.md).

Từ Phase 16 (2026-08-16), `CrudService` không còn mở transaction trực tiếp trên một `PgPool`
dùng chung — mọi transaction tenant-scoped đi qua `metap-control::Router::begin(tenant_id)`
(`crates/metap-control`), thứ tự quyết định *bảng nào thực sự lưu row của tenant này*:
`Schema` (`SET LOCAL search_path`, hiện chỉ chạy `schema_name="public"` trong thực tế — isolation
thật cần data-plane table-per-entity, chưa xây) hay `DedicatedDb` (một `PgPool` riêng cho
tenant đó, cache theo `dsn_secret_ref`, DSN được resolve qua `SecretStore`/`VaultStore`). Một
tenant chưa có row `control.tenants` thì fallback về hành vi tương thích ngược (schema `public`,
status `Active`). Chi tiết building block ở
[05. Building Block View](05-building-blocks.md#control-plane-router-multi-tenancy).

## Permission Enforcement

RBAC (danh sách role được phép) kết hợp với ABAC tùy chọn (điều kiện thuộc tính), được đánh giá phía server, ở ba mức: mức entity (role này có được đụng vào entity này không, action gồm `read`/`create`/`update`/`delete`/`transition` — sửa field và chuyển workflow state là hai action tách biệt), mức field (field nào được đọc/ghi), mức record (row cụ thể nào được đọc/ghi, được dịch thành mệnh đề SQL `WHERE`). **Deny-by-default cho non-admin, deny-overrides-allow** (đổi từ opt-in-restriction ngày 2026-08-21 — xem [09. Architecture Decisions](09-adr.md)): chưa có policy nào cho một `(entity, action)` thì **không ai được phép**; mỗi policy còn mang một `effect` (`allow`/`deny`) — cần ít nhất một `allow` khớp mới được phép, nhưng chỉ cần một `deny` khớp là bị từ chối ngay bất kể có bao nhiêu `allow` cũng khớp (`metap_permission::evaluate_policies`, seam chung cho cả 4 entry point enforcement). `POST /admin/policies/seed-defaults` bulk-tạo policy allow cho một role mới, tránh việc onboard chậm vì phải tạo policy từng action một. Role `admin` luôn bypass toàn bộ bước này. Một điều kiện record-level có thể tham chiếu sang record khác qua dotted attribute path (1-hop, chỉ cho thao tác trên record đơn — không áp dụng `list()`). Role được tra mới từ `user_roles` cho mỗi request, không bao giờ cache trên JWT. Sequence diagram đầy đủ (tạo user → đăng nhập → kiểm tra quyền) ở [06. Runtime View](06-runtime.md)'s mục "Tạo user, đăng nhập, và kiểm tra quyền"; building block ở [05. Building Block View](05-building-blocks.md#permission-service).

## Security Principles

- Route nghiệp vụ mặc định yêu cầu auth.
- Tenant scope là bắt buộc.
- Permission được enforce ở phía server.
- CORS dùng allowlist.
- HTML rich text phải được sanitize trước khi render.
- Secret không bao giờ nằm trong repository.
- Container nên chạy non-root trong production.
- Audit log cho các hành động nhạy cảm phải append-only.

## Performance Principles

- Giới hạn cứng cho page size. (Đã xong.)
- Keyset pagination cho record khối lượng lớn. (Đã xong — xem [05. Building Block View](05-building-blocks.md#query-planner).)
- Background job cho export/print/report. (Hoãn lại, dựa trên trigger — xem [11. Risks and Technical Debt](11-risks.md).)
- Query contract cho từng list view. (Đã xong.)
- Cache snapshot metadata và permission. (Đã xong — `PermissionSnapshot`, theo từng lời gọi, chủ ý không cache theo TTL/cross-request.)
- Index được khai báo sát với metadata. (Đã xong — `EntityField.indexed`/`unique`/`searchMode`, được `IndexReconciler` reconcile.)
- Tách workload reporting khỏi workload OLTP khi cần thiết. (Hoãn lại, dựa trên trigger — cùng mục với trên.)
