## Phase 3: Permission Engine

**Trạng thái: Đã xong**, được ship thành một initiative 4 phần, đi xa hơn so với "modest
RBAC+ABAC scaffold" ban đầu trong roadmap bằng cách khiến chính role assignment trở nên
dynamic:

1. **Dynamic role assignment** — role sống trong DB theo `(tenantId, userId)`,
   được grant/revoke lúc runtime qua một admin API (`RoleAssignmentService`,
   `src/core/auth/role-assignment-service.ts`) thay vì được bake cứng vào
   JWT; JWT giờ chỉ là một bare identity assertion. `scripts/seed-admin.mjs`
   bootstrap admin đầu tiên bên ngoài API (vốn đã bị gate bởi admin).
2. **Policy storage + bộ đánh giá RBAC/ABAC** — bảng `policies` (theo từng
   tenant) kết hợp một role allow-list với một attribute condition tùy chọn
   (`PolicyCondition`, `src/core/permission/policy-condition.ts`), OR-combine
   giữa nhiều policy khớp nhau, không có deny rule.
3. **Enforcement ở field-level + record-level** — `condition-to-sql.ts`
   dịch các condition scoped theo record thành một mệnh đề `WHERE` của
   Drizzle, wire vào `QueryPlanner.planList`; `PermissionService`/
   `PermissionSnapshot` mask các field-level read và gate các field-level
   write, wire vào mọi call site của `CrudService` (`list`/`create`/
   `update`/`transition`).
4. **`PolicyExplainer` + snapshot cache** — `explain()` tạo ra một trace
   read-only về mọi policy đã được xét và lý do, expose qua simulator
   `POST /admin/policies/explain` (bị gate bởi admin); `PermissionSnapshot`
   gom các policy của một tenant/entity vào một lần fetch DB duy nhất, tái
   sử dụng trong suốt một lệnh gọi `CrudService` (có chủ đích *không* phải
   một cache cross-request/TTL — xem spec của sub-project đó để biết lý do).

Các điểm lệch/gap đã biết, có chủ đích để lại chứ không âm thầm bỏ qua:
- Record-level read enforcement chỉ chạy qua `list()` — chưa có endpoint
  `GET /api/:entity/:id` cho một record đơn để nó bao phủ.

**Review 2026-08-21 (chủ dự án yêu cầu), đã fix:**
- `check_action` đổi từ fail-open sang **deny-by-default cho non-admin** khi entity/action chưa
  có policy nào (admin không đổi, vẫn luôn bypass). Kèm endpoint mới
  `POST /admin/policies/seed-defaults` (bulk-tạo policy cho nhiều action cùng lúc, thay vì gọi
  `create_policy` 5 lần) để đỡ chậm lúc mới onboard role/entity.
- Thêm `EntityAction::Transition`, tách khỏi `Update` — quyền sửa field và quyền chuyển state giờ
  là hai policy riêng.
- **Đã fix (2026-08-21):** thêm `PolicyEffect::Deny` (migration `0014_policy_effect.sql`, cột
  `policies.effect`, mặc định `"allow"`) — deny-overrides-allow, áp dụng ở cả 4 điểm quyết định:
  `check_action` (entity-level), `filterReadableFields`/`writableFields` (field-level),
  `can_perform_record_condition` (record-level, đã fetch record), và `record_policy_where_clause`
  (SQL-generation cho `list()` — build `(allow) AND NOT (deny)` thay vì OR tất cả vào nhau, tránh
  một deny row vô tình *mở rộng* thay vì thu hẹp kết quả). Đã verify sống qua HTTP thật cả 2 lớp:
  entity-level (role bị deny đọc dù có policy allow chung cũng match) và record-level/SQL (user
  có allow-record-policy nhưng bị deny khi `status=blocked` — record đó biến mất khỏi `list()`,
  admin vẫn thấy đủ vì luôn bypass).
