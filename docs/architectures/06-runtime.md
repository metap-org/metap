# 6. Runtime View

## Concurrency: hai process độc lập

API Server và Outbox Publisher chỉ kết nối với nhau qua PostgreSQL (transactional outbox) và RabbitMQ — không bao giờ gọi trực tiếp lẫn nhau. Sequence diagram bên dưới mô tả một request `create()` (process API Server) chạy song song với vòng lặp polling của Outbox Publisher (process riêng biệt). (Process View của Kruchten 4+1.)

```mermaid
sequenceDiagram
  participant Client
  participant Route as axum Route
  participant Crud as CrudService
  participant Perm as PermissionService
  participant Flow as Workflow fns
  participant DB as PostgreSQL
  participant OP as Outbox Publisher (separate process)
  participant MQ as RabbitMQ

  Client->>Route: POST /api/crm.customers
  Route->>Crud: create(entity, data, context)
  Crud->>Perm: can_create_entity(context, entity)
  Perm-->>Crud: allowed
  Crud->>Flow: get_initial_status(entity, data)
  Flow-->>Crud: status
  Crud->>DB: BEGIN
  Crud->>DB: INSERT records
  Crud->>DB: INSERT outbox_events (same transaction)
  Crud->>DB: COMMIT
  Crud-->>Route: RecordDto
  Route-->>Client: 201 Created

  loop every ~1s, independent of any request
    OP->>DB: SELECT pending outbox_events FOR UPDATE SKIP LOCKED
    DB-->>OP: rows
    OP->>MQ: publish (via EventBus)
    OP->>DB: UPDATE published_at
  end
```

Nếu RabbitMQ bị down, vòng lặp trên chỉ tiếp tục fail và retry — request `create()` đã commit và trả về xong trước khi vòng lặp đó chạy, nên tính khả dụng của API không bao giờ phụ thuộc vào việc RabbitMQ có đang chạy hay không.

## Tạo user, đăng nhập, và kiểm tra quyền

Ba việc thường bị nhầm là một, nhưng là ba cơ chế tách biệt: **tạo user** (danh tính), **đăng
nhập** (mint JWT), và **kiểm tra quyền** (chạy lại ở server cho *mỗi* request, không đọc từ
JWT). Sequence dưới đây gộp cả ba để thấy rõ chúng nối với nhau ở đâu:

```mermaid
sequenceDiagram
  participant Admin as Admin (UI/curl)
  participant AdminRoute as axum /admin/users
  participant Peripherals as metap_peripherals
  participant DB as PostgreSQL
  participant EndUser as End user
  participant AuthRoute as axum /auth/login
  participant ApiRoute as axum /api/:entity
  participant Perm as PermissionService

  Note over Admin,DB: 1. Tạo user + gán role - admin-driven, dev-tools create-user/seed-admin đi cùng đường này
  Admin->>AdminRoute: POST /admin/users {email, password, roles?}
  AdminRoute->>Peripherals: create_user(pool, tenantId, email, password)
  Peripherals->>DB: INSERT users (password_hash = argon2id)
  loop mỗi role trong roles
    AdminRoute->>Peripherals: assign_role(pool, tenantId, userId, role)
    Peripherals->>DB: INSERT user_roles
  end
  AdminRoute-->>Admin: 201 {userId, roles}

  Note over EndUser,DB: 2. Đăng nhập — JWT chỉ là bằng chứng danh tính, KHÔNG chứa role
  EndUser->>AuthRoute: POST /auth/login {email, password}
  AuthRoute->>Peripherals: verify_credentials(pool, email, password)
  Peripherals->>DB: SELECT users WHERE email
  Note right of Peripherals: email không tồn tại vẫn chạy argon2-verify<br/>với dummy hash — timing giống hệt sai mật khẩu
  Peripherals-->>AuthRoute: user hoặc lỗi invalid_credentials
  AuthRoute->>Peripherals: mint_jwt(userId, tenantId)
  AuthRoute-->>EndUser: 200 {token}

  Note over EndUser,Perm: 3. Mọi request sau đó — role tra mới từ DB, không bao giờ đọc từ JWT
  EndUser->>ApiRoute: GET /api/sales.orders (Authorization: Bearer token)
  ApiRoute->>ApiRoute: AuthContext: verify JWT (RS256) -> userId, tenantId
  ApiRoute->>Peripherals: get_roles_for_user(pool, tenantId, userId)
  Peripherals->>DB: SELECT role FROM user_roles WHERE tenant_id, user_id
  Peripherals-->>ApiRoute: roles[]
  ApiRoute->>Perm: can_read_entity(context{roles}, "sales.orders")
  alt context.is_admin()
    Perm-->>ApiRoute: allowed (bypass, không query policies)
  else không phải admin
    Perm->>DB: SELECT policies WHERE tenant_id, entity, action
    Perm->>Perm: evaluate_policies(rows, context) — mọi policy khớp role gate + condition
    alt có ít nhất một policy effect=deny khớp
      Perm-->>ApiRoute: forbidden — deny luôn thắng, bất kể có policy allow nào cũng khớp
    else có ít nhất một policy effect=allow khớp, không có deny nào khớp
      Perm-->>ApiRoute: allowed
    else không có policy nào khớp (kể cả khi entity/action chưa có policy nào)
      Perm-->>ApiRoute: forbidden — deny-by-default cho non-admin
    end
  end
```

Ghi chú:

- **Mô hình phân quyền là deny-by-default cho non-admin, với deny-overrides-allow** (đổi từ
  "opt-in restriction" ngày 2026-08-21, xem [09. Architecture Decisions](09-adr.md)): một
  `(entity, action)` chưa có policy nào thì **không ai được phép** (trừ `admin`, luôn bypass toàn
  bộ bước này) — ngược hẳn với hành vi cũ. `POST /admin/policies/seed-defaults` là cách nhanh để
  bulk-tạo policy allow cho một role mới trên một entity, tránh phải gọi `create_policy` từng
  action một khi onboard. Mỗi policy còn mang một `effect` (`allow`/`deny`, cột `policies.effect`)
  — nếu có ít nhất một policy `deny` khớp (role gate + condition) thì bị từ chối ngay, bất kể có
  bao nhiêu policy `allow` cũng khớp; ngược lại cần ít nhất một `allow` khớp mới được phép. Hàm
  `metap_permission::evaluate_policies` là nơi duy nhất quyết định thứ tự ưu tiên này — mọi entry
  point (entity-level `check_action`, field-level `filterReadableFields`/`writableFields`,
  record-level `can_perform_record_condition`, và bản dịch sang SQL
  `record_policy_where_clause` cho `list()`) đều đi qua nó. Chi tiết ở
  [08. Cross-cutting Concepts](08-cross-cutting.md#permission-enforcement).
- Có 3 tầng policy, phân biệt bằng cột `field`/`subject` của bảng `policies` (xem ER diagram ở
  [05. Building Block View](05-building-blocks.md#database-design-er-diagram)): **context-level**
  (`field`/`subject` đều rỗng — gác toàn bộ action trên entity), **field-level** (`field` có giá
  trị, `action` là `"read"` để mask lúc đọc hoặc `"write"` để chặn lúc ghi), **record-level**
  (`subject: "record"` — dịch thành mệnh đề SQL `WHERE` trong `QueryPlanner`, lọc row nào hiện ra
  trong `list()`). `action` có 5 giá trị: `read`/`create`/`update`/`delete`/`transition` — sửa
  field và chuyển workflow state là hai action tách biệt (`transition` != `update`, từ
  2026-08-21) nên một role có thể sửa field mà không được transition, hoặc ngược lại.
- Điều kiện của một policy `record`-level có thể tham chiếu sang một record khác qua dotted
  attribute path (vd `"referredBy.status"`, resolve 1-hop qua field kiểu `Reference`) — chỉ áp
  dụng cho thao tác trên một record đơn (`get`/`update`/`transition`/`delete`), **không** áp dụng
  cho `list()` (`metap-query` reject rõ ràng nếu gặp dotted path trong policy dùng cho `list()`,
  vì SQL path chưa hỗ trợ join). Xem [05. Building Block View](05-building-blocks.md#permission-service).
- `users` và `user_roles` là hai bảng tách biệt có chủ đích (xem ghi chú ở ER diagram) — một user
  có thể tồn tại (đăng nhập được) mà chưa có role nào, hoặc có nhiều role cùng lúc.
- **Cách kiểm chứng cả luồng này** — `apps/crm-server/scripts/permission-smoke.sh` chạy qua HTTP
  thật, đủ cả 3 tầng policy + admin route gating (401/403) + `PolicyExplainer`, tự dọn state sau
  mỗi lần chạy. Không phải test suite được commit, chỉ là script lặp lại được cho việc poke thủ
  công — xem `docs/CONTRIBUTING.md`.

## Scenarios

Các scenario dùng để kiểm chứng những building block ở trên, làm cơ sở cho các e2e test chạy trên DB thật của codebase này (`cargo test --workspace -- --ignored`, cần `DATABASE_URL` + một Postgres/RabbitMQ dev đang chạy). (Phần "+1" của Kruchten 4+1 — các scenario dùng để xác thực những view còn lại.)

- **Tạo một record** — `CrudService` → `PermissionService` → workflow fns → outbox `enqueue`, gói gọn trong một transaction PostgreSQL. Sequence: như trên.
- **Update với version đã lỗi thời** — cùng luồng như create, nhưng `WHERE version = $expected_version` của `CrudService::update` khớp 0 dòng, trả về `409 version_conflict` thay vì âm thầm ghi đè lên một write đang diễn ra đồng thời.
- **Workflow transition** — `find_transition` + `run_guard` (một phép đánh giá `PolicyCondition`) gác cổng cho việc đổi state; khi thành công, một dòng `workflow_events` dạng append-only được ghi và một outbox event `<entity>.workflow.transitioned` được enqueue trong cùng transaction với scenario create.
- **List có filter, full-text search, và keyset pagination** — thực thi toàn bộ `plan_list`: filter bị ràng buộc bởi metadata, nhánh `searchMode: "fts"`, mệnh đề `WHERE` của policy ở mức record, và một cursor được kiểm tra khớp với sort đã resolve — tất cả được AND lại thành một query duy nhất, chạy trên các index mà `IndexReconciler` duy trì.
- **Admin cấp một role** — `POST /admin/users/{userId}/roles` (chỉ admin mới gọi được, `crates/metap-http/src/routes/admin.rs`) ghi một dòng `user_roles` qua `metap_peripherals::assign_role`; request tiếp theo của user đó nhận role mới ngay lập tức (role luôn được đọc mới ở mỗi request, không bao giờ cache trong JWT).