- **Đã fix (2026-08-21):** cross-record/hierarchical condition (permission phân cấp kiểu Jira
  project→issue) — attribute path dạng dotted (`"project.ownerId"`) giờ resolve được.
  `metap-permission::policy_condition` tự nó vẫn thuần túy/đồng bộ, không I/O: `evaluate_condition`
  traverse path lồng nhau trên `subject` đã có sẵn; `required_relation_fields`
  (`PermissionSnapshot::required_relation_fields`) chỉ đọc segment đầu tiên của path để biết cần
  fetch quan hệ nào, trả `Vec` rỗng (không tốn gì) khi entity không có condition kiểu này.
  `CrudService::enrich_record_for_actions` (`metap-crud`) là nơi duy nhất có I/O: với action đang
  xét, nếu snapshot báo có relation field cần, fetch đúng 1-hop qua field `FieldKind::Reference`
  (`ref_entity`) rồi merge `data` của record liên quan vào một **bản sao** subject (không đụng
  record gốc — guard/`writable_fields` vẫn cần giá trị id gốc, không phải object đã mở rộng), chỉ
  chạy ở 4 method single-record (`get`/`update`/`transition`/`delete`), không chạy ở `list()`.
  `list()` không hỗ trợ (đúng theo thiết kế đã chốt — cần `QueryPlanner` JOIN, chưa xây): thay vì
  âm thầm sai (dotted path bị coi là một key JSONB literal, không bao giờ khớp — nguy hiểm với
  policy `deny`, vì nghĩa là deny không bao giờ kích hoạt), `metap_query::condition_to_sql` giờ
  reject rõ ràng attribute có dấu chấm với lỗi mô tả rõ lý do. Verify sống qua HTTP thật cả
  `get()` lẫn `update()`: policy `allow` record-level với condition `referredBy.status eq active`
  — record có `referredBy` trỏ tới customer `active` thì đọc/sửa được (200), record không có
  `referredBy` (path resolve về `null`) thì bị từ chối (403).

Đã bugfix từ đó (2026-08-01), cả hai được phát hiện trong lần verify E2E thủ
công của Phase 3 và được xác nhận bằng regression test trong
`src/core/crud/crud-service.test.ts`:
- `recordPolicyWhereClause` (`src/core/query/condition-to-sql.ts`) không có
  admin bypass, nên một record-level read policy không scoped cho admin đã
  làm rỗng nhầm kết quả `list()` của admin. Đã fix bằng cách bypass hoàn
  toàn việc đánh giá policy khi `context.roles` chứa `admin`, khớp với mọi
  entry point quyết định permission khác (`PermissionSnapshot.filterReadableFields`/
  `assertWritableFields`/`canUpdateRecordCondition`).
- `filterReadableFields` chỉ mask blob JSONB `data`, không mask các cột
  top-level `code`/`status` trên `records` vốn mirror các field bên trong đó
  (`src/infra/db/schema.ts`), nên việc field-level masking cho `code`/
  `status` chưa đầy đủ. Đã fix bằng một helper mới
  `CrudService.maskRecordForRead`, cũng null hóa `code`/`status` khi field
  mirror tương ứng (`code`, hoặc `entity.workflow.stateField` cho `status`)
  bị mask khỏi `data`. (Một vấn đề thứ ba, nhỏ hơn, đã được fix sớm hơn
  trong cùng diff: `POST /admin/policies` không validate rằng tổ hợp
  `field`+`action` là hợp lệ — giờ bị reject với 400 qua một schema
  refinement.)

Mục tiêu:

- Triển khai RBAC + ABAC.
- Hỗ trợ permission ở field-level.
- Hỗ trợ permission ở record-level.
- Hỗ trợ policy context.
- Thêm policy simulator.
- Cache permission snapshot của user.

Deliverables:

- `PolicyDefinition`
- `AccessDecision`
- `PolicyExplainer`
- `PermissionSnapshotCache`
- policy test

