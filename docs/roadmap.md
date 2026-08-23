# Roadmap

Tài liệu này chỉ theo dõi trạng thái ở cấp độ phase. Với một feature nhỏ hơn một phase, xem
`docs/features/`; về ownership/process của team, xem `docs/team-charter.md`, `docs/CONTRIBUTING.md`,
và `docs/agile-process.md`; checklist chi tiết ở mức UI/UX cho frontend, xem
`docs/frontend-checklist.md`.

## Trạng thái hiện tại (cập nhật 2026-08-22)

| Phase | Status |
|---|---|
| 0. Skeleton | Đã xong |
| 1. Production-shaped Platform Kernel | Đã xong |
| 2. Metadata Compiler | Đã xong |
| 3. Permission Engine | Đã xong |
| 4. Query Planner V1 | Đã xong |
| 5. Workflow Engine V1 | Đã xong |
| 6. Frontend Core | Đã xong (chưa verify trên browser) |
| 7. Module Migration Strategy | Đã xong — 4/4 module (crm.customers, sales.orders, inventory.movements, accounting.journal) |
| 8. Hardening | Đang làm — chỉ còn "tích hợp secret manager" (design-only 2026-08-17, chờ chốt target production); load test + backup/restore drill xong 2026-08-17 |
| 9. Multi-Service Evolution | Trigger-based, đã rà soát lại 2026-08-17 — vẫn chưa trigger nào xảy ra, không có việc để làm |
| 10. Monorepo, npm publish | Làm một phần |
| 11. Low-code Platform Backbone Architecture | Phase A + Phase B xong 2026-08-17 (Phase B's "policy editor UI" hoá ra đã có sẵn từ Phase 15); Phase C bắt đầu 2026-08-20 — metadata audit log, migration-impact check, import/export, operational visibility (cross-entity audit feed) xong (2026-08-22); còn lại approval workflow ("nếu cần", chưa có trigger) và schema isolation cấp tenant (chặn bởi quyết định kiến trúc lớn hơn — xem `docs/team-charter.md`'s "Metadata low-code theo từng Tenant") |
| 12. Rust Core Migration | Đã quyết định; Migration Order (bước 1-9) đã xong trong `crates/`; chưa cut over sang production |
| 13. Dynamic Cron Jobs | Backend đã xong; admin UI đã xong (Phase 15) |
| 14. Multi-language (i18n) | UI chrome + locale storage đã xong; metadata-label translation hướng đã chốt (translation-key/override table, 2026-08-22), chưa code — chưa có trigger |
| 15. Shared App Shell (UI kit, real login, permission-aware components) | Đã xong |
| 16. Multi-tenant SaaS Control Plane & Data Plane | Hướng B đã chốt. Giai đoạn 1-3 xong (Router, `provision-tenant`+`DedicatedDb`, HTTP tenant provisioning + platform-superadmin — 2026-08-16 → 2026-08-17); Giai đoạn 4: `VaultStore` (token) xong 2026-08-17, AppRole auth + auto-renewal + role lookup/RBAC qua Router (đóng bug login vỡ cho `dedicated_db`) + delete/deprovision tenant xong 2026-08-20 → 2026-08-21; `schema`/trial vẫn chưa có isolation thật; dynamic Vault creds/data-plane/capabilities/FE onboarding/deployment còn lại |
| 17. Metadata-driven Workflow Engine | Increment 1 (on-transition trigger cho `metap-cron`) xong 2026-08-21; Increment 2 (chuỗi activity, `workflow_runs`) và Increment 3 (`wait_event` durable pause) vẫn approved, chưa code — chờ Increment 1 chạy thật lộ ra nhu cầu cụ thể |
| 18. Organization & Identity — P0 | Done 2026-08-22 — `RequestContext.context_attributes` (opt-in `AUTH_CONTEXT_ENTITY`, cache + invalidate endpoint), entity mẫu `hr.departments`/`hr.employees` qua low-code, org-scoped policy verify sống qua HTTP thật. P1 (`hr.positions`/`hr.locations`, `managerId` self-reference)/P2 (Legal Entity, Approval Authority...) vẫn proposed, chưa có trigger |

## Phase 0: Skeleton

**Trạng thái: Đã xong.**

Scaffold hiện tại:

- Fastify app shell
- Zod config validation
- Drizzle PostgreSQL setup
- RabbitMQ publisher
- outbox table/service
- metadata registry
- generic CRUD service
- query planner boundary
- permission service boundary
- workflow engine boundary
- sample `crm.customers` entity

## Phase 1: Production-shaped Platform Kernel

**Trạng thái: Đã xong.** Auth middleware, `RequestContext` (`tenantId`/`userId`/`roles`/`functionId`), structured error response kèm request/trace id, enforce tenant scope, outbox publisher worker, và service test cho CRUD/query đều đã có đủ. `defaultContext()` đã được thay thế hoàn toàn bằng real JWT-derived context — không còn code nào trong `src/` reference đến nó nữa. Một điểm lệch có chủ đích: không xây riêng class `TransactionManager`/`BaseRepository` — DB transaction được xử lý inline qua `db.client.transaction()` của Drizzle, và cách này đã đủ dùng cho đến nay (YAGNI thay vì abstraction sớm).

Mục tiêu:

- Thêm auth middleware.
- Thêm request context với `tenantId`, `userId`, `roles`, `functionId`.
- Thay thế default context trong `CrudService`.
- Enforce tenant scope ở mọi nơi.
- Thêm structured error response.
- Thêm request id và trace id.
- Thêm service test cho CRUD/query/metadata.
- Thêm outbox publisher worker.
- Thêm DB transaction helper.

Deliverables:

- `AuthService`
- `RequestContext`
- `TransactionManager`
- `OutboxPublisherWorker`
- `BaseRepository`
- migration thật đầu tiên

## Phase 2: Metadata Compiler

**Trạng thái: Đã xong.**

- `MetadataCompiler.validate` — validate lúc startup cho từng entity: duplicate field names, dangling listView field/filter/defaultSort reference, enum field không có `enumValues`, workflow shape sai định dạng, duplicate transition. Chạy bên trong `MetadataRegistry.register()`, nên một entity module lỗi sẽ fail ngay lúc boot, không đợi đến request đầu tiên.
- `MetadataRegistry.validateReferences()` — kiểm tra cross-entity rằng mọi field kiểu `reference` có `refEntity` trỏ đến một entity đã đăng ký; chạy một lần sau khi mọi entity đã được đăng ký (tách ra khỏi `container.ts` — xem ghi chú về entity-registration bên dưới).
- `MetadataCompiler.hash` — SHA-256 xác định (deterministic) trên một serialization đã sắp xếp canonical của shape entity (loại trừ các hàm `guard` của workflow transition, vì chúng không thể biểu diễn được và đã bị strip khi truyền qua wire). Được expose dưới dạng `version` trên `EntitySummary` (`GET /metadata/entities`) và trên type `EntitySummary` phía frontend.
- Bảng `metadata_versions` (migration `0005_condemned_cerise.sql`) + `MetadataDriftService` — so sánh hash hiện tại của mỗi entity với hash đã ghi nhận lần trước lúc boot, và cảnh báo (không bao giờ crash) khi có drift, theo cùng tinh thần graceful-degradation của `HealthService`. Được wire vào container dưới tên `container.metadataDrift`, gọi từ `buildApp`.
- OpenAPI generator (`openapi-generator.ts`) — expose tại `GET /metadata/openapi.json`, chỉ build từ projection an toàn `EntitySummary`.

Cũng đã fix trong lần này: `createContainer` (`src/core/container.ts`) trước đây import trực tiếp `customerEntity` và đăng ký nó inline — tức một file `core` với tay vào `modules`, điều mà layering (`modules -> metadata definitions`, không theo chiều ngược lại) không cho phép. Entity registration giờ là mối quan tâm ở tầng application: `createContainer` trả về một `MetadataRegistry` rỗng, và `registerEntities()` (`src/modules/registry.ts`) — nơi duy nhất biết danh sách entity của deployment — đăng ký chúng rồi gọi `validateReferences()` sau đó. Các caller (`buildApp`, outbox worker, test) gọi `registerEntities(container.metadata)` ngay sau `createContainer(config)`.

Mục tiêu:

- Validate entity definition lúc startup.
- Compile field definition thành:
  - validation schema
  - list view contract
  - OpenAPI schema
  - frontend metadata
  - index recommendation
- Thêm metadata version/hash.
- Thêm schema compatibility check.

Deliverables:

- `MetadataCompiler`
- `MetadataValidationError`
- OpenAPI được generate
- endpoint frontend metadata được generate

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

## Phase 4: Query Planner V1

**Trạng thái: Đã xong**, được ship thành 3 sub-project, theo thứ tự sau:

1. **Chiến lược index cho hot field** — `EntityField.indexed`/`unique`
   (trước đây được khai báo nhưng chưa được đọc) giờ dẫn động
   `IndexReconciler` (`src/core/metadata/index-reconciler.ts`): partial
   expression index theo từng entity trên `records`, được reconcile tự động
   lúc boot (`CREATE INDEX CONCURRENTLY IF NOT EXISTS`, best-effort, không
   bao giờ chặn startup) và qua script thủ công `pnpm index:reconcile`. Đã
   bắt và fix một bug thật trong lúc implement: expression được index phải
   là `jsonb_extract_path_text(data, field)`, khớp byte-for-byte với chính
   expression filter/sort của `QueryPlanner` — một index được build trên
   dạng `data->>field` (tương đương về mặt ngữ nghĩa) sẽ âm thầm không bao
   giờ được planner của Postgres chọn.
2. **Chiến lược full-text search** — `EntityField.searchMode: "fts"` mới,
   opt-in (mặc định `"substring"`, tức hành vi ILIKE hiện tại không đổi),
   match qua `to_tsvector('simple', ...) @@ plainto_tsquery('simple', ...)`,
   được backing bởi một GIN index (loại index thứ ba của `IndexReconciler`,
   cùng kỷ luật khớp expression như trên).
3. **Keyset pagination** — cursor base64 mờ (opaque) (`src/core/query/cursor.ts`)
   được validate theo sort *đã resolve* (sau fallback); `QueryPlanner` build
   điều kiện `WHERE` của keyset dưới dạng OR hai mệnh đề tường minh (không
   phải một so sánh row-value đơn) vì tiebreaker `orderBy` hiện tại
   (`id ASC`) không đảo chiều theo hướng của field chính. `CrudService.list`
   thực thi với một lookahead `limit + 1` để tạo ra
   `page.nextCursor: string | null`; một cursor sai sort, hoặc malformed, sẽ
   trả về `400 invalid_cursor` sạch sẽ, không bao giờ là 500.

**Report query boundary — deferred, trigger-based** (chưa build), theo cùng
phong cách của Phase 9 chứ không phải ba item còn lại của phase này: chưa có
gap cụ thể nào thúc đẩy nó — chưa có UI/consumer reporting-analytics nào tồn
tại, và hệ thống hiện chỉ có đúng một entity (`crm.customers`). Xây dựng
`ReportService`/report-specific query path ngay bây giờ sẽ là hạ tầng cho
một workload chưa tồn tại, mâu thuẫn với chính triết lý tiến hóa
trigger-based của dự án (xem Phase 9, và mục Data Model Strategy của
`docs/architectures/05-building-blocks.md`: "none of it should be built
ahead of its trigger"). Trigger: xuất hiện nhu cầu export/aggregation cụ thể
(một UI hoặc consumer thật sự yêu cầu), hoặc một query trên OLTP path bị làm
chậm đáng kể bởi các access pattern kiểu report.

Mục tiêu ban đầu, để tham khảo:

- Hỗ trợ filter định nghĩa bằng metadata. (Phase 1/đã có sẵn.)
- Hỗ trợ safe sort field. (Phase 1/đã có sẵn.)
- Thêm keyset pagination. (Đã xong, sub-project 3 ở trên.)
- Thêm chiến lược full-text search. (Đã xong, sub-project 2 ở trên.)
- Thêm chiến lược generated column/index cho hot JSONB field. (Đã xong, sub-project 1 ở trên.)
- Thêm report query boundary. (Deferred, xem ở trên.)

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

## Phase 6: Frontend Core

**Trạng thái: Đã xong.** React + TypeScript app shell, TanStack Query API client (`packages/platform-react/src/api`), metadata client, `GeneratedList` (kèm cursor-based infinite-scroll pagination và row windowing bằng `@tanstack/react-virtual`), và `FieldRenderer` (cả hai nửa — `FieldValue`/`fieldKindConfig` cho read, `FieldInput` cho write) đều đã xong. `GeneratedForm` đã xong. `WorkflowActionBar` đã xong. Permission-aware UI state đã xong — `CrudService.get()` giờ trả về `capabilities` chủ động (writable field, `canUpdate` ở record-level, kết quả guard thật cho từng transition) mà `GeneratedForm`/`WorkflowActionBar`/`FieldValue` dùng để disable/đánh dấu trước những gì sẽ fail, trước khi user thử. Điều hướng danh sách và delete được thêm vào như một gap-fix follow-up sau khi verify thủ công phát hiện `GeneratedList` không có cách nào thực sự đến được route create của `GeneratedForm` hay `RecordDetail`, và delete thì chưa tồn tại ở đâu cả: `GeneratedList` giờ có nút "New" và một cột action View/Delete theo từng dòng, `RecordDetail` có nút Delete, và backend có thêm hỗ trợ soft-delete (`EntityAction` mở rộng thêm `"delete"`, `PermissionService.canDeleteEntity`, `CrudService.delete()`, `DELETE /api/:entity/:id`, `WorkflowEngine.emitDeleted`). Tất cả những cái này đã pass typecheck/lint/bộ test backend và đã được commit; vẫn chưa được verify trên browser trong sandbox này (không có headless Chromium chạy được — thiếu system library, không có `sudo`, không có phương án cache thay thế). Frontend giờ nằm trong `packages/platform-react` + `apps/crm-fe` (đổi tên từ `web/`) như một phần của việc restructure monorepo ngày 2026-08-02 — xem mục "Frontend Platform Package" của [Architecture](docs/architectures/04-strategy.md). Sự phụ thuộc còn lại của `packages/platform-react` vào `react-router-dom` (`ApiErrorMessage`/`GeneratedList`/`RecordDetail` gọi trực tiếp `Link`/`useNavigate`) cũng đã được fix: một `NavigationAdapter` được inject qua React Context thay thế cả 3 chỗ import trực tiếp, và `apps/crm-fe` cung cấp implementation thật duy nhất. Đã pass typecheck/build/lint/toàn bộ bộ test backend; chưa verify trên browser vì cùng lý do sandbox như trên.

Mục tiêu:

- React + TypeScript app shell.
- TanStack Query API client.
- Generated list renderer.
- Generated form renderer.
- Workflow action UI.
- Permission-aware UI state.
- Table virtualization.

Deliverables:

- `metadata-client`
- `api-client`
- `GeneratedList`
- `GeneratedForm`
- `WorkflowActionBar`
- `FieldRenderer`

## Phase 7: Module Migration Strategy

**Trạng thái: Đã xong (2026-08-10).** Mục tiêu: chứng minh pattern metadata-driven generalize
được qua nhiều module khác nhau (field kind khác, workflow shape khác, list view khác), không
chỉ đúng cho `crm.customers`. Cả 4 module đăng ký cùng process trong `apps/crm-server` — tách
thành binary/service riêng là trigger của Phase 9, không phải Phase 7.

Mục tiêu:

- ~~Port một module master-data đơn giản~~ — **Đã xong**: `crm.customers`
  (`apps/crm-server/src/entities/customer_entity.rs`), có từ bản port Rust.
- ~~Port một module transaction~~ — **Đã xong (2026-08-10)**: `sales.orders`
  (`apps/crm-server/src/entities/sales_order_entity.rs`) — field kind mới (`Reference` tới
  `crm.customers`, `Money`, `Date`), workflow 4 state (draft/confirmed/shipped/cancelled). Chi
  tiết + tiêu chí chấp nhận đã verify live ở `docs/features/demo/01-sales-order-entity.md`.
- ~~Port một module nặng về workflow~~ — **Đã xong (2026-08-10)**: `inventory.movements`
  (`apps/crm-server/src/entities/inventory_movement_entity.rs`) — 6 state, nhánh rẽ approve/reject, và
  một transition (`reverse`) đi ra khỏi state không phải initial; guard trên field `Number`.
  Chi tiết + tiêu chí chấp nhận đã verify live ở
  `docs/features/demo/02-inventory-movement-entity.md`.
- ~~Port một flow report/export~~ — **Đã xong (2026-08-10)**: `accounting.journal`
  (`apps/crm-server/src/entities/journal_entry_entity.rs`) — 2 list view trên cùng entity (`default`,
  `ledger`) chứng minh "report" là một list view khai báo qua metadata, không phải backend
  mới (nền tảng chưa có đường query report/analytics riêng, xem `11-risks.md` — cố tình chưa
  xây); guard đầu tiên dùng `PolicyCondition::Any`. Chi tiết + tiêu chí chấp nhận đã verify
  live ở `docs/features/demo/03-journal-entry-entity.md`.

**Kết luận Phase 7:** pattern metadata-driven (field kind, workflow — kể cả nhánh rẽ và
transition ngược, list view kép, guard đơn/`Any`) generalize tốt qua 4 entity khác nhau mà
không cần đổi gì ở `crates/metap-*`. Không phát sinh nhu cầu cross-module workflow thật trong
lúc làm — củng cố (chưa phải xác nhận dứt khoát) hướng "chưa có trigger" đã ghi ở
`docs/team-charter.md` cho ý tưởng workflow hai chế độ.

## Phase 8: Hardening

**Trạng thái: Đang làm** — bắt đầu 2026-08-09. Bản port HTTP layer ban đầu đã có chủ đích
deferred toàn bộ gap phía Rust của phase này (header tương đương helmet, rate limiting,
requestId/traceId) ra khỏi phạm vi của nó;
gap đó là thứ được đóng lại đầu tiên, tiếp theo là các mục tiêu hạ tầng Docker/CI bên dưới.

Mục tiêu:

- **Tích hợp secret manager** — **Vault impl xong 2026-08-17** (`metap-control::VaultStore`, xem
  Phase 16 Giai đoạn 4), đúng theo hướng thiết kế đã ghi trước đó ở
  `docs/architectures/07-deployment.md`'s "Secret manager — hướng thiết kế":
  `metap-control::SecretStore` trait (xây cho Phase 16's `DedicatedDb`) chỉ cần thêm một impl
  mới của cùng trait, không phải thiết kế lại. Chỉ mới bao phủ `dsn_secret_ref` của
  `DedicatedDb` (Vault token auth, static KV v2 — không phải AppRole, không phải dynamic
  database-credentials engine, cả hai vẫn deferred tới khi có trigger production thật).
  `AppConfig` (đọc `DATABASE_URL`/`RABBITMQ_URL`/JWT key path từ env) là phạm vi rộng hơn còn
  chưa qua abstraction nào, cần mở rộng riêng — config hiện tại vẫn là file `.env` (phù hợp cho
  dev, không phải tư thế production). Vẫn chưa có production deployment topology nào được chốt
  (cloud secret manager của provider nào, nếu không self-host Vault) — quyết định đó vẫn thuộc
  về lúc chọn hạ tầng production thật, không chặn phần đã làm ở trên.
- ~~CORS allowlist theo environment~~ — **Đã xong**, có trước khi phase này được track:
  `CORS_ORIGINS` (`crates/metap-infra/src/config.rs`) là một env var theo từng environment,
  phân tách bằng dấu phẩy, chỉ mặc định rỗng (permissive `CorsLayer::new()`) khi không được
  set — xem doc comment của `metap_http::build_router` để biết ràng buộc `allow_credentials` +
  explicit-origin-list mà nó enforce.
- ~~Security header tương đương helmet~~ — **Đã xong (2026-08-09)**:
  `crates/metap-http/src/security_headers.rs`, áp dụng toàn cục trong `build_router` (bao phủ
  cả static SPA fallback của `apps/crm-server`, không chỉ `/api`/`/metadata`) —
  Content-Security-Policy (mặc định dựa trên `'self'` của helmet, an toàn cho một SPA
  same-origin), X-Frame-Options, X-Content-Type-Options, Referrer-Policy,
  Strict-Transport-Security, Cross-Origin-Opener/Resource-Policy, và phần còn lại của bộ mặc
  định của helmet.
- CSP — xem "Security header tương đương helmet" ở trên; gộp vào đó thay vì track riêng, vì
  axum không có crate tương đương helmet để configure một CSP directive.
- HTML sanitizer / File scanning hook — Chưa áp dụng được: đây là một API chỉ dùng JSON,
  không render HTML và không có endpoint upload file. Xem lại nếu một trong hai được thêm vào.
- ~~Rate limiting~~ (không phải mục tiêu gốc của Phase 8, thêm vào từ gap riêng của Rust ở
  trên) — **Đã xong (2026-08-09)**: `tower_governor`, key theo peer IP, ~300 req/phút (một
  xấp xỉ token-bucket của fixed-window mặc định cũ của `@fastify/rate-limit` — xem doc comment
  của `build_router`), trả 429 với cùng shape error-body `too_many_requests` như mọi error
  response khác. Cần binary phục vụ dùng
  `into_make_service_with_connect_info::<SocketAddr>()` — cả `apps/crm-server/src/main.rs`
  và e2e test của `metap-http` đều dùng.
- ~~Lan truyền requestId/traceId~~ (gap còn lại riêng của Rust) — **Đã xong (2026-08-09)**:
  `crates/metap-http/src/request_context.rs`, response header `x-request-id`/`x-trace-id`
  trên mọi request, `x-trace-id` được echo lại khi caller gửi một id hợp lệ, và cả hai id
  được inject vào mọi JSON error body 4xx/5xx một cách tập trung (không phải luồn qua ~30
  call site riêng lẻ của `service_error_response`/`internal_error_response`).
- ~~Docker image non-root~~ — **Đã xong (2026-08-09)**: `apps/crm-server/Dockerfile` —
  Dockerfile đầu tiên trong repo, đặt cạnh example app mà nó đóng gói thay vì ở repo root
  (cùng lý do như `keys/`/`.env` riêng của `apps/crm-server`: đây là Dockerfile riêng của
  example app này, không phải "cái" Dockerfile của repo — một downstream project tự build
  binary tương đương của riêng nó và tự viết Dockerfile tương tự cho nó, giống như tự viết
  `main.rs` riêng thay vì import cái này). Build context vẫn là repo root
  (`docker build -f apps/crm-server/Dockerfile .`) vì cả Cargo workspace lẫn pnpm workspace
  đều sống ở đó. Multi-stage (`node:24-slim` để build static cho `apps/crm-fe`,
  `rust:1-slim-bookworm` cho `crm-server --release`, `debian:bookworm-slim` làm runtime),
  không bake secret nào vào image (đường dẫn DB/RabbitMQ/JWT key đều được đọc từ environment
  lúc container start, giống convention `.env` local — bản thân JWT key được mount vào, không
  copy vào image), chạy dưới một user non-root cố định `metap` (uid/gid 10001). Đã verify
  bằng cách thực sự build image và chạy nó với một dev Postgres/RabbitMQ đang sống
  (`docker run --entrypoint id` xác nhận `uid=10001(metap)`, `curl /health` trả về 200 kèm
  đầy đủ mọi hardening header).
- ~~CI checks~~ — **Đã xong (2026-08-09)**: `.github/workflows/ci.yml`, ba job — `rust`
  (build + unit test + clippy, không cần DB), `rust-e2e` (service container Postgres/RabbitMQ
  mirror lại credential của `docker-compose.yml`, `db-migrate` trên một DB mới tinh, rồi chạy
  toàn bộ e2e suite `--ignored`), `frontend` (typecheck/lint/format:check/test). Đã verify
  bằng cách thực sự chạy cùng chuỗi đó local trên các container Postgres/RabbitMQ dùng một
  lần (migration trên DB mới + toàn bộ e2e suite pass) thay vì chỉ tin vào file YAML.
  `clippy -D warnings`/`fmt --check` đã strict thật từ 2026-08-16 (xem bullet "Clippy chưa gate"
  bên dưới) — dòng này ban đầu (2026-08-09) nói ngược lại, đã lỗi thời, sửa lại 2026-08-22. Vẫn
  còn thiếu: chưa enforce như một merge gate thật (chưa configure branch protection trên GitHub —
  một cấu hình repo, không phải code).
- ~~Structured logging / observability~~ (không phải mục tiêu gốc của Phase 8 — thêm vào
  2026-08-09 sau khi một audit phát hiện các crate core gần như không có logging:
  `metap-crud`, `metap-permission`, `metap-query`, `metap-workflow` không có gì cả, và nơi
  duy nhất có log — 500 handler của `metap-http` — thậm chí còn không mang theo
  `requestId`/`traceId` mà response body đã có sẵn, nên một id do client report không thể
  grep khớp với log server) — **Đã xong (2026-08-09)**: `tracing` + `tracing-subscriber`
  được wire qua `metap_infra::init_tracing()` (một init dùng chung, được gọi đầu tiên bởi mọi
  binary — `crm-server`, `outbox-publisher`, `notification-worker`, `db-migrate` — đọc
  `RUST_LOG`, mặc định `info`; `dev-tools` cố tình bị loại trừ, stdout của nó là CLI output —
  một token vừa mint, một usage message — không phải log stream).
  `crates/metap-http/src/request_id.rs` (mới, middleware ngoài cùng) sinh cặp
  request/trace id một lần vào request extension; `tower_http::trace::TraceLayer` (cũng mới,
  bọc quanh mọi layer khác) build một span cho mỗi request mang theo cả hai id cùng
  method/path/status/latency, nên **bất kỳ** `tracing` event nào được log ở downstream — một
  lần từ chối permission trong `metap-permission`, một lỗi validation trong `metap-crud`, một
  filter bị reject trong `metap-query` — đều tự động được correlate với cùng id mà client
  nhìn thấy, không cần luồn id qua chữ ký hàm của bất kỳ crate nào trong số đó.
  `request_context.rs` giờ đọc cùng các id đó từ extension thay vì tự sinh id riêng. Đã
  instrument các điểm quyết định trước đây im lặng: allow/deny permission
  (`metap-permission`), filter/sort field bị reject/bỏ qua và cursor không hợp lệ
  (`metap-query`), và trong `metap-crud::CrudService` — entity/record không tìm thấy, lỗi
  validation (kèm tên field vi phạm), version conflict, và toàn bộ chuỗi transition-rejection
  (không có workflow, không có transition định nghĩa, guard fail) cộng với log INFO-level cho
  các lần create/update/transition/delete thành công. Cố tình *chưa* làm: chưa có JSON/OTLP
  exporter (chỉ log ra stderr dạng plain text — chưa có aggregator nào để gửi tới, cùng gap
  với "chưa document production deployment topology" của các mục tiêu Docker/CI); xem lại khi
  có. Đã verify live trên một Postgres/RabbitMQ/crm-server thật (không chỉ `cargo build`): hit
  `/health`, một route chưa auth, một entity không tồn tại, và một `create` payload rỗng — xác
  nhận dòng access-log và các log quyết định của `metap-crud`/`metap-permission` đều mang
  cùng `request_id`/`trace_id` và nằm lồng trong cùng một span.
- **load test cho list/query/export** — **Đã xong 2026-08-17.** Không có endpoint export riêng
  (report/export vẫn là một `listView` thứ hai trên cùng `GET /api/:entity`, xem Phase 4 ở
  trên) nên load test nhắm vào chính path list/query đó:
  `apps/crm-server/scripts/load-test.sh` (script thủ công, cùng kiểu `smoke.sh`, không dùng
  binary ngoài như k6/hey — chỉ `curl` + `xargs -P`). Chạy thật với 200 row seed +
  3 kịch bản × 250 request/kịch bản (limit=50; filter+sort `status=active&sort=-createdAt`;
  keyset pagination trang 2), concurrency 20, nhắm vào `crm-server` debug build + dev Postgres
  local: **p50 12-50ms, p95 66-118ms, p99 79-137ms, 0 lỗi** trên cả 3 kịch bản. Phát hiện đáng
  chú ý trong lúc build script: rate limiter Phase 8 (`tower_governor`, burst 300 @ 5/giây, key
  theo peer IP) dùng chung một token bucket cho *mọi* route — chạy seed + scenario liên tiếp từ
  cùng một IP (như một script thủ công trên một máy) sẽ tự làm cạn bucket của chính nó và gây
  429 hàng loạt, không phải lỗi ở query path. Script tự đợi bucket refill đầy (~65s) trước mỗi
  kịch bản để số đo là latency thật của query, không lẫn hiệu ứng rate-limit; ghi lại rõ trong
  comment của script cho lần chạy sau. Debug build (không phải `--release`), một máy dev — số
  liệu này là baseline tương đối, không phải benchmark production.
- **backup/restore drill** — **Đã xong 2026-08-17.** `apps/crm-server/scripts/backup-restore-drill.sh`
  — `pg_dump -Fc` dev Postgres (qua `docker compose exec postgres`) thật, `pg_restore` vào một
  database tạm trên cùng container, rồi diff row-count chính xác (`count(*)`, không dùng
  `pg_stat_user_tables.n_live_tup` — đó chỉ là ước lượng theo ANALYZE, không đáng tin cho một
  diff ngay-sau-restore) trên toàn bộ bảng ở cả 2 schema (`public` + `control`) giữa DB gốc và
  DB restore. Chạy thật, verify khớp tuyệt đối trên cả 13 bảng (bao gồm `control.tenants` từ
  Phase 16). Không phải pipeline backup production (không upload off-site, không retention
  policy, không lịch chạy định kỳ) — chưa có target triển khai production nào để wire vào,
  cùng gap với mục secret manager bên dưới; đây là drill xác nhận cơ chế `pg_dump`/`pg_restore`
  hoạt động đúng trên schema thật của repo, chạy tay khi cần.
- **TS `strict` tắt cả 2 tsconfig** (`apps/crm-fe`, `packages/platform-react`) — **Đã xong
  2026-08-16.** Bật `"strict": true` + `noUncheckedIndexedAccess` ở cả 3 tsconfig
  (`apps/crm-fe/tsconfig.app.json`, `tsconfig.node.json`, `packages/platform-react/tsconfig.json`).
  `tsc -b --force`/`tsc --noEmit` sạch ngay, không phát sinh lỗi type nào cần sửa.
- **`opt-level = "z"` cho server backend** (`Cargo.toml`) — **Đã xong 2026-08-16.** Đổi
  `[profile.release]` sang `opt-level = 3`.
- **Clippy chưa gate, thiếu `rustfmt.toml`** — **Đã xong 2026-08-16.** Thêm
  `[workspace.lints.clippy]` (`Cargo.toml` gốc), commit `rustfmt.toml` (`max_width = 120` — khớp
  độ rộng dòng thực tế của codebase, không dùng mặc định 100 để tránh diff cơ học quá lớn không
  cần thiết), dọn 5 warning clippy có sẵn (redundant guard, derivable impl, 2×result_large_err,
  1 warning mới tự phát sinh ở `metap-control`), chạy `cargo fmt --all` một lần cho toàn repo
  (diff cơ học lớn, thuần whitespace, đã verify build + toàn bộ test unit/e2e vẫn pass y hệt sau
  đó). CI (`​.github/workflows/ci.yml`) giờ chạy `cargo fmt --all --check` +
  `cargo clippy -- -D warnings` như gate thật, không còn "chỉ informational".
- **JWT không check `aud`/`iss`** (`metap-peripherals` mint/verify) — **Đã xong 2026-08-16.**
  Thêm hằng số `JWT_ISSUER`/`JWT_AUDIENCE` (`metap-peripherals::auth`), cả hai `Claims` struct
  (mint lẫn verify) thêm field `iss`/`aud`, `Validation::set_audience`/`set_issuer` ở phía verify
  (`metap-http::auth`). Verify qua e2e thật (`cargo test -p metap-http -- --ignored`) + smoke
  `pnpm mint-token` → gọi API thật → 200. Token đã mint trước ngày này (không có `aud`/`iss`,
  gồm cả `CRON_SERVICE_JWT` nếu đã set ở môi trường thật) sẽ cần mint lại.
- **`.claude/settings.local.json` từng bị commit kèm JWT** — **Đã xong một phần 2026-08-16.**
  Thêm `.claude/settings.local.json` vào `.gitignore`. KHÔNG rewrite lịch sử git — không tìm
  thấy dấu vết file này từng được commit trong lịch sử của clone local hiện tại; nếu sự cố có
  thật ở một remote/fork khác, cần tự kiểm tra và quyết định rotate key/rewrite history riêng
  (hành động phá hoại, không tự động hoá).

## Phase 9: Multi-Service Evolution

Khác với Phase 1-8, phase này là trigger-based, không tuần tự — nó bắt đầu khi điều kiện trigger của nó xảy ra, không phải khi Phase 8 xong. Xem mục "Future Evolution: Multi-Service Split" của `docs/architectures/04-strategy.md` để biết toàn bộ lý do.

**Bản thân cấu trúc repo/package đã xong, trước cả khi trigger xảy ra.** Việc restructure monorepo ngày 2026-08-02 đã kéo việc split pnpm-workspace lên sớm hơn, bằng một lựa chọn tường minh, không phải vì điều kiện trigger đã xảy ra: `packages/core` và `apps/crm` đã là các package riêng biệt (`apps/crm` là một Fastify app mỏng, import `packages/core` qua `workspace:*`), khớp với hình dạng mà trigger này mô tả. Điều *chưa* xảy ra là phần thực chất của trigger — vẫn chỉ có đúng một module thật (`crm`); chưa module thứ hai nào cần được xây như một deployable unit riêng. Hãy coi việc split cấu trúc này là hạ tầng sẵn có, không phải bằng chứng rằng trigger multi-service nền tảng đã xảy ra.

Các trigger và transition mà mỗi cái mở khóa:

- **Một module thứ hai (CRM, sales, inventory, accounting, ...) thực sự cần được xây như một deployable unit riêng** → đã xong về mặt cấu trúc (xem ở trên); phần việc còn lại là xây chính module thứ hai đó — xem Phase 7.
- **Một màn hình frontend đơn cần aggregate dữ liệu từ ≥2 service** → xây một GraphQL gateway làm BFF phía trước các REST service. Chưa trigger — vẫn chỉ có một module, nên chưa có nhu cầu aggregate cross-service nào tồn tại. Readiness-note (2026-08-12, không phải implementation): đã kiểm tra và ghi lại ở `docs/architectures/04-strategy.md`'s "Sự sẵn sàng của backend cho GraphQL BFF tương lai" — `CrudService` đã đủ protocol-agnostic để một GraphQL resolver in-process gọi thẳng vào, không cần module `dispatch` trung gian; phần "local-vs-remote dispatch" (BFF gọi in-process hay remote tuỳ entity) cố tình chưa thiết kế, chờ đến khi trigger split-deploy bên dưới xảy ra thật.
- **Việc split repo/package ở trên đã thực sự xảy ra** → đánh giá gRPC cho các lệnh gọi service-to-service ở nơi overhead của REST đáng kể. Việc split đã xong về mặt cấu trúc, nhưng với chỉ một process đang chạy thì chưa có lệnh gọi service-to-service thật nào để tối ưu — đánh giá việc này khi một module thứ hai thực sự được deploy độc lập (Phase 7), không phải chỉ dựa trên việc split cấu trúc.

Cho đến khi một trigger xảy ra, transition của nó không được build. Việc duy nhất cần làm ngay bây giờ, trước mọi trigger: giữ tên mọi entity module mới theo domain-namespace (`<module>.<entity>`, ví dụ `crm.customers`) và không bao giờ để `QueryPlanner`/`CrudService` join dữ liệu giữa các entity khác nhau trong SQL — cả hai điều này đã đúng ngay hôm nay và không tốn gì để giữ đúng.

**Rà soát lại 2026-08-17:** kiểm tra lại cả 3 trigger ở trên — vẫn chỉ có một module thật
(`crm.customers` + 3 module demo của Phase 7, cùng chạy chung một process `crm-server`), chưa
màn hình FE nào cần aggregate ≥2 service, và việc split repo/package vẫn chỉ là hạ tầng sẵn có
chứ chưa có lệnh gọi service-to-service thật nào tồn tại. Không có gì để "làm nốt" ở phase này
— cố tình build trước trigger sẽ đi ngược triết lý trigger-based của chính phase này.

## Tiêu chí thành công

Metap được coi là thành công nếu một developer có thể:

1. Định nghĩa một ERP entity với field và workflow.
2. Có được metadata CRUD/list/form mà không cần viết boilerplate.
3. Thêm policy mà không cần đụng vào HTTP route.
4. Có được event đáng tin cậy mà không cần publish RabbitMQ thủ công.
5. Tune một list view chậm thông qua metadata query/index.
6. Giữ việc enforce security ở phía server.

## Phase 10: Monorepo, npm publish

**Trạng thái: Làm một phần.** Split repo thành một pnpm workspace và publish `packages/core` (tương ứng `src/core` + `src/infra` dùng chung hiện tại) như một npm package cài được, để một downstream project có thể depend vào core của Metap thay vì fork nó. Overlap với trigger repo/package-split của Phase 9, nhưng được scope riêng ở đây vì "publish một npm package cho người khác cài" là một cam kết riêng, bổ sung (semver, changelog, public API surface) ngoài việc chỉ split repo cho mục đích multi-service nội bộ.

Mục tiêu:

- ~~Split thành một pnpm workspace (`packages/core`, `apps/*`)~~ — **Đã xong** 2026-08-02 (`packages/core`, `packages/platform-react`, `apps/crm`, `apps/crm-fe`). Được kéo lên sớm trước cả trigger của Phase 9, bằng một lựa chọn tường minh — xem Phase 9 ở trên. Đã bị Rust migration (Phase 12) thay thế — `packages/core` không còn tồn tại, phần tương đương bên Rust là `crates/metap-*`.
- Định nghĩa và ổn định public API surface của `packages/core`. — Chưa bắt đầu cho việc publish thật lên crates.io/npm (cả `packages/platform-react` lẫn mọi crate `metap-*` đều vẫn chưa được publish, chưa có consumer ngoài workspace nào tồn tại). Có tiến triển một phần trên *downstream-consumption ergonomics* mà mục tiêu này thực sự nhắm tới, hoàn thành 2026-08-09 trước cả việc publish thật: `crates/metap` (một facade crate re-export các sub-crate `metap-*` — một dependency, một `use metap::prelude::*` thay vì phải nhớ item nào nằm ở sub-crate nào) và `templates/metap-app` (một template `cargo generate` được wire để depend vào `metap` qua git, vì việc publish lên crates.io chưa xảy ra) — cả hai đều được dogfood bằng cách migrate chính `apps/crm-server` sang dùng facade và bằng cách thực sự generate + compile + chạy một project từ template đó với một Postgres thật. Bản thân việc publish (một git dependency vẫn có nghĩa là "clone và compile từ source" cho mỗi consumer) chưa bắt đầu.
- Thiết lập versioning/changelog và một pipeline publish npm (và, giờ đây, một pipeline crates.io cho `metap`/`metap-*`).

## Phase 11: Low-code Platform Backbone Architecture

**Trạng thái: Phase A (Metadata Control Plane Foundation) và Phase B (Builder UI và Safe Runtime Rules) đã xong (Phase B: 2026-08-12 → 2026-08-17), Phase C chưa bắt đầu.** Định nghĩa kiến trúc cho việc dùng Metap làm backbone của một low-code platform (ERP, CRM, và hơn thế), không chỉ là một ERP core đơn mục đích.

Mục tiêu:

- ~~Định nghĩa cụ thể "low-code" nghĩa là gì với Metap~~ — **Đã xong, ở mức định hướng**, bởi `docs/vision.md` và `docs/low-code-platform-v1.md` (cả hai đều 2026-08-02): ai configure mọi thứ (operator, qua một metadata control plane, không sửa source code cho đường đi chuẩn), cái gì user-editable lúc runtime (metadata: entity/field/list view/workflow/policy) so với lúc deploy-time (chính execution engine — các service của `packages/core` vẫn là code, chỉ có metadata *input* của chúng mới được persist).
- Reconcile điều này với thiết kế metadata-driven đã có sẵn (Phase 0-6) và việc split multi-service (Phase 9-10). — Mục "Ràng buộc kiến trúc" của `docs/low-code-platform-v1.md` đã nêu rõ nguyên tắc reconcile (tiến hóa authoring model, giữ nguyên execution engine); Phase A dưới đây tuân theo đúng nguyên tắc đó (execution engine — `CrudService`/`QueryPlanner`/`PermissionService` — không đổi, chỉ nguồn metadata đổi).
- ~~Tạo ra một design spec trước khi viết bất kỳ implementation plan nào~~ — **Đã xong** bởi `docs/low-code-metadata-storage-design.md` (viết cho TS, trước quyết định Rust).
- **Phase A: Metadata Control Plane Foundation — Đã xong (2026-08-11), retarget từ spec TS sang Rust, cả 4 sub-project theo đúng thứ tự đã định:**
  1. *Persisted metadata storage (draft/publish/rollback)* — crate mới `crates/metap-lowcode` (`LowCodeEntityDefinition` tái dùng `EntityField`/`EntityListView`/`compiler::validate` của `metap-metadata` thay vì một Zod schema song song), migration `crates/migrations/0010_low_code_entities.sql` (`low_code_entity_drafts`/`low_code_entity_versions`, đúng data model đã chốt trong spec). 13 test e2e trên Postgres thật (`crates/metap-lowcode/tests/store.rs`).
  2. *Runtime loader* — **đi xa hơn spec gốc**: không chỉ load lúc boot mà hot-reload thật lúc runtime, không cần restart. `MetadataRegistry::merge_with` (mới, `crates/metap-metadata/src/registry.rs`) gộp một base code-authored với các entity DB-authored; `AppState`/`CrudService` giữ registry trong một `ArcSwap` (`arc-swap` crate) thay vì `Arc<MetadataRegistry>` bất biến — mỗi request chụp một snapshot nhất quán, publish/rollback swap registry mới vào ngay khi request đó trả về.
  3. *Publish validation pipeline* — gộp vào `metap_lowcode::publish`/`rollback` luôn (không tách riêng): chặn tên trùng với entity code-authored (điểm bị hoãn tường minh trong spec gốc vì thiếu registry access — giờ có, vì `metap-lowcode` phụ thuộc `metap-metadata`), và tái dùng `MetadataRegistry::validate_references()` có sẵn để bắt `refEntity` treo.
  4. *Metadata admin API* — ban đầu ở `crates/metap-http/src/routes/lowcode.rs`, sau đó tách hẳn ra crate riêng `crates/metap-lowcode-http` (`docs/team-charter.md`-style boundary: `metap-http` không còn phụ thuộc `metap-lowcode`/`metap-lowcode-http` nữa — `build_router` nhận thêm tham số `extra_routes: Router<AppState>` chung, `apps/crm-server` tự merge `metap::lowcode_http::router()` vào, một downstream project không muốn low-code có thể truyền `Router::new()` thay vào và không bao giờ compile crate đó vào). `/admin/lowcode/entities/{name}/{draft,publish,rollback,published,versions}` + `GET /admin/lowcode/entities`, gate bởi `AdminContext`, global (không theo tenant, đúng quyết định đã chốt).

  Builder UI (`packages/platform-react/src/admin/LowCodeEntitiesAdminPage.tsx`) cũng đã có: field builder (name/label/kind dropdown/required/searchable/sortable/enum values/ref entity) + list-view builder (fields shown/filterable fields/default sort/max limit), wire vào `apps/crm-fe` tại `/admin/lowcode`.

  **Enable/disable toggle cho entity đã publish** (2026-08-11, thêm sau khi Phase A "xong" ở trên — cùng đợt với fix code-review bên dưới): migration `crates/migrations/0011_low_code_entity_enabled.sql` thêm cột `enabled` vào `low_code_entity_drafts`; `metap_lowcode::set_enabled`/`list_enabled_published` (bản lọc, dùng cho boot + mọi lần rebuild registry — `list_all_published` không lọc vẫn giữ nguyên, dùng riêng cho `GET /admin/lowcode/entities` để phân biệt "chưa publish" với "đã publish nhưng đang tắt"); route `PATCH /admin/lowcode/entities/{name}` (`crates/metap-lowcode-http`) toggle rồi rebuild + swap registry ngay, không cần restart — cùng cơ chế hot-reload publish/rollback đã có. Publish/rollback một entity đang tắt **không** tự động bật lại nó (có test regression riêng). Field "Entity name" trong Builder UI tự khoá sau khi entity đã có draft/publish (không có thao tác rename, sửa tên lúc đó sẽ vô tình tạo entity mới tách biệt).

  `/code-review` (2026-08-11) tìm ra 10 finding trên nhánh này, tất cả đã fix: race condition thật khi 2 publish/rollback cùng entity chạy đồng thời (fix bằng `pg_advisory_xact_lock` trong transaction, có test regression), `CrudService::list()` load registry 2 lần có thể tách snapshot giữa chừng, boot chỉ `validate_references()` trên registry code-authored chứ không phải registry đã merge, publish/rollback build lại registry 2 lần dư thừa (giờ tái dùng registry đã validate thay vì query lại DB — cũng dẹp luôn nguy cơ "commit DB xong mà reload registry fail"), cộng vài bug FE (race khi click Edit nhanh 2 entity liên tiếp, enum value chứa dấu phẩy bị vỡ khi save lại — chuyển sang `TagsInput` không còn join/split chuỗi, đổi tên field không dọn tham chiếu cũ khỏi list view), và `templates/metap-app` bị gãy compile do lệch API (đã fix + verify bằng `cargo check` độc lập, vì template không nằm trong workspace nên CI không tự bắt được).

  Đã verify live trên Postgres/RabbitMQ thật, không chỉ `cargo test`: draft → publish → `GET /metadata/entities/lowcode.demo` trả về đúng ngay trên **cùng một server đang chạy, không restart** → `POST /api/lowcode.demo` tạo record thật qua đúng `CrudService`/`QueryPlanner` như một entity code-authored → publish v2 → rollback về v1 tạo version 3 (append-only, đúng thiết kế) → registry phản ánh lại ngay → thử publish tên `crm.customers` bị chặn `409 lowcode_name_reserved`. Chưa làm lúc đó (ngoài scope Phase A, thuộc Phase B): `crm.customers` vẫn code-authored (đúng quyết định — DB-authored chỉ chứng minh trên entity mới).

**Phase B: Builder UI và Safe Runtime Rules — bắt đầu 2026-08-12.** Increment đầu tiên: field builder (`LowCodeEntitiesAdminPage.tsx`) trước đây chỉ expose `name`/`label`/`kind`/`required`/`searchable`/`sortable`/`enumValues`/`refEntity` — 4 flag đã có sẵn trên `EntityField` phía backend (`crates/metap-metadata/src/entity.rs`, đã đi qua OpenAPI/generated-types từ trước) nhưng chưa có chỗ set trên UI: `indexed`, `unique`, `searchMode` (select "substring"/"fts", disable khi `searchable` tắt), `refDisplayField` (chỉ hiện khi `kind === "reference"`, cạnh `refEntity`). Đã pass `typecheck`/`lint`/`format` cho `packages/platform-react` + `apps/crm-fe`; chưa verify trên browser (theo policy hiện tại, xem CLAUDE.md — code xong, hand off cho user tự kiểm tra).

Rà lại phần core khi expose `unique` lên UI phát hiện một gap thật (đã fix cùng đợt, 2026-08-12): `indexed`/`searchMode` (`fts`) đã được `IndexReconciler` (`crates/metap-peripherals/src/index_reconciler.rs`) reconcile đầy đủ cho cả boot lẫn hot-reload publish/rollback/toggle của low-code entity (qua `apply_registry`, `crates/metap-lowcode-http/src/lib.rs`) — không có gap. Nhưng `unique: true` trước đây chỉ được enforce dưới dạng Postgres unique index, không có gì bắt exception ở tầng `CrudService`: một write đụng constraint sẽ rớt xuống `?` và biến thành lỗi 500 thô, thay vì một validation error sạch như mọi lỗi khác của `create`/`update`. Đã fix trong `crates/metap-crud/src/crud_service.rs` — cả `create` và `update` giờ bắt `sqlx::Error::Database` có `is_unique_violation()`, map tên constraint (`uniq_records_<entity>_<field>`) ngược lại thành tên field, trả về `409 unique_violation` kèm `field_errors` đúng shape mà `GeneratedForm` (`packages/platform-react`) đã tự render cho mọi lỗi có `fieldErrors`, không cần đổi gì ở `metap-http`/frontend. Có test e2e regression mới `unique_field_violation_is_a_clean_409_not_a_500` (`crates/metap-crud/tests/crud_service_postgres.rs`), verify trên Postgres thật: duplicate create -> 409, duplicate update -> 409 và record không bị bump version.

**Declarative workflow guard model cho DB-authored entity — Đã xong (2026-08-17).** Gap thật
hoá ra nhỏ hơn cách spec gốc mô tả: bản port Rust đã biến `WorkflowTransition.guard` thành dữ
liệu khai báo (`PolicyCondition`) từ trước, nhưng field đó vẫn bị `#[serde(skip)]` chỉ để khớp
hành vi loại trừ của bản TS cũ (khi guard còn là một hàm) — đây chính là thứ chặn DB-authored
entity có workflow, không phải thiếu một rule engine mới. Đã bỏ `#[serde(skip)]`
(`crates/metap-metadata/src/entity.rs`, giờ `#[serde(default, skip_serializing_if =
"Option::is_none")]`), thêm `workflow: Option<EntityWorkflow>` vào
`crates/metap-lowcode/src/definition.rs`'s `LowCodeEntityDefinition` (không cần migration DB —
cột `definition` đã là `jsonb`), cập nhật `MetadataCompiler::hash`'s doc comment (giờ hash gồm
cả guard, trước đây thì không — một guard-only edit giờ bump đúng `version`), và thêm `guard: {}`
(loose, không hand-model lại `PolicyCondition`'s recursive untagged enum) vào
`workflow_transition_json_schema()` (`crates/metap-metadata/src/openapi.rs`) để không lệch với
generated types. `metap_workflow::run_guard` vốn đã entity-agnostic từ trước — không cần đổi gì
ở `metap-crud`/`metap-workflow`. Test mới: `hash_changes_when_only_a_transition_guard_changes`
(`metap-metadata`), `workflow_with_guard_round_trips_through_draft_and_publish`
(`metap-lowcode`, e2e Postgres thật). Verify live qua HTTP thật (không chỉ `cargo test`): draft
một entity `lowcode.wftest2` với workflow + guard `email neq ""` → publish → `GET
/metadata/entities/lowcode.wftest2` phản ánh guard ngay, không restart → tạo record thiếu
email → transition `activate` bị `409 guard_failed` → tạo record đủ email → transition thành
công — đúng path `CrudService::transition` mà một entity code-authored dùng.

**Workflow editor UI — Đã xong (2026-08-17).** `LowCodeEntitiesAdminPage.tsx`
(`packages/platform-react`) có thêm `WorkflowBuilder` — cùng pattern với `FieldBuilder`/
`ListViewBuilder` đã có (memoized row editor, `useCallback`-stable update/remove/add). "Không
có workflow" được biểu diễn bằng `stateField` rỗng (giống cách `ListViewRow.sortField` rỗng =
"không default sort"), không cần một boolean toggle riêng: state field (`Select`, chọn từ field
đã khai báo + `createdAt`/`updatedAt`), initial state, terminal states (`TagsInput`), và danh
sách transition (action/from/to/label + guard). Guard được edit như JSON thô trong một
`Textarea` — cùng pattern `PoliciesAdminPage` đã dùng cho `PolicyCondition` (recursive untagged
enum `Attribute`/`All`/`Any`, không hand-model một structured editor cho nó), không phải quyết
định mới. Validate phía client trước khi save: initial state bắt buộc khi đã cấu hình workflow,
mọi transition cần đủ action/from/to/label, guard JSON không hợp lệ chặn save với thông báo rõ
ràng thay vì gửi lên server để 400. `adminApi.ts`'s `LowCodeEntityDefinition`/`saveDraft` gained
`workflow?: unknown` (loose, cùng lý do field/listView đã loose). i18n: cả `en`/`vi` dưới
`admin.lowcode.workflow.*`. `pnpm typecheck`/`lint`/`format:check` (root, cả `platform-react`
lẫn `crm-fe`) sạch; chưa verify trên browser (theo policy hiện tại, xem CLAUDE.md).

**Policy editor UI — hoá ra đã xong từ trước, do Phase 15 (2026-08-10).** Rà lại thấy
`PoliciesAdminPage` (`packages/platform-react/src/admin/`) đã tồn tại — create/list/delete
policy, editor raw-JSON cho `PolicyCondition`, cùng pattern đang dùng lại cho guard editor ở
trên. Đây là staleness của chính tài liệu Phase 11 (bullet này được viết trước khi Phase 15
build cái đó, chưa ai reconcile lại), không phải việc thật còn thiếu — bỏ khỏi danh sách "còn
lại" của Phase B.

**Publish preview/validation report — Đã xong (2026-08-17).** `metap-lowcode/src/store.rs`
tách phần validate-only của `publish` (get draft → `validate_shape` → name-reservation check →
`validate_references`) thành `validate_for_publish`, dùng chung bởi cả `publish` lẫn hàm mới
`preview_publish` — hàm sau chạy đúng các check đó nhưng không `insert_version`/swap live
registry, chỉ đọc thêm `MAX(version_number)` (không lock, chỉ mang tính tham khảo — số thật vẫn
do `insert_version`'s advisory-lock transaction quyết định lúc publish thật) để báo
`wouldBeVersion`. Route mới `POST /admin/lowcode/entities/{name}/publish/preview`
(`metap-lowcode-http`). FE: nút "Preview" cạnh "Publish" trong `LowCodeEntitiesAdminPage.tsx`,
kết quả hiện trong một alert riêng màu xanh (không lẫn với `rowError` màu đỏ) — thành công báo
"hợp lệ, sẽ tạo version N", lỗi hiện đúng message `PublishError` mà `publish` thật cũng trả về.
Test mới: `preview_publish_reports_the_would_be_version_without_writing_anything` (xác nhận
không có row version nào được ghi, published version không đổi),
`preview_publish_surfaces_the_same_errors_publish_would` (`metap-lowcode`, e2e Postgres thật).
Verify live qua HTTP thật: no draft → 404; draft hợp lệ → `{valid:true, wouldBeVersion:1}`,
`GET .../versions` vẫn rỗng; publish thật → v1; preview lại → `wouldBeVersion:2`, published
version vẫn là 1; draft với dangling reference → 422 `lowcode_validation_failed`.

Phase 11 Phase B coi như xong tất cả các mục đã liệt kê.

**Phase C: Củng cố Platform cho việc sử dụng Low-code thực tế — bắt đầu 2026-08-20.** Deliverable
đầu tiên, "audit log cho metadata" (`docs/low-code-platform-v1.md`'s Phase C), **đã xong**:
migration mới `crates/migrations/0013_low_code_metadata_audit.sql`
(`low_code_metadata_audit_events` — entity_name/action/actor_user_id/actor_tenant_id/
version_number/restored_from_version/occurred_at, index theo `(entity_name, occurred_at)`), module
mới `crates/metap-lowcode::audit` (`record`/`list_for_entity`). **Cố tình không nằm trong cùng
transaction** với `store.rs`'s `save_draft`/`set_enabled`/`publish`/`rollback` — 4 hàm đó đã có
~40 call site trực tiếp trong `crates/metap-lowcode/tests/store.rs`, nên thay đổi signature để
luồn thêm actor qua sẽ là một diff cơ học lớn cho một tính năng governance/observability;
`crates/metap-lowcode-http`'s handler (vốn đã giữ `RequestContext` từ `AdminContext` — trước đây
bind rồi bỏ qua dưới tên `_context`) gọi `audit::record` ngay sau khi `store.rs`'s call thành công.
Best-effort: một lỗi ghi audit event bị log rồi nuốt (`tracing::warn!`), không bao giờ biến một
draft/publish/rollback/enable-toggle đang thành công thành lỗi HTTP — chấp nhận mất một audit
event nếu crash đúng khoảnh khắc giữa 2 write, phù hợp cho "khả năng quan sát vận hành", không
phải một tamper-evident compliance log. Route mới `GET
/admin/lowcode/entities/{name}/audit` (gate `AdminContext`, giống mọi route khác của crate này).
Verify live qua HTTP thật (không chỉ build/clippy sạch): draft → publish → disable một entity mới
→ `GET .../audit` trả đúng 3 event `draft_saved`/`published`/`disabled`, đúng thứ tự mới nhất
trước, đúng `actorUserId`/`actorTenantId`/`versionNumber`.

**Migration-impact check — Đã xong (2026-08-21).** `metap_lowcode::impact::diff_impact` so draft
sắp publish với bản đang live, cảnh báo (không chặn publish) 4 loại thay đổi phá huỷ: field bị
xoá, đổi `kind`, field mới `required`, field mới `unique`, và enum value bị xoá. Trả về trong
`POST /admin/lowcode/entities/{name}/publish/preview`'s response (`impact: [...]`). Verify live
qua HTTP thật: publish 1 entity, sửa draft xoá field + thu hẹp enum + thêm required/unique, gọi
preview → đúng cả 4 cảnh báo.

**Import/export định nghĩa app — Đã xong (2026-08-22).** `metap_lowcode::export_entities(pool,
names: Option<&[String]>)` — đọc từ `list_all_published` (không phải
`list_enabled_published`: một snapshot export phải là bản sao chính xác của những gì đã publish,
gồm cả entity đang disable, không phải view đã lọc theo registry đang phục vụ runtime), lọc
theo tên nếu có truyền `names`. Route mới: `GET /admin/lowcode/export` (query `?entities=a,b,c`
lọc theo tên, bỏ trống export tất cả; tên không tồn tại trả về trong `notFound`, không lỗi cả
request) và `POST /admin/lowcode/import` (body cùng shape `entities: [{name, definition}]` với
output của export — ghép được thẳng output export vào import). Vì định nghĩa entity DB-authored
là global theo deployment, không theo tenant (xem đầu file `metap-lowcode-http/src/lib.rs`),
đây là cơ chế mang một "app" (tập entity definition) giữa các **deployment** khác nhau (dev →
staging → prod, hoặc chia sẻ một app mẫu), không phải copy cross-tenant trong cùng một
deployment. Import chỉ ghi mỗi entity như một **draft** (`save_draft`, cùng validate shape một
operator tự tay author qua admin UI nhận được) — không bao giờ tự publish; publish vẫn là một
bước riêng, tường minh, qua đúng `POST .../publish` đã có (đầy đủ check name-reservation/
cross-reference/migration-impact), import cố tình không bypass bất kỳ check nào trong số đó.
Best-effort như target `bulk_query_action` của cron: một entity lỗi trong batch không làm hỏng
cả batch, response báo `imported`/`failed` riêng từng entity. Verify live qua HTTP thật: publish
2 entity mẫu → export không filter thấy cả 2 → export có filter + 1 tên không tồn tại → đúng
`notFound` → import 1 entity hợp lệ (tên mới) + 1 entity sai tên (mismatch `definition.name`) →
đúng `imported: [...]`/`failed: [{name, error}]` tách biệt → entity import xong nằm ở draft,
`GET .../published` vẫn 404 (chưa tự publish). 3 test e2e mới
(`crates/metap-lowcode/tests/store.rs`: export không filter, export có filter, export gồm cả
entity disabled).

**Operational visibility (2026-08-22): cross-entity audit feed.** Audit log trước đó
(`audit::list_for_entity`, `GET /admin/lowcode/entities/{name}/audit`) chỉ xem được theo từng
entity — một operator muốn biết "gần đây ai publish gì trên toàn platform" phải query từng entity
một. Thêm `audit::list_recent(pool, limit)` (`crates/metap-lowcode/src/audit.rs`) — cùng bảng
`low_code_metadata_audit_events`, không lọc theo entity, `ORDER BY occurred_at DESC LIMIT`.
`AuditEvent` thêm field `entity_name` (trước đó ngầm định qua tham số hàm, giờ cần tường minh vì
kết quả trộn nhiều entity) — thay đổi cộng thêm, không phá `list_for_entity` hiện có.
`GET /admin/lowcode/audit?limit=N` (`crates/metap-lowcode-http`, gate `AdminContext`) — `limit`
mặc định 50, kẹp tối đa 200 (cùng nguyên tắc "mọi list đều có max limit" của `QueryPlanner`, áp
dụng thủ công ở đây vì bảng audit không đi qua `QueryPlanner`/`records`). 1 test e2e mới
(`crates/metap-lowcode/tests/store.rs`: audit event nhiều entity trộn đúng thứ tự mới nhất
trước, `limit` được tôn trọng, `list_for_entity` không bị ảnh hưởng). Verify sống qua HTTP thật:
publish 2 entity mới → `GET .../audit?limit=5` trả về đúng 4 event (draft+publish mỗi entity),
mới nhất trước, kèm `entityName` phân biệt; `limit=99999` bị kẹp, không trả nguyên bảng.

Còn lại của Phase C: **publish approval workflow** — chính spec gốc
(`docs/low-code-platform-v1.md`) ghi là "nếu cần", chưa có tín hiệu nhu cầu thật (mọi hành động
draft/publish hôm nay chỉ gate bởi `AdminContext` chung, chưa có nhu cầu tách vai trò soạn/duyệt).
**Quy tắc cô lập schema cấp tenant** — bị chặn bởi một quyết định kiến trúc lớn hơn nhiều (metadata
low-code hôm nay GLOBAL, không theo tenant, quyết định tường minh từ Phase A); hướng dài hạn đã
ghi lại ở `docs/team-charter.md`'s "Metadata low-code theo từng Tenant" (2026-08-22), chưa có
trigger để bắt đầu.

## Phase 12: Rust Core Migration

**Trạng thái: Đã quyết định, Migration Order đã hoàn tất, chưa deploy.** `packages/core`
chuyển hoàn toàn sang Rust cho mọi deployment profile — xem
[09. Architecture Decisions](architectures/09-adr.md). Không phải một
sub-item của phase nào trước đó: nó tái định hình *ngôn ngữ implementation* của execution
engine mà mọi phase khác ở trên được xây dựng dựa vào, mà không thay đổi những gì các phase
đó thực sự deliver (metadata compiler, permission engine, query planner, workflow engine,
CRUD, HTTP layer, peripherals — tất cả được re-implement 1:1, không redesign).

Mục tiêu:

- ~~Quyết định có chuyển `packages/core` sang Rust hay không~~ — **Đã xong (2026-08-07)**,
  Option B (mọi profile), sau khi một spike đo được lợi ích footprint/throughput thật.
- ~~Port execution engine (Migration Order bước 1-9)~~ — **Đã xong (2026-08-07)**:
  `crates/` là một Cargo workspace 9 crate (`metap-infra`, `metap-metadata`,
  `metap-permission`, `metap-query`, `metap-workflow`, `metap-crud`, `metap-http`,
  `metap-peripherals`, cộng binary `outbox-publisher`) — 51 unit test (không cần DB) + 19
  e2e test (Postgres/RabbitMQ thật, một HTTP server thật với một JWT RS256 thật) đều pass,
  `cargo build --release --workspace` sạch. Hai bug thật chỉ được bắt bởi e2e/live
  verification (một gap defaulting `data`/`status` trong `CrudService`, một panic ở
  CORS-config chỉ tái hiện được với một origin list không rỗng) — cả hai đã fix, cả hai giờ
  đều có test bao phủ.
- ~~Chứng minh việc port trên business entity thật, không chỉ fixture~~ — **Đã xong
  (2026-08-07)**: `apps/crm-server` (ban đầu là `crates/crm-server`, chuyển đi khi `crates/`
  được scope lại chỉ còn library crate + ops binary — xem ghi chú Repo Structure bên dưới),
  một binary tương đương `apps/crm` thật, chạy đúng entity `crm.customers` (port từ
  `customer.entity.ts`), đã verify live qua HTTP — chạy bằng `pnpm dev:rs`.
- ~~Xóa `apps/crm`/`packages/core` một khi phần port không còn cần chúng nữa~~ — **Đã xong
  (2026-08-07)**. Đóng ba gap trước để không có gì bị bỏ rơi âm thầm: JWT key chuyển sang
  `crates/crm-server/keys/`, ba dev script `packages/core/scripts/*.mjs` trở thành các
  subcommand của `crates/dev-tools`, và SQL migration của Drizzle được copy sang
  `crates/migrations/` cùng `crates/db-migrate` (`sqlx::migrate!`) được thêm vào để apply
  chúng — đã verify bằng cách chạy toàn bộ e2e suite trên một database được migrate từ đầu
  chỉ bằng công cụ đó, *trước khi* xóa bất cứ thứ gì. `packages/platform-react`/`apps/crm-fe` không bị đụng đến
  (frontend vốn luôn chỉ giao tiếp qua HTTP). Gap đã biết được phát hiện lúc đó: các admin
  HTTP route (policy CRUD, role grant/revoke) chưa tồn tại qua HTTP, chỉ tồn tại như các hàm
  có e2e coverage — đã đóng 2026-08-08, xem `crates/metap-http/src/routes/admin.rs`
  (extractor `AdminContext` yêu cầu role `admin`; `/admin/users`,
  `/admin/users/{userId}/roles[/{role}]`, `/admin/policies[/{id}]`,
  `/admin/policies/explain`), đã verify live trên một dev stack Postgres/RabbitMQ thật
  (assign/revoke/list role, create/list/delete/explain policy, 401 khi chưa auth, 403 khi
  không phải admin).
- Cut stack Rust sang thực sự phục vụ traffic. — **Chưa bắt đầu.** Chưa tồn tại production
  deployment topology cho việc này (cùng gap mà Phase 8 Hardening đã track cho stack TS);
  đây là một quyết định riêng, sau này, không mặc nhiên kéo theo khi việc port hoàn tất.
- Retarget các spec đang author bằng TS còn dang dở của Phase 11 (bắt đầu từ
  `docs/low-code-metadata-storage-design.md`) sang Rust trước khi implement chúng. — Chưa
  bắt đầu.

## Phase 13: Dynamic Cron Jobs

**Trạng thái: Đã xong (2026-08-10) — backend và admin UI đều đã live.** Scheduled job metadata-driven — một operator định nghĩa một job định kỳ (schedule + target action) qua admin API, theo đúng cách policy/role được định nghĩa hiện nay, thay vì một developer tự hand-wire một cron entry mới trong code. "Dynamic" là từ khóa chính: tập hợp job là dữ liệu mà platform đọc lúc runtime, không phải một danh sách cố định bake vào binary lúc compile time.

Đã implement:

- **Storage** (`crates/metap-cron`): bảng `cron_jobs`/`cron_job_runs` (`crates/migrations/0006_cron_jobs.sql`) — platform/ops config, không phải một `EntityDefinition`/row trong `records` generic, cùng loại với `policies`/`user_roles`. Một job có `cronExpr` (cú pháp 6-field chuẩn của crate `cron`, ví dụ `0 */5 * * * *`), một `timezone` IANA tường minh (occurrence được tính trong timezone đó, không phải giờ server-local — `schedule::next_run_at`), và một `targetType` (`workflow_transition` | `bulk_query_action` | `webhook`) + một blob JSON `targetConfig`.
- **Dispatch, reliable-vs-fire-and-forget theo từng job** (`crates/cron-scheduler`, chạy bằng `pnpm worker:cron:rs`): một ticker poll `cron_jobs` tìm entry đến hạn (`SELECT ... FOR UPDATE SKIP LOCKED`, cùng cách claim an toàn về concurrency mà `outbox-publisher::publish_pending` dùng, để nhiều replica scheduler không bao giờ fire trùng một job), advance `next_run_at`, và insert một row `cron_job_runs` — rồi rẽ nhánh theo `dispatchMode` của job (2026-08-09, thêm vào sau review: không phải job nào cũng cần độ bền cấp outbox). `dispatchMode: "outbox"` (mặc định) ghi một outbox event `cron.job.due` trong cùng transaction, tái sử dụng `outbox-publisher` hiện có để thực sự đưa nó lên RabbitMQ (không bao giờ publish trực tiếp); một executor trong cùng process `cron-scheduler` subscribe routing key đó và chạy job — at-least-once, sống sót qua một lần `cron-scheduler` crash giữa lúc claim và lúc thực thi. `dispatchMode: "direct"` bỏ qua hoàn toàn hop outbox/RabbitMQ: ticker gọi thẳng đúng hàm thực thi đó in-process, trong cùng tick đã claim job — latency thấp hơn (đã xác nhận live: claim-đến-webhook-call ~16ms so với latency ~1s poll-interval của đường outbox), nhưng đúng nghĩa fire-and-forget — một crash giữa chừng thực thi sẽ mất lần fire đó, không có redelivery. Doc comment của `metap_cron::DispatchMode` có đầy đủ tradeoff.
- **Việc thực thi vẫn entity-agnostic**: các target `workflow_transition`/`bulk_query_action` gọi ngược lại vào chính bề mặt `/api/:entity/...` HTTP của `crm-server` sở hữu chúng, với một JWT dịch vụ đã mint sẵn (`CRON_SERVICE_JWT`), thay vì để `cron-scheduler` link trực tiếp `metap-crud`/`metap-metadata` — tái sử dụng permission check, field validation, optimistic-locking, và workflow audit trail miễn phí, đồng thời giữ đúng boundary mà rule của CLAUDE.md yêu cầu (không `metap-*`/ops-binary nào được biết business-entity). Target `webhook` thì gọi một URL bên ngoài tùy ý. Ràng buộc đã biết: claim `tenantId` của service JWT cố định tenant nào mà một executor thực sự chạy job được — một job có `tenant_id` không khớp sẽ fail lúc thực thi (không tìm thấy record/entity), không phải lỗ hổng bảo mật, nhưng deployment multi-tenant hiện cần một `CRON_SERVICE_JWT`/executor cho mỗi tenant.
- **Admin API** (`crates/metap-http/src/routes/cron.rs`): `GET/POST /admin/cron-jobs`, `GET/PATCH/DELETE /admin/cron-jobs/{id}`, `GET /admin/cron-jobs/{id}/runs` — được gate bởi `AdminContext` giống `routes/admin.rs`, validate `cronExpr`/`timezone`/`targetType` lúc ghi (`metap_cron::validate_schedule`) thay vì fail âm thầm lần đầu tiên ticker thử schedule một job hỏng.
- **Admin UI của `packages/platform-react`** (`CronJobsAdminPage`, Phase 15, 2026-08-10): create/list/delete job, enable toggle, lịch sử run theo từng job — xem Phase 15 để biết phần còn lại của admin kit được ship kèm.

Chưa làm:

- **Alert khi fail lặp lại** — retry-with-backoff đã xong (Phase 17 Increment 1, 2026-08-21):
  `cron_jobs.maxAttempts`/`retryBackoffSeconds` áp dụng cho mọi job (cả `schedule` lẫn
  `on_transition`), `finish_run_with_retry` tự lên lịch lần thử tiếp theo khi còn attempt, ticker
  claim qua `claim_due_retries`. Phần còn thiếu chỉ còn *alert* khi một run fail hết attempt —
  chưa có gì báo cho ai, phụ thuộc vào việc kênh notification thật cuối cùng sẽ là gì
  (`crates/notification-worker` hiện chỉ stdout).
- **Multi-tenant executor routing** — xem ràng buộc `CRON_SERVICE_JWT` ở trên.

## Phase 14: Multi-language (i18n)

**Trạng thái: UI chrome đã xong (2026-08-09); metadata-label translation — hướng đã chốt 2026-08-22, chưa code.** Hai mối quan tâm tách biệt nhau:

- **Frontend UI chrome** (`packages/platform-react`, `apps/crm-fe`) — đã xong. `react-i18next`/`i18next` được wire vào `platform-react` (`src/i18n/`: `resources.ts` giữ bảng string `en`/`vi`, `i18n.ts` tạo một instance i18next riêng — không phải singleton cấp module, để một app embed `platform-react` cùng với setup i18next của riêng nó không bị đụng độ). `LocaleProvider` (phải nest bên trong `AuthProvider`) load locale của caller từ `GET /preferences` mới lúc mount và bọc `I18nextProvider`; `useLocale()`/`LocaleSwitcher` ghi ngược lại qua `PUT /preferences`. Mọi string chrome tĩnh trong `GeneratedForm`/`GeneratedList`/`RecordDetail`/`WorkflowActionBar`/`ApiErrorMessage` và các route guard `DevLoginPage`/`EntitiesPage`/`App.tsx` của `crm-fe` giờ đều đi qua `useTranslation()` — label entity/field không bị đụng đến (xem bên dưới, vẫn single-locale từ metadata).
- **Backend locale storage** — đã xong. Bảng `user_preferences` (`crates/migrations/0007_user_preferences.sql`, primary key `tenant_id`+`user_id`) qua `metap_peripherals::preferences` (`get_locale`/`set_locale`, theo cùng phong cách plain-function của `role_assignment.rs`) và `GET/PUT /preferences` (`crates/metap-http/src/routes/preferences.rs`, self-service gated bởi `AuthContext`, không phải `/api/preferences` — sẽ đụng độ với wildcard `/api/{entity}` của `routes::records`). Locale được validate theo một allowlist nhỏ `SUPPORTED_LOCALES` (`en`, `vi` hiện nay — phải giữ đồng bộ với `SUPPORTED_LOCALES` của `packages/platform-react/src/i18n/resources.ts`, kiểm tra thủ công, chưa có single source of truth chung).
- **Nội dung được author qua metadata (label entity/field/list-view, tên workflow action, validation message)** — hướng đã chốt (2026-08-22), **chưa code, chưa có trigger**. Vẫn là string single-locale hard-code trong `EntityDefinition`/`EntityField`/v.v. (xem `apps/crm-server/src/entities/customer_entity.rs`). **Chọn hướng translation-key/override table**, không phải đổi `label` thành `Record<locale, string>` — nhất quán với chính mẫu Phase 14 đã dùng cho UI chrome (`resources.ts`: bảng resource *tách khỏi* string gốc, không nhét map vào từng chỗ dùng string). Cụ thể: `EntityField`/`EntityDefinition`/`EntityListView`/`WorkflowTransition`'s `label: String` **giữ nguyên** (vẫn là text mặc định/fallback) — zero breaking change, cả 4 entity code-authored hiện có và OpenAPI generator/`generated-types.ts` không cần sửa; một bảng override mới (`metadata_label_translations(entity_name, field_name?, locale, text)`, phác thảo) tra cứu lúc render — có override cho locale đó thì dùng, không có thì fallback về `label` gốc (hỗ trợ tự nhiên việc dịch từng phần, không lỗi khi thiếu). Hoạt động đồng nhất cho cả entity code-authored lẫn DB-authored (bảng override không quan tâm entity định nghĩa ở đâu). Đánh đổi: hai chỗ phải nhìn khi debug label (gốc hay override), cần một màn quản lý translation riêng (không phải sửa ngay trong field editor low-code hiện có).

Chưa làm:

- Implementation của hướng đã chốt ở trên — chưa có trigger nhu cầu đa ngôn ngữ thật cho metadata label.
- Chỉ có hai locale có resource đã dịch (`en`/`vi`); thêm một locale thứ ba nghĩa là cần cả một entry mới trong `resources.ts` lẫn thêm vào `SUPPORTED_LOCALES` của backend.
- `data` của record (giá trị field do user nhập) và locale: có chủ đích nằm ngoài scope — đó là dữ liệu business của tenant, không phải nội dung platform sở hữu — nhưng đáng nói rõ ra để không bị ai giả định sau này.

## Phase 15: Shared App Shell (UI kit, real login, permission-aware components)

**Trạng thái: Đã xong (2026-08-10) — real login, shared app shell, permission primitive, và admin UI kit đều đã live.** `apps/crm-fe` đã chứng minh các màn hình CRUD generated của `packages/platform-react` (`GeneratedList`/`GeneratedForm`/`RecordDetail`/`WorkflowActionBar`) hoạt động tổng quát trên mọi entity; phase này đóng gap ở mọi thứ *xung quanh* các màn hình đó — login, page chrome, UI gated theo permission, và các màn hình admin để quản lý policy/role/user/cron-job — vốn trước đây được hand-roll riêng lẻ theo từng app hoặc hoàn toàn chưa tồn tại.

Đã implement:

- **Flow login thật** (local username/password, được chọn thay vì federated IdP cho phase này — tự chứa, không phụ thuộc bên ngoài cho một demo/kernel platform). Bảng `users` (`crates/migrations/0009_users.sql`: `tenant_id`, `email` unique, `password_hash`) qua `metap_peripherals::auth` — `create_user`/`verify_credentials` (argon2id; `verify_credentials` luôn trả cùng chi phí argon2-verify trên một email không tồn tại bằng một dummy hash tính sẵn, để "email không tồn tại" và "sai mật khẩu" không thể phân biệt được qua timing) và `mint_jwt`, implementation JWT-encoding **duy nhất** trong repo hiện nay — `POST /auth/login` (`crates/metap-http/src/routes/auth.rs`) và `dev-tools mint-token` đều gọi nó, để một token mint từ CLI và một token từ real-login không thể lệch nhau về claim shape. `crm-server` trước đây có chủ đích chỉ verify-only (chỉ giữ key *decoding* của JWT); việc mint từ một real login nghĩa là giờ nó cũng load thêm key *encoding* lúc boot (`AUTH_JWT_PRIVATE_KEY_PATH`, bắt buộc, cùng keypair mà `pnpm auth:dev-keys` đã generate) — một dịch chuyển kiến trúc thật sự, không chỉ là một route mới.
- **Provisioning**: `POST /admin/users` (email+password, `roles` tùy chọn để assign trong cùng lệnh gọi; `409 email_taken` khi email trùng) và `dev-tools create-user <tenantId> <email> <password>` cho dev-seeding — cả hai đều gọi cùng `create_user`.
- **`GET /auth/me`** (`crates/metap-http/src/routes/auth.rs`): trả về `{userId, tenantId, roles}` cho token của chính caller qua `AuthContext` — được thêm riêng để frontend biết role của chính nó phục vụ UI gating, vì role có chủ đích không bao giờ được encode trên chính JWT (được tra cứu mới từ `user_roles` cho mỗi request, giống mọi route `AuthContext` khác).
- **`LoginForm`** trong `packages/platform-react` (email+password, gọi `POST /auth/login`, phân biệt `invalid_credentials` để hiện message đã dịch so với các lỗi khác), thay thế `DevLoginPage` của `apps/crm-fe` — đổi tên thành `LoginPage` tại route `/login` (trước là `/dev-login`, gây hiểu lầm giờ khi nó đã là login thật). `pnpm mint-token`/`pnpm seed:admin` vẫn hoạt động không đổi để mint token thủ công mà không cần qua real login.
- **Permission-aware UI primitive**: `useCurrentUser()` (query `GET /auth/me`) cộng `useHasRole()`/`<Can roles={[...]} fallback={...}>` (`packages/platform-react/src/auth/`) — chỉ gate ở phía UI (server vẫn re-check qua `AdminContext` bất kể frontend ẩn gì), dùng để filter nav link và gate ba admin route bên dưới.
- **Shared app shell**: `AppShellLayout` (`packages/platform-react/src/shell/`) — một header `AppShell` của Mantine với brand/nav (nav item tùy chọn gate theo role qua `useHasRole`), `LocaleSwitcher`, badge role của current-user, và logout, thay thế boilerplate `Container`/`Group`/`Title` mà `EntitiesPage.tsx` từng hand-roll. `RequireAuth` của `apps/crm-fe` giờ bọc mọi route đã auth trong nó.
- **Admin UI kit**: `packages/platform-react/src/admin/` — `adminApi.ts` (các hook cho users/policies/cron-jobs qua `/admin/*`) cộng `UsersAdminPage` (create user, list, assign/revoke role), `PoliciesAdminPage` (create/list/delete policy, editor raw-JSON cho `PolicyCondition`), `CronJobsAdminPage` (create/list/delete job, enable toggle, lịch sử run theo từng job) — đóng gap "chưa có admin UI" của Phase 13. Được wire vào `apps/crm-fe` tại `/admin/users`, `/admin/policies`, `/admin/cron-jobs`, mỗi cái được gate bởi `<Can roles={["admin"]}>`.
- Đã verify live end-to-end (chạy bằng Playwright, `apps/crm-fe` + `crm-server` trên dev Postgres/RabbitMQ): login với mật khẩu sai và với một email không tồn tại đều trả về cùng `401 invalid_credentials`; user do admin provision có thể login ngay với mật khẩu admin đã đặt; nav link cho Users/Policies/Cron Jobs chỉ render với một token có role `admin`; round-trip create/list/delete đầy đủ trên cả ba trang admin kể cả lịch sử run của cron-job.
- Hai bug thật được phát hiện và fix trong lần verify đó (không tồn tại trước phase này): (1) dev proxy của `apps/crm-fe/vite.config.ts` thiếu `/admin` (có `/api`/`/metadata`/`/health`/`/preferences`/`/auth` nhưng không có `/admin`), khiến mọi request của admin-kit trả 404 từ chính dev server của Vite thay vì đến được `crm-server`; (2) `apiFetch` trong `packages/platform-react/src/api/client.ts` luôn gọi `response.json()` vô điều kiện, thứ sẽ throw trên một body `204 No Content` — vô hình cho đến phase này vì mọi route DELETE của caller hiện có đều trả `200 {data}`, nhưng `/admin/policies/:id`, `/admin/users/:id/roles/:role`, và `/admin/cron-jobs/:id` đều trả `204` trần; `apiFetch` giờ short-circuit về `undefined` khi gặp 204.

Chưa làm (gap đã biết, không nằm trong hàng đợi):

- Token refresh/rotation, "quên mật khẩu", xác minh email, rate-limiting login theo từng route ngoài limiter global per-IP hiện có.
- Admin UI kit hoạt động được nhưng còn tối giản: chưa có pagination trên bất kỳ admin list nào, `PolicyCondition`/`targetConfig` của cron là textarea raw-JSON thay vì structured builder, chưa có bộ chuyển tenant (chỉ một dev tenant duy nhất).

Liên quan đến: Phase 13 (admin UI cho cron — được đóng bởi admin kit của phase này), Phase 11 (shared shell là một phần của platform surface, không phải mối quan tâm riêng của từng app).

## Phase 16: Multi-tenant SaaS Control Plane & Data Plane

**Trạng thái: Giai đoạn 1 (control-plane skeleton) đã triển khai (2026-08-16)** — crate mới
`crates/metap-control` (`Router`, `control.tenants` registry, `RegistryCache`) và `CrudService`
(`crates/metap-crud`) đã refactor để mọi method (list/get/create/update/transition/delete) đi
qua `Router::begin(tenant)` thay vì `&PgPool` trực tiếp — đúng seam đã chốt ở §2.2. **Không đổi
hành vi runtime nào** ở giai đoạn này: chưa có tenant nào được provision qua `control.tenants`
(bảng mới toanh, `crates/migrations/0012_control_tenants.sql`), nên `Router::begin` áp dụng
fallback tương thích ngược có chủ đích — tenant chưa có row → coi như
`{status: Active, strategy: Schema("public")}`, đúng hành vi trước khi có Router (mọi thứ vẫn nằm
`public` schema, isolation vẫn là cột `tenant_id` như cũ). Đã verify: 5 kịch bản e2e Router
(`cargo test -p metap-control -- --ignored`, gồm kịch bản chứng minh `SET LOCAL search_path`
không rò qua pool tái dùng — bẫy #1 nghiêm trọng nhất của thiết kế) + 4 test e2e `CrudService`
cũ pass y hệt không đổi + smoke thủ công qua HTTP (create/get/update/transition/delete) đều 200.

**Giai đoạn 2 (tenant provisioning + `DedicatedDb`) đã triển khai (2026-08-16).** `dev-tools
provision-tenant` (`pnpm provision:tenant`) là cách duy nhất ghi row `control.tenants` hôm nay
(không có HTTP `POST /admin/tenants` — `AdminContext` chỉ ủy quyền trong tenant của chính người
gọi, chưa có khái niệm "platform superadmin" xuyên tenant). Hai nhánh:
- `schema` (trial): luôn ghim `schema_name='public'` — **chưa có isolation thật**, vì bảng
  `records`/`users`/... chỉ tồn tại ở `public` cho tới khi data-plane evolution (§3,
  table-per-entity) triển khai. Route một tenant sang schema khác hôm nay sẽ vỡ hết query.
- `dedicated_db` (paid): **có isolation thật** — chạy migration lên một DB Postgres riêng, ghi
  `control.tenants.dsn_secret_ref`, tạo admin user trên DB đó. `crates/metap-control::SecretStore`
  (trait) + `EnvStore` (impl duy nhất — đọc DSN từ biến env tên đúng bằng `dsn_secret_ref`, chưa
  có Vault) + `Router`'s `dedicated_pools` cache (moka, idle TTL 10 phút) làm cho
  `Router::begin` mở transaction đúng trên DB riêng. Đã verify end-to-end qua HTTP thật: record
  tạo qua tenant `dedicated_db` chỉ nằm trong DB riêng, không xuất hiện ở DB chính.

**Đã fix 2026-08-21** (xem Phase 3): `check_action` giờ deny-by-default cho non-admin khi
entity/action chưa có policy — admin vẫn luôn bypass, và `POST /admin/policies/seed-defaults`
là công cụ nhanh để gán quyền cho role của tenant mới, tránh phải gọi `create_policy` từng cái.

**Giai đoạn 3 (HTTP tenant provisioning + platform-superadmin) đã triển khai (2026-08-17)** —
trigger đi theo hướng B đã chốt. Mô hình "platform superadmin" tái dùng 100% hạ tầng JWT/role
sẵn có, không thêm bảng/loại claim mới:
- `metap_control::PLATFORM_TENANT_ID` (`Uuid::nil()`, all-zero) — một tenant sentinel, **không
  bao giờ** có row `control.tenants`, không bao giờ được `Router` route tới. Chỉ tồn tại để
  `users`/`user_roles` (luôn ở `public`, không qua Router) có chỗ giữ danh tính platform-admin.
- Role `"platform_admin"` (một role name như bất kỳ role nào khác, gán qua
  `metap_peripherals::assign_role` sẵn có) cho user trong tenant sentinel đó.
- `PlatformAdminContext` (`crates/metap-http/src/auth.rs`, cạnh `AdminContext`) — extractor mới
  check `tenantId == PLATFORM_TENANT_ID && roles chứa "platform_admin"`, khác `AdminContext`
  (chỉ ủy quyền trong tenant của chính người gọi).
- `dev-tools bootstrap-platform-admin <email> <password>` (`pnpm bootstrap:platform-admin`) —
  bootstrap con-gà-quả-trứng đầu tiên, cùng kiểu `seed-admin` đã giải quyết cho tenant admin.
- Logic provisioning (trước đây inline trong `dev-tools`) được kéo ra
  `metap_control::provision_schema_tenant`/`provision_dedicated_db_tenant` — CLI và HTTP giờ
  gọi chung 2 hàm này, không thể lệch nhau (cùng lý do `mint_jwt`/`create_user` đã dùng chung
  trước đó). `PostgresTenantRegistry::list()` (mới) hỗ trợ `GET /platform/tenants`.
- Crate mới `crates/metap-control-http` (cùng lý do tách riêng `metap-lowcode-http` — khả năng
  optional, `metap-http` không phụ thuộc nó): `POST /platform/tenants` (body có `strategy:
  "schema"|"dedicated_db"`, 409 nếu `tenantId` trùng, 400 nếu thiếu field theo strategy),
  `GET /platform/tenants`, `GET /platform/tenants/{id}`, `PATCH /platform/tenants/{id}/status`
  (suspend/resume — thêm ngay sau đó cùng ngày 2026-08-17). **Chưa làm** (out of scope đợt
  này): delete/deprovision — cần thiết kế riêng cho việc dọn dữ liệu tenant.
- **Suspend/resume hoá ra là một việc rất nhỏ**: enforcement đã tồn tại sẵn từ Giai đoạn 1 —
  `Router::begin` đã reject `TenantStatus::Suspended` với 403 (`RouterError`, xem
  `crud_service.rs`'s `router_unavailable`) từ trước, việc còn thiếu chỉ là hành động admin để
  đổi cột `status`. `PostgresTenantRegistry::set_status(id, status)` (mới, chỉ nhận
  `"active"`/`"suspended"` qua route — các status khác do flow chưa xây quản lý) +
  `PATCH /platform/tenants/{id}/status`. Chịu ảnh hưởng của `RegistryCache`'s TTL 30s có sẵn
  (đã document từ trước là tradeoff chấp nhận được cho "provisioning, suspend/promote") — một
  suspend/resume có thể mất tới 30s mới có hiệu lực trên route đã cache, không phải bug mới.
- Test mới: 5 test e2e `metap-control` (provisioning + `list()` + trùng `tenantId` → lỗi
  downcast được thành unique-violation + `set_status` nối trực tiếp với `Router::begin` reject
  thật), 1 test e2e `metap-http` (`PlatformAdminContext` gate qua route giả lập).
  `metap-control-http` không có test tự động (đúng tiền lệ `metap-lowcode-http`) — verify live
  qua HTTP thật: bootstrap platform-admin → mint token → provision tenant `schema` → 409 khi
  trùng id → `GET /platform/tenants`/`{id}` (kể cả 404) → admin user tenant mới login được qua
  `POST /auth/login` → provision tenant `dedicated_db` → thiếu field bắt buộc → 400 → strategy
  sai → 400 → suspend → tenant đó bị 403 trên `/api/*` → status không hợp lệ → 400 → id không
  tồn tại → 404 → resume → hoạt động lại → token không phải platform-admin → 403 trên mọi route
  `/platform/*`, kể cả một admin thường của tenant khác.

**Giai đoạn 4 (Vault) — bắt đầu, `VaultStore` đã xong (2026-08-17).** `crates/metap-control::VaultStore`
— second `SecretStore` impl cạnh `EnvStore` — static KV v2 secret qua HTTP API của Vault (crate
`vaultrs`), token auth (`VAULT_TOKEN`), không phải AppRole, không phải dynamic database-credentials
engine của Vault (cả hai đều là gap thật, cố tình để lại tới khi có một production deployment
target thật sự cần — `DbCreds::expires_at` vẫn luôn `None`, giống `EnvStore`). Lựa chọn store nào
(`EnvStore` hay `VaultStore`) giờ chuyển lên composition root (`apps/crm-server/src/main.rs`,
`AppState::new` nhận `secret_store: Arc<dyn SecretStore>` thay vì tự build `EnvStore` bên trong) —
hành vi mặc định không đổi: vẫn `EnvStore` trừ khi `VAULT_ADDR`/`VAULT_TOKEN` (`metap-infra`'s
`AppConfig`) được set, nên không downstream project nào bị ép chạy Vault container để dev bình
thường. `dev-tools vault-put-dsn <dsnSecretRef> <dsn>` (`pnpm`-equivalent chưa thêm) ghi DSN vào
Vault cho một tenant `dedicated_db` — đối trọng Vault-backed của bước "set env var" mà
`provision-tenant` vẫn in ra khi dùng `EnvStore`. `docker-compose.yml` có thêm service `vault` (dev
mode, fixed root token) — opt-in, không nằm trong stack mặc định `docker compose up -d postgres
rabbitmq`. Test: 3 e2e (`crates/metap-control/tests/vault_store.rs`, `--ignored`, cần một dev Vault
sống). Đóng lại luôn phần "Design-only, chưa code" mà Phase 8's bullet secret manager từng ghi.

**Role lookup + RBAC/policy qua Router — Đã xong (2026-08-20), đóng một bug thật, không chỉ một
gap kiến trúc.** Rà soát lại roadmap phát hiện dòng "role lookup và `PostgresPolicyStore` vẫn
dùng `AppState.pool` trực tiếp" phía trên **sai lý do**: đây không phải RBAC/policy là bảng
control-plane dùng chung an toàn để bỏ qua Router — `provision_dedicated_db_tenant` chạy toàn bộ
`crates/migrations/*.sql` (gồm `users`/`user_roles`/`policies`) lên DB riêng của tenant, nên với
một tenant `dedicated_db` các bảng này **chỉ tồn tại trong DB riêng đó**, không bao giờ có trong
pool control-plane dùng chung. Verify trực tiếp (không chỉ đọc code): provision một tenant
`dedicated_db` thật qua `POST /platform/tenants` → admin user được tạo đúng trong DB riêng
(query xác nhận) → `POST /auth/login` với đúng email/password đó → **`401 invalid_credentials`**,
vì `verify_credentials` query nhầm pool chung. Kết luận: **toàn bộ tier `dedicated_db`** (Phase 16
Giai đoạn 2, 2026-08-16) **không ai login được** kể từ khi ship — không phải RBAC lỏng, mà auth
hỏng hoàn toàn; không bị phát hiện trước đó vì narrative verify của Giai đoạn 3 chỉ test login
cho tenant `schema`.

Đã fix toàn bộ, không phải patch một phần:
- `metap_peripherals::role_assignment` (`get_roles_for_user`/`assign_role`/`revoke_role`/
  `list_users`) và `metap_peripherals::auth` (`verify_credentials`/`create_user`) đổi từ
  `pool: &PgPool` sang generic `impl PgExecutor<'e>` (cùng pattern `metap-crud::crud_service`'s
  `fetch_existing` đã dùng) — vừa chạy được với một `&PgPool` trần (provisioning, trước khi
  `control.tenants` row tồn tại nên Router chưa route được), vừa chạy được với một
  `Router::begin`-transaction (mọi call site còn lại).
- `PostgresPolicyStore` **chuyển từ `metap-permission` sang sống trong `metap-control`**
  (`crates/metap-control/src/policy_store.rs`) — lý do thuần dependency-cycle, không phải
  ranh giới thiết kế mới: `metap-metadata -> metap-permission`, `metap-peripherals ->
  metap-metadata`, `metap-control -> metap-peripherals`; `metap-permission -> metap-control`
  (để với tới `Router`) sẽ khép vòng lặp đó. Trait `PolicyStore` vẫn ở `metap-permission`
  (`row_from_sql` được đổi `pub` để impl bên `metap-control` tái dùng); mọi method của trait đã
  sẵn nhận `tenant_id: Uuid` nên không cần đổi signature, chỉ đổi phần lưu trữ — mỗi method giờ
  tự `router.begin(tenant_id.into())` rồi commit.
- `AppState` (`metap-http`) có thêm field `router: Router` public; `AppState::new` nhận
  `router: Router` thay vì tự build từ `secret_store` — `Router` giờ được build một lần ở
  composition root (`apps/crm-server/src/main.rs`) và chia sẻ cho cả `PostgresPolicyStore::new`
  lẫn `AppState`/`CrudService`, thay vì hai `Router`/`RegistryCache` độc lập.
- `AuthContext` (`crate::auth`, mọi request đã auth) route role lookup qua
  `state.router.begin(tenant_id)` — `PLATFORM_TENANT_ID` (sentinel, không bao giờ có
  `control.tenants` row) tự động rơi vào fallback "unregistered tenant → public schema" sẵn có
  của `Router::begin`, đúng nơi `users`/`user_roles` của nó thật sự nằm, không cần
  special-case.
- `POST /auth/login` thêm field **tuỳ chọn** `tenantId` vào body. Có `tenantId` → route qua
  `Router::begin(tenantId)` (bắt buộc với `dedicated_db`, vì `users` không nằm ở pool chung).
  Không có `tenantId` → giữ nguyên hành vi cũ (query pool chung theo email global) — đúng mặc
  định cho tenant `schema` (hiện vẫn dùng chung `public`, chưa có isolation thật, nên email vẫn
  là khoá tra cứu duy nhất khả dụng cho nhóm này). Không phải breaking change cho flow hiện có,
  chỉ thêm khả năng mới.
- `/admin/users`, `/admin/users/{id}/roles[/{role}]` (`routes/admin.rs`) route qua Router;
  `create_user` giờ chạy insert user + mọi role assignment trong **một** transaction thay vì một
  connection mỗi lệnh gọi — tiện thể đóng luôn một gap atomicity có sẵn từ trước (một role
  assignment fail giữa chừng từng để lại user đã tạo nhưng chỉ có một phần role, không cách nào
  biết role nào fail).
- `templates/metap-app` (main.rs + tests/http_server.rs) cập nhật theo cùng shape — verify bằng
  `cargo generate` một project thật (không nằm trong workspace nên `cargo check` gốc không tự
  bắt được) rồi trỏ dependency `metap` sang path local, `cargo check --tests` sạch.

Verify: toàn bộ test suite hiện có (`cargo test --workspace` + `-- --ignored` trên Postgres/
RabbitMQ/Vault thật) pass không đổi, cộng test mới cho template. Verify live riêng cho đúng bug
gốc: provision lại tenant `dedicated_db` → `POST /auth/login` **kèm** `tenantId` → 200, JWT hợp
lệ → `GET /auth/me` trả đúng `roles: ["admin"]` (role lookup qua Router hoạt động) →
`GET /admin/users` liệt kê đúng user của tenant đó → `POST /admin/policies` tạo policy thành
công — cả bốn đều chạm đúng DB riêng của tenant, không phải pool chung.

**Vault AppRole auth — Đã xong (2026-08-20).** `metap_control::VaultStore::new_with_approle`
(cạnh `new` token-based có sẵn) — login một lần lúc construct qua
`vaultrs::auth::approle::login`, `client.set_token(...)` với client token trả về. Lý do tồn tại
song song với token-based: `VAULT_TOKEN` nghĩa là phải phân phối tay một credential dùng-được-
ngay, sống lâu dài; AppRole's `role_id` không nhạy cảm (bake thẳng vào deploy manifest được),
`secret_id` mới là phần nhạy cảm và có thể để pipeline secret-injection (Vault Agent, một bước
CI, K8s injector) cấp ngắn hạn thay vì hand-carry một token thô. **Chưa làm, có chủ đích**: auto-
renew trước khi token hết hạn — token AppRole hết hạn thì mọi call Vault sau đó fail cho tới khi
restart process hoặc gọi lại constructor; cùng mức "không tự rotate" như token tĩnh vốn có, không
phải regression mới, nhưng vẫn là gap thật cần một background task hoặc retry-on-fail để đóng.
`AppConfig` thêm `vault_role_id`/`vault_secret_id`/`vault_approle_mount`
(`VAULT_ROLE_ID`/`VAULT_SECRET_ID`/`VAULT_APPROLE_MOUNT`, mount mặc định `"approle"`);
`apps/crm-server/src/main.rs` ưu tiên AppRole nếu cả `vault_role_id`+`vault_secret_id` đều có,
rồi mới tới token, rồi mới `EnvStore`. Test mới: `approle_login_can_read_a_dsn_written_by_a_token_authed_store`
(`crates/metap-control/tests/vault_store.rs`, doc comment của file có sẵn các bước `vault` CLI để
tự setup AppRole role trên dev Vault). Verify live, không chỉ test: enable `approle` + tạo role
qua `vault` CLI trên dev Vault container → boot `crm-server` thật chỉ với
`VAULT_ROLE_ID`/`VAULT_SECRET_ID` (không có `VAULT_TOKEN` trong env của chính nó) → provision một
tenant `dedicated_db` mới, DSN ghi vào Vault qua `dev-tools vault-put-dsn` (dùng root token,
việc của operator, tách biệt với credential read-only mà server tự dùng) → login vào tenant đó
→ 200, JWT hợp lệ — xác nhận `Router` resolve đúng DSN qua Vault bằng AppRole token, không phải
token tĩnh.

**Delete/deprovision tenant — Đã xong (2026-08-21).** Hai quyết định thiết kế chốt trước khi code
(không có sẵn trong `docs/architectures/09-adr.md`, thao tác phá huỷ nên hỏi trực tiếp thay vì tự
suy đoán): (1) `dedicated_db` — **không** tự động `DROP DATABASE` vật lý; (2) `schema` (hiện vẫn
chung `public`, chưa có isolation thật) — **không** tự động xoá record theo `tenant_id` trong
`records`/`users`/... dùng chung. Cả hai lý do giống nhau: một API call lỡ tay không nên xoá dữ
liệu vĩnh viễn, và với `schema` việc xoá theo `tenant_id` trên bảng dùng chung còn rủi ro hơn
(một bug trong `WHERE` ảnh hưởng tenant khác) khi chưa có data-plane isolation thật (§3).

Implement: `TenantStatus::Deleted` (mới, `metap-control::tenant`) — terminal, không có đường quay
lại như `Suspended`→`resume`. `Router::begin` reject với `RouterError::TenantDeleted`, map 404
(`metap-crud`'s `router_unavailable`, `metap-http`'s `router_unavailable_response`) — 404 chứ
không phải 403 như Suspended/Expired, vì tenant coi như không còn tồn tại nữa, không phải "tồn
tại nhưng tạm bị cấm". `PostgresTenantRegistry::deprovision(id)` (chỉ update cột `status`,
idempotent — gọi lại trên tenant đã xoá vẫn `true`, không lỗi). Route mới
`DELETE /platform/tenants/{id}` (`metap-control-http`, gate `PlatformAdminContext`, 204 thành
công / 404 nếu `id` không tồn tại) — tách riêng khỏi `PATCH .../status` (chỉ nhận
`active`/`suspended`) vì xoá là một chiều, không phải một giá trị status như hai cái kia.

Test mới: `deprovisioning_is_immediately_enforced_by_router_with_a_404_not_a_403`
(`crates/metap-control/tests/provisioning_postgres.rs`, e2e Postgres thật — cùng pattern test
suspend có sẵn). Verify live qua HTTP thật: provision tenant `schema` → `GET` thấy `status:
"active"` → `DELETE` → 204 → `GET` lại vẫn 200 nhưng `status: "deleted"` (row vẫn còn, chỉ đổi
status) → `DELETE` lần 2 vẫn 204 (idempotent) → `DELETE` một id không tồn tại → 404 → mint token
cho tenant đã xoá, gọi `/api/crm.customers` → bị chặn thật (không phải giả lập) — dù response
thực tế caller thấy là **401** "failed to resolve roles" từ `AuthContext`, không phải 404 từ
`CrudService`: role lookup cũng route qua `Router::begin` (fix RBAC 2026-08-20) và chạy trước
handler, nên với `Suspended`/`Expired`/`Deleted` một client luôn gặp 401 ở tầng auth trước khi
tới được tầng CrudService nơi 404/403 mới thực sự map — hành vi đã có sẵn cho
`Suspended`/`Expired` từ trước, không phải điểm không nhất quán mới do `Deleted` gây ra. `404
tenant_not_found` từ `CrudService`'s mapping vẫn đúng và có test riêng (Router-level), chỉ là
không phải status code một client thật nhìn thấy qua route đã-auth thông thường.

**AppRole auto-renewal — Đã xong (2026-08-21).** Đóng gap đã ghi nhận từ lúc ship AppRole
(2026-08-20): trước đây token AppRole hết hạn là mọi call Vault sau đó fail cho tới khi restart
process. `VaultStore` giờ giữ `client`/`expires_at` sau một `tokio::sync::Mutex` chung, và mọi
method của `SecretStore` (`db_credentials`, cộng `put_dsn`) tự kiểm tra + re-login (cùng
`role_id`/`secret_id`, một `vaultrs::auth::approle::login` mới) trước khi thực sự gọi Vault, nếu
còn dưới `RENEW_BUFFER` (60s) là hết hạn — không cần background task/renewal loop riêng, vì
`db_credentials` vốn đã không phải hot path (`Router` cache pool của tenant `dedicated_db` 10
phút). `lease_duration == 0` (quy ước "không hết hạn" của Vault) được xử lý riêng, không bị hiểu
nhầm thành "đã hết hạn ngay" (sẽ ép re-login ở mọi call nếu tính sai). Instance dùng token tĩnh
(`VaultStore::new`) không bị ảnh hưởng — vẫn không có khái niệm renew, đúng như trước.

Verify live, không chỉ đọc code: tạo một AppRole role riêng cho test với `token_ttl=5s` (ngắn hơn
nhiều `RENEW_BUFFER`) → login → **đợi thật 6 giây** (token gốc chắc chắn đã hết hạn phía Vault) →
gọi `db_credentials` → vẫn thành công, chỉ có thể vì code đã tự renew trước đó. Test mới
`approle_token_auto_renews_before_a_real_expiry` (`crates/metap-control/tests/vault_store.rs`,
~6s runtime, doc comment của file có sẵn lệnh `vault` CLI để tự tạo role ngắn hạn này). Toàn
workspace `build`/`clippy -D warnings`/`fmt --check`/`test` sạch, không regression trên 2 test
AppRole cũ.

**`/code-review` trên commit trên tìm ra 2 finding thật, cả hai đã fix cùng ngày (2026-08-21):**

1. **Renewal tiêu mất `secret_id` một-lần-dùng** — bản đầu tiên luôn re-login bằng
   `vaultrs::auth::approle::login` (cùng `secret_id` đã lưu) mỗi lần renew, tự phá chính nó ngay
   lần renew đầu tiên nếu role được cấu hình `secret_id_num_uses=1` — đúng khuyến nghị chính
   module doc comment đưa ra ("issued short-lived/one-time"). Fix: `ensure_fresh_token` giờ thử
   `vaultrs::token::renew_self` trước (gia hạn token hiện có, không cần `secret_id`), chỉ fallback
   về login lại khi `renew_self` fail (token vượt `max_ttl`). Verify live: tạo role với
   `secret_id_num_uses=1`, `token_ttl=65s` (dài hơn `RENEW_BUFFER` để `renew_self` chạy trong lúc
   token còn sống thật, không phải sau khi đã chết hẳn) → login (tiêu hết `secret_id`) → renew
   2 lần liên tiếp (mỗi lần cách nhau 6s) → cả 2 đều thành công, không đụng lại `secret_id` đã
   dùng. Test mới `renewal_survives_past_expiry_twice_without_reusing_a_single_use_secret_id`.
2. **Giữ lock qua trọn network call, serialize hết mọi tenant** — `Mutex` bọc `client` bị giữ
   suốt cả round-trip HTTP tới Vault, trong khi `VaultStore` là một `Arc<dyn SecretStore>` dùng
   chung cho `Router` của mọi tenant `dedicated_db` — N tenant cold-start/idle-evict cùng lúc sẽ
   xếp hàng qua đúng 1 lock thay vì chạy song song. Fix: `fresh_client()` chỉ giữ lock cho phần
   kiểm tra/renew (rẻ, hiếm khi cần I/O), rồi build một `VaultClient` mới (cùng `addr`/token hiện
   tại, qua `build_client` có sẵn) để gọi Vault **ngoài** lock — đánh đổi chấp nhận được (mất
   connection-pool reuse giữa các lần gọi, đổi lấy không serialize hoá cross-tenant) vì Vault
   không phải hot path ở đây.

Toàn workspace `build`/`clippy -D warnings`/`fmt --check`/`test` sạch lại sau cả 2 fix, 6/6 test
`vault_store.rs` pass trên dev Vault thật.

Còn lại cho Giai đoạn 4+: dynamic database-credentials engine thật của Vault (rotating creds,
không phải static DSN); template pack; data-plane evolution (§3-§7); capabilities (§8); FE
onboarding (§9); deployment SaaS specifics (§11).

Toàn bộ thiết kế nằm ở `docs/multi-tenant-platform-design.md` (hợp nhất từ hai bản nháp brainstorm
`adr.md`/`adr2.md` ngày 2026-08-15, đã xóa sau khi hợp nhất); các quyết định cốt lõi rút gọn dạng
bullet nằm ở [09. Architecture Decisions](architectures/09-adr.md). Tóm tắt phạm vi:

- **Tenant isolation**: tiered tenancy — schema-per-tenant cho trial (1 DB chung, N schema),
  DB-per-tenant cho paid (isolation vật lý). Thay cho đề xuất RLS-only ban đầu. §2.1.
- **Control plane**: `control.tenants` registry + `Router.begin(tenant)` (mọi query qua
  transaction, `SET LOCAL search_path` — không session-level, tránh rò tenant qua pool tái dùng),
  Vault cho secret + config 4 tầng kế thừa, tenant provisioning tự phục vụ + template pack YAML.
  §2.2-§2.5.
- **Data plane**: table-per-entity thay `records` JSONB dùng chung (khi @ ~10M row/entity), 3
  tier storage suy từ cờ metadata (`indexed/unique/searchable`), reconciler DDL level-triggered
  (idempotent, tự lành sau crash — DDL online không rollback được), migration declarative-only
  (eager cho field indexed, lazy cho field display-only), quarantine cho data bẩn, orchestrator
  fan-out multi-tenant (pull-based, canary→wave rollout). §3-§7.
- **Capabilities phái sinh**: audit/history (diff mode, opt-in per-entity), aggregation/rollup
  (permission pushdown vào WHERE), inbound integration (idempotency gate + raw store trước khi
  xử). §8.
- **FE onboarding**: `<MetapApp>` shell đọc `AppManifest` từ pack, tự dựng nav/routes — thay cho
  việc ráp tay từng dự án như `apps/crm-fe/App.tsx` hiện nay. §9.
- **Deployment SaaS**: PgBouncer transaction-mode bắt buộc khi scale ngang (connection budget
  nhân theo số instance), reconcile-orchestrator + cron tách khỏi request-serving instance
  (singleton/worker riêng), HA cho control-plane + Vault (SPOF). §11.

**Đừng over-build trước khi có trigger** (nguyên văn từ thiết kế gốc): three-way merge pack,
Kafka, pack registry/CDN, scripting/plugin runtime per-tenant, zero-downtime cutover phức tạp hơn
expand-contract 2 tầng đã mô tả.

Findings nhỏ từ cùng đợt review đã tách sang mục tiêu Phase 8 (TS strict, `opt-level`, clippy/
rustfmt gate, JWT `aud`/`iss`, `.gitignore` cho `settings.local.json`) vì không phụ thuộc trigger
SaaS — làm được ngay, không cần đợi Phase 16 bắt đầu.

## Phase 17: Metadata-driven Workflow Engine

**Trạng thái: Increment 1 đã xong (2026-08-21).** Trigger đi theo yêu cầu trực tiếp của chủ dự án
("ưu tiên cực cao"), sau khi review thấy `metap-cron` (Phase 13) đã có sẵn ~70% hạ tầng cần thiết
(claim-safe polling, outbox dispatch, `EventBus::subscribe`). Quyết định kiến trúc: tiến hoá
`metap-cron`/`cron-scheduler` tại chỗ, không tách crate `metap-orchestration` mới — chi tiết đầy
đủ + trade-off so sánh ở `docs/features/02-workflow-engine.md` và
[09. Architecture Decisions](architectures/09-adr.md).

**Increment 1 — trigger "on state transition"**: `cron_jobs` giờ có `triggerType`
(`schedule`/`on_transition`) + `triggerConfig` (`{entity, action}`), cùng bảng, cùng
`targetType`/`targetConfig`/`dispatchMode` như job lịch cũ. `cron-scheduler` thêm một consumer
mới (`trigger.rs`) subscribe `#.workflow.transitioned` — khi một transition thật khớp
`(tenant, entity, action)` của một job `on_transition` đang enable, job đó tự dispatch qua đúng
cơ chế outbox/direct đã có, không cần polling. Kèm retry-with-backoff
(`maxAttempts`/`retryBackoffSeconds`, backoff nhân đôi mỗi lần thất bại, mặc định 1 lần thử = giữ
nguyên hành vi cũ) cho mọi job (cả `schedule` lẫn `on_transition`) — đóng gap đã ghi từ Phase 5
("chỉ ghi `status: failed` rồi bỏ đó").

**Đổi kèm ngoài scope gốc, bắt buộc để trigger hoạt động đúng multi-tenant**:
`metap_workflow::emit_transitioned` giờ nhận thêm `tenant_id`, ghi vào payload outbox — trước đó
payload `<entity>.workflow.transitioned` không mang tenant, nên không consumer nào subscribe được
event đó mà biết chắc nó thuộc tenant nào.

**Verify sống qua HTTP + RabbitMQ + Postgres thật** (không phải suy đoán): tạo record C (draft) +
job `on_transition` khi `crm.customers` bị `block` → target `workflow_transition` activate C; tạo
record B, activate B (không khớp trigger, không dispatch gì) rồi block B (khớp trigger) → log
`cron-scheduler` ghi "cron job triggered on transition" rồi "cron job executed" → record C
`draft` → `active` thành công, `cron_job_runs` ghi `status: "success"`. Cộng 6 e2e test mới
(`crates/metap-cron/tests/cron_store_postgres.rs`): match đúng entity/action, không match sai
action, không rò cross-tenant, direct-mode job trả về đúng, retry-with-backoff lên lịch đúng +
`claim_due_retries` chỉ claim khi tới hạn, hết `maxAttempts` thì không retry nữa.

Migration: `crates/migrations/0015_cron_jobs_trigger_and_retry.sql`.

Còn lại — Increment 2 (chuỗi activity tuần tự, bảng `workflow_runs` mới) và Increment 3
(`wait_event`, durable pause) — vẫn ở trạng thái approved, chưa code, chờ Increment 1 chạy thật
lộ ra nhu cầu cụ thể trước khi thiết kế tiếp (trigger-based, không suy đoán trước). Admin UI cho
`triggerType: on_transition` không nằm trong phạm vi Increment 1 (backend track, FE track khác lo
riêng).

**Fix (2026-08-22, phát hiện khi nghiên cứu Organization & Identity × table-per-entity, độc lập
với cả hai — không phải một phần của feature brief `03-organization-identity.md`, vẫn `proposed`):**
`CrudService::delete()` (`crates/metap-crud/src/crud_service.rs`) trước đây không quét record nào
đang tham chiếu tới record bị xoá qua field `Reference` — xoá để lại orphan reference âm thầm,
không lỗi, không chặn. Giờ quét toàn registry tìm mọi field `Reference` trỏ tới entity đang xoá
(kể cả self-reference), chặn `409 record_referenced` nếu còn record tham chiếu, trong cùng
transaction với chính lệnh xoá — Restrict mặc định, chưa có per-field override. Verify sống qua
HTTP thật (`crm.customers.referredBy`): xoá A khi B còn trỏ tới → 409, A vẫn nguyên; xoá B trước
rồi xoá A → 200. 2 e2e test mới (`crates/metap-crud/tests/crud_service_postgres.rs`).

**Fix (2026-08-22, gap đầu tiên đã ghi trong `docs/features/01-fe-platform-overhaul.md`, tự bản
thân feature đó vẫn `proposed`/chưa scope xong):** `plan_list`
(`crates/metap-query/src/query_planner.rs`) trước đây luôn dùng `entity.list_views.first()` —
list view thứ hai của một entity (vd `accounting.journal`'s `ledger`) không gọi được qua API list
dù đã khai báo đầy đủ trong metadata. Thêm `ListInput.list_view: Option<String>` +
`GET /api/:entity?listView=<name>` (`crates/metap-http/src/routes/records.rs`); không truyền giữ
nguyên hành vi cũ, tên không tồn tại trả `400 unknown_list_view` (không âm thầm fallback về view
mặc định). 2 e2e test mới (`crates/metap-query/tests/query_planner_postgres.rs`), verify sống
qua HTTP thật trên `accounting.journal`'s `ledger`. Xoá hàng risk tương ứng ở
`docs/architectures/11-risks.md` (đã resolve).

**Fix + benchmark thật (2026-08-22, trigger: chủ dự án yêu cầu bằng chứng số liệu trước khi bắt
đầu xây một app thật — dạng Jira — trên bảng `records` chung).** Hai phần:

1. **Fix gap `search_mode: "substring"` không có index** — xác nhận bằng code: `ILIKE
   '%value%'` (`condition_to_sql.rs`/`query_planner.rs`) trước đây không có index nào hỗ trợ
   (không `pg_trgm` ở đâu trong repo) — mọi filter substring là quét tuần tự trong phạm vi
   `(tenant_id, entity)`. Thêm `crates/migrations/0016_pg_trgm_extension.sql`
   (`CREATE EXTENSION pg_trgm`) + `IndexReconciler::ensure_trgm_index`
   (`crates/metap-peripherals/src/index_reconciler.rs`) — GIN trigram index, cùng field-expression
   phải khớp chính xác với `QueryPlanner` (nguyên tắc đã có từ index thường/FTS). 1 e2e test mới
   xác nhận Postgres planner thực sự chọn index này cho đúng dạng `ILIKE` được sinh ra
   (`crates/metap-peripherals/tests/peripherals_postgres.rs`).
   - **Phát hiện phụ, đã ghi vào risk row** (`docs/architectures/11-risks.md`): hai lệnh
     `CREATE INDEX CONCURRENTLY` đồng thời trên cùng bảng `records` từ hai session khác nhau
     **deadlock thật** — tái hiện trực tiếp qua `psql`. Không phải rủi ro trong một process
     (`reconcile_inner` tuần tự), nhưng là rủi ro thật khi ≥2 instance `crm-server` cùng boot một
     lúc (rolling deploy/scale ngang) và cùng cần build index mới — chưa fix (cần
     `pg_advisory_lock`), chỉ mới ghi nhận.
2. **Benchmark thật ở 500K record** (không phải 200 record như load test cũ) — seed thẳng qua SQL
   (`generate_series` + `jsonb_build_object`, ~500K record entity `bench.issues` mô phỏng Jira:
   `title`/`status` (indexed)/`assignee` (indexed)/`description` (fts)/`priority`), `VACUUM
   ANALYZE`, đo qua `EXPLAIN (ANALYZE, BUFFERS)` **và** qua HTTP thật trên `crm-server` đang chạy.
   Kết quả (debug build, một máy dev, không phải production):
   - Filter chính xác trên field `indexed` (status) + sort + limit 100: **~1.5ms** (SQL), **10-49ms**
     (HTTP end-to-end, warm request ~10-20ms).
   - Substring search (title, `ILIKE`) — **trước fix** (không trigram index): Parallel Seq Scan,
     **177ms**. **Sau fix** (trigram index): Bitmap Index Scan, **~35ms** (SQL), **22-29ms**
     (HTTP) — ~5x nhanh hơn, đo trực tiếp bằng cách drop rồi tạo lại index trên cùng dataset.
   - Full-text search (description, độ chọn lọc thật ~2%): **~32ms** (SQL), **27-30ms** (HTTP).
   - Ghi record mới (`POST`) với 4 index đang phải maintain trên 500K row nền: **9-15ms**.
   
   **Kết luận**: ở quy mô 500K row/entity — cao hơn nhiều so với năm đầu vận hành thật của một
   app kiểu Jira nội bộ — bảng `records` chung + JSONB expression index (không cần table-per-entity,
   không cần generated column/cột thật) cho latency dưới 50ms cho mọi dạng query thực tế
   (exact/substring/FTS/sort/write). Không phải bằng chứng "không bao giờ cần table-per-entity" —
   trigger `@10M/entity` (`docs/architectures/09-adr.md`) giữ nguyên, đây chỉ là bằng chứng số liệu
   rằng **500K không phải ngưỡng đáng lo**, đủ để bắt đầu xây một app thật mà không cần chờ
   table-per-entity trước. Benchmark chưa test: concurrent load (nhiều client cùng lúc), release
   build, deep keyset pagination xa trang đầu, quy mô >1M row.

**Đóng gói benchmark thành tooling tái sử dụng được (2026-08-22)** — benchmark thủ công ở trên
giờ là 2 script commit vào repo: `apps/crm-server/scripts/seed-bulk.sh` (seed 300K+ row qua SQL
trực tiếp, nhanh hơn nhiều bậc so với seed qua HTTP của `load-test.sh`) và `bench-queries.sh`
(chạy 4 dạng query thật, báo cáo cả `EXPLAIN ANALYZE` lẫn latency HTTP). Cộng một stack
Grafana/Prometheus **opt-in** (`docker compose --profile observability up -d`, KHÔNG thuộc stack
dev mặc định) xem tài nguyên Postgres realtime lúc benchmark (connections, cache hit ratio,
transactions/sec, deadlock, temp file spill) — dashboard tự động provision, không setup tay.
`pg_stat_statements` bật sẵn trên service `postgres` cho truy vấn per-query trực tiếp qua `psql`.
Verify sống: chạy cả 2 script thật ở 100K row, xác nhận đúng index được chọn (trigram/GIN/B-tree)
và pipeline metric Postgres → exporter → Prometheus → Grafana dashboard hoạt động end-to-end
(kiểm từng metric name dùng trong dashboard JSON tồn tại thật qua Prometheus API, không suy đoán).

**Thêm metric cho chính `crm-server` (2026-08-22)** — ban đầu chỉ đo tài nguyên Postgres, thiếu
phần BE. `GET /metrics` mới (`crates/metap-http/src/metrics.rs`+`routes/metrics.rs`, public,
cùng quy ước `/health`) — request-level (`axum-prometheus`: count/duration/in-flight theo từng
route) + process-level (`metrics-process`: CPU/RSS/fd/thread). Một vấn đề thật gặp phải và đã xử
lý: `PrometheusMetricLayer::pair()` cài global `metrics` recorder — gọi 2 lần trong cùng process
sẽ panic, đúng kịch bản 3 test e2e trong `crates/metap-http/tests/http_server.rs` mỗi test tự
gọi `build_router` riêng — sửa bằng `OnceLock` guard (`prometheus_handle()`), verify bằng cách
chạy cả 4 test e2e cùng lúc, không panic. Prometheus thêm scrape target `crm-server` (chạy trên
host qua `pnpm dev:rs`, cần `extra_hosts: host-gateway` để container Prometheus reach host trên
Linux). Dashboard thứ 2 "Metap — crm-server Resource Metrics" — verify sống mọi metric name qua
Prometheus API trước khi đưa vào dashboard JSON (phát hiện `postgres-exporter` tự nó cũng expose
metric tên `process_*` giống hệt — phải lọc `job="crm-server"` trong mọi panel để không lẫn).
FE (`crm-fe`) cố tình không đo — không phải service dài hạn có resource để scrape theo kiểu này.
Chỉ `crm-server`, chưa làm cho `outbox-publisher`/`notification-worker`/`cron-scheduler` (không
có HTTP server để gắn `/metrics` vào — chưa có trigger cho việc riêng đo các worker đó).

Chi tiết đầy đủ ở `docs/local-benchmarking.md`.

**Benchmark nghiệp vụ phức tạp thật, 10 phút, đồng thời (2026-08-23) — chủ dự án chỉ ra đúng:
benchmark `pgbench` ở trên vẫn chỉ là MỘT bảng, filter đơn giản, không phản ánh nghiệp vụ thật
(multi-entity, hydration, ABAC).** Dựng schema quan hệ thật qua chính low-code builder:
`hr.departments` (20 row) → `hr.employees` (200 row, `departmentId` Reference) →
`helpdesk.tickets` (500K row, `assigneeId`+`departmentId` Reference, có `refDisplayField` cho cả
hai, có workflow 3 transition), cộng policy ABAC record-level theo phòng ban
(`fromContext.departmentId`) — đúng pattern Organization & Identity (Phase 18). Chạy
`CrudService::list()` **trực tiếp** (không qua HTTP — bỏ qua rate limiter theo IP, thứ không
phản ánh năng lực xử lý nghiệp vụ, chỉ là giới hạn tầng HTTP riêng), 20 worker đồng thời, 600
giây liên tục. Mỗi lần gọi `list()` ở đây trả giá đúng: context permission check + load record
policy snapshot + base query có điều kiện ABAC + `hydrate_related_display` cho **cả hai** field
Reference (mỗi field lại có permission check + batch fetch + record-condition check riêng) —
không phải một xấp xỉ, đúng cost shape thật của một list view org-scoped.

**Kết quả**: 448,399 lệnh gọi `list()` thành công / 600s, **747.3 list()/s trung bình, 0% lỗi**,
latency ổn định suốt 10 phút (không suy giảm theo thời gian) — p50=26ms, p95=31ms, p99=34ms,
max=120ms. Cache hit ratio **100%** trong suốt cửa sổ đo (B-tree index nhỏ cho
`assigneeId`/`departmentId`, khác hẳn 2 GIN index 47-48MB của benchmark đơn bảng trước — vừa đủ
trong `shared_buffers` mặc định 128MB, không cần tune). 0 deadlock mới, 0 temp file spill.

**Kết luận**: nghiệp vụ multi-entity thật (2 Reference field hydrate + ABAC record-level + workflow)
**không phải điểm nghẽn** ở quy mô 500K ticket/200 employee/20 department — nhanh hơn nhiều so
với lo ngại ban đầu, kể cả với ~5-9 query/lệnh `list()` (permission + snapshot + base + 2×
hydration). Test tool: `crates/metap-crud/tests/crud_service_postgres.rs`'s
`sustained_concurrent_list_against_a_real_multi_entity_abac_workflow` (`#[ignore]`d, cần seed
out-of-band trước — không chạy trong e2e suite thường).

**Mở rộng 10M row / 10 tenant / 3 Reference field, 10 phút, đồng thời (2026-08-23) — chủ dự án hỏi
tiếp: "seed 10tr bản ghi xem còn k, entity khác nhau tenant khác nhau lookup dữ liệu join bảng
nhiều thì sao".** Seed 10 tenant độc lập, mỗi tenant 20 `hr.departments` → 200 `hr.employees` →
1,000,000 `helpdesk.tickets` (10,000,000 ticket tổng — đúng vào ngưỡng `@10M/entity` đã ghim ở
Data Model Strategy/ADR cho table-per-entity), cùng chia sẻ **một** bảng `records` vật lý —
đúng kịch bản ngưỡng đó nói tới. `helpdesk.tickets` thêm field Reference thứ 3 (`reporterId` →
`hr.employees`, cạnh `assigneeId`/`departmentId` đã có) để `hydrate_related_display` chạy 3 lượt
permission-check + batch-fetch + record-condition thay vì 2 — gần hơn với "join nhiều bảng" thật.
Policy ABAC theo phòng ban được tạo lại cho **cả 10 tenant** (context-role grant + record
condition `fromContext.departmentId`, insert thẳng vào `policies` cho nhanh thay vì lặp HTTP). 20
worker đồng thời, mỗi lần lặp chọn ngẫu nhiên một cặp `(tenant_id, departmentId)` trong số 200 cặp
trải trên 10 tenant — traffic thật sự trộn tenant, không phải một tenant lặp lại.

**Kết quả**: 65,756 lệnh gọi `list()` thành công / 600s, **109.5 list()/s trung bình, 0% lỗi**,
nhưng latency suy giảm rõ — p50=57ms, **p95=1306ms, p99=1479ms, max=2140ms** (so với p95=31ms/
p99=34ms ở benchmark 500K/1 tenant). Throughput giảm ~6.8 lần. Cache hit ratio Postgres tụt còn
92.4-94.75% (so với 100% ở benchmark 500K) với ~149,000 block read/s — **root cause giống hệt
benchmark `pgbench` đơn bảng trước đó: `shared_buffers` mặc định 128MB quá nhỏ**, lần này còn rõ
hơn vì working set đã lớn hẳn (`pg_total_relation_size('records')` = 6.5GB, so với ~600MB ở
benchmark trước) — index/dữ liệu bị đẩy ra khỏi buffer cache liên tục dưới tải đồng thời 10
tenant. Đã loại trừ nguyên nhân khác: `ANALYZE` đã chạy tự động sau seed (`last_autoanalyze` mới
hơn thời điểm seed xong, `n_live_tup` khớp số dòng thật — planner không dùng statistics cũ), 0
deadlock mới, không có `crm-server` nào chạy song song gây nhiễu (test gọi thẳng `CrudService`,
xác nhận qua Prometheus target `crm-server` ở trạng thái down trong lúc benchmark). `EXPLAIN
ANALYZE` một truy vấn mẫu cho thấy planner ưu tiên index theo `(tenant_id, entity, created_at)`
để tránh sort thay vì index riêng trên `departmentId` (chọn lọc thấp — mỗi phòng ban chỉ ~0.6%
dòng của tenant) — một composite index `(tenant_id, entity, departmentId, created_at)` sẽ tốt
hơn cho pattern filter+sort này, chưa làm, ghi lại làm việc tiếp theo nếu quy mô 10M/tenant trở
thành thật thay vì benchmark.

**Kết luận (bản đầu, trước verify)**: ngưỡng `@10M/entity` **có ý nghĩa thật, không phải con số
suy diễn** — cùng cấu hình Postgres mặc định (`shared_buffers=128MB`) từng đủ dùng ở 500K giờ đã
là điểm nghẽn rõ rệt. Multi-tenant tự nó (10 tenant chia sẻ 1 bảng) **không** làm chậm thêm — mỗi
tenant vẫn lọc đúng bằng `tenant_id`. 3 Reference field hydrate (thay vì 2) cũng không phải
nguyên nhân chính. Test tool: `crates/metap-crud/tests/crud_service_postgres.rs`'s
`sustained_concurrent_list_across_many_tenants_at_ten_million_rows` (`#[ignore]`d, cần seed
out-of-band 10 tenant/10M row — không chạy trong e2e suite thường, dữ liệu test đã được dọn sau
khi đo).

**Verify fix thật, không dừng ở suy luận (2026-08-23, trigger: chủ dự án phản bác đúng — "hiệu
năng k tốt r, bài test db quá ít logic, chỉ mới datastore mà đã mất hơn 1s", tức là kết luận "chỉ
cần tune, chưa cần table-per-entity" phải được **verify bằng đo lại**, không phải để nguyên như
một giả thuyết chưa kiểm chứng).** Áp 2 fix đã nêu: `shared_buffers` 128MB → **2GB** +
`effective_cache_size` → 4GB (`docker-compose.yml`, không còn revert như benchmark `pgbench`
trước — bake thẳng vào compose vì lần này có dự định giữ lại), và composite index
`(tenant_id, departmentId, created_at DESC)` `WHERE entity='helpdesk.tickets' AND deleted=false`
(`idx_records_helpdesk_tickets_dept_created`, `CREATE INDEX CONCURRENTLY`). Seed lại đúng bộ 10
tenant/10M ticket, chạy lại đúng bài test 10 phút/20 worker.

**Kết quả sau fix**: 179,549 lệnh gọi `list()` / 600s, **299.2 list()/s** (109.5 → 299.2, ~2.7
lần), **p50=66ms, p95=78ms, p99=91ms, max=190ms** (so với p95=1306ms/p99=1479ms/max=2140ms trước
fix — **giảm ~17 lần** ở p95/p99), 0 lỗi. Cache hit ratio Postgres trong suốt cửa sổ đo:
**99.35%** (so với 92.4-94.75% trước fix). `EXPLAIN (ANALYZE, BUFFERS)` trên đúng truy vấn mẫu
(department-scoped, sort theo `created_at`) xác nhận planner giờ dùng
`idx_records_helpdesk_tickets_dept_created` thay vì né sort qua `records_tenant_entity_created_idx`
như trước — 0.448ms execution, 55 buffer hit, toàn bộ từ cache, 0 đọc đĩa.

Một điểm cần nói thẳng: `pg_total_relation_size('records')` ở lần đo này là **13GB** (bảng + toàn
bộ index, kể cả GIN trigram/description áp cho `helpdesk.tickets` — lớn hơn ước tính 6.5GB ban
đầu vì tính thiếu các GIN index đó), trong khi host dev chỉ có **~9.7GB RAM tổng**. `shared_buffers
2GB` vẫn nhỏ hơn nhiều so với working set thật 13GB — kết quả tốt hơn hẳn không phải vì đã cache
toàn bộ working set, mà vì phần **thực sự được truy cập** (composite index cho truy vấn department
+ B-tree cho Reference field) đủ nhỏ để nằm gọn trong 2GB, còn phần ít dùng (GIN trigram cho search
tự do) không cần nằm trong cache cho pattern truy cập của bài test này.

**Kết luận đã verify**: fix ở tầng Postgres (shared_buffers + composite index) **thật sự giải
quyết được vấn đề đo được** ở quy mô 10M/10-tenant hiện tại — không còn là suy luận từ Prometheus,
mà là số đo trước/sau cụ thể. Nhưng điều đó **không có nghĩa table-per-entity không cần làm** —
nó có nghĩa: ở quy mô 10M **hiện tại**, chưa cần, và khi cần thì đã biết chính xác 2 đòn bẩy nào
tác động lớn nhất. Bản thân việc `pg_total_relation_size` đã lên 13GB chỉ với 1 entity 10M-row —
trên một host dev 9.7GB RAM — là tín hiệu thật của đúng vấn đề mà `docs/multi-tenant-platform-
design.md` §3.1 mô tả: "N entity × 10M trong một bảng chung = 100M+ → index phình, autovacuum ác
mộng, một entity nặng làm chậm cả hệ" — tune RAM/index cho **một** entity 10M là khả thi, nhưng
không scale tuyến tính khi có **nhiều** entity cùng ở quy mô đó cùng lúc (mỗi entity nặng lại đòi
thêm working set riêng, cộng dồn trên cùng một bảng vật lý). table-per-entity được nâng độ ưu
tiên: xem `docs/features/04-table-per-entity.md` (trạng thái cập nhật — trigger đã có bằng chứng
thực nghiệm, không còn thuần lý thuyết). Test tool:
`crates/metap-crud/tests/crud_service_postgres.rs`'s
`sustained_concurrent_list_across_many_tenants_at_ten_million_rows`, dữ liệu test đã được dọn sau
khi đo (10,002,200 record + 40 policy xoá đúng phạm vi 10 tenant test, tenant dev cố định
`00000000-0000-0000-0000-000000000001` không bị đụng — verify 2203 record/2 policy còn nguyên).

**Sustained load test thật, 10 phút, đồng thời (2026-08-23, trigger: benchmark trước đó chỉ đơn
luồng, vài request — chủ dự án yêu cầu test nhiều hơn, tối thiểu 10 phút).** `pgbench` (có sẵn
trong image `postgres:16-alpine`) chạy 5 kịch bản trộn theo trọng số (filter chính xác 30%,
substring 20%, FTS 20%, filter theo assignee 20%, ghi 10%) — **20 client đồng thời, 4 thread,
600 giây liên tục**, trên 1 triệu record `bench.issues` (build `--release`, không phải debug
như benchmark trước). Kết quả:

- **106,562 transaction / 600s, 177.6 TPS trung bình, 0% lỗi.**
- Filter chính xác (indexed): 8.9ms; filter assignee (indexed): 6.8ms; ghi: 7.0ms — không đổi
  nhiều so với benchmark đơn luồng trước.
- **Substring/FTS chậm hẳn dưới tải đồng thời: 270ms/269ms** (so với 30-35ms đơn luồng trước đó
  ở 500K row) — phát hiện thật mà benchmark đơn luồng trước **hoàn toàn bỏ sót**.

**Truy nguyên nguyên nhân bằng Prometheus** (không suy đoán): cache hit ratio trong lúc chạy chỉ
**95.68%**, ~69K block đọc/giây từ ngoài `shared_buffers`. `shared_buffers` mặc định của image
Postgres chỉ **128MB**, trong khi working set (bảng `records` 329MB + 2 GIN index 47-48MB mỗi
cái) đã ~600MB — không đủ chứa trong cache của Postgres dưới tải đồng thời. **Verify bằng thực
nghiệm**: tăng `shared_buffers` lên 1GB (`ALTER SYSTEM` + restart, không đụng
`docker-compose.yml`), chạy lại 90 giây cùng kịch bản — cache hit ratio lên **99.98%**, TPS
tổng gần **gấp đôi** (177.6 → 345.3), **FTS giảm 70%** (268ms → 81.5ms), substring giảm 36%
(270ms → 173.7ms). Đã revert `shared_buffers` về mặc định sau khi verify xong (không thay đổi
`docker-compose.yml` — đây là runtime tuning, để lại quyết định "có bake vào compose file
không" cho lúc thật sự cần).

Kiểm tra phụ trong lúc chạy: `pg_stat_database_deadlocks` tăng thêm **0** trong suốt 11 phút
(dùng `increase()`, không phải giá trị cộng dồn — giá trị cộng dồn là 10, dư âm từ lúc tái hiện
deadlock `CREATE INDEX CONCURRENTLY` thủ công trước đó trong phiên, không phải phát sinh mới);
`pg_stat_database_temp_bytes` tăng 0 (không có sort/hash tràn ra đĩa, loại trừ nguyên nhân
`work_mem`). 1,013,833 row đã dọn sau test.

**Kết luận cập nhật**: 500K-1M row/entity vẫn không cần table-per-entity — nhưng khác kết luận
benchmark đơn luồng trước, **`shared_buffers` mặc định của Postgres dev container là điểm nghẽn
thật dưới tải đồng thời thực tế**, không phải kiến trúc bảng chung/JSONB. Đáng làm trước khi
cân nhắc table-per-entity: tune `shared_buffers` (và các tham số bộ nhớ liên quan) cho target
production thật — rẻ hơn nhiều so với tách bảng, và benchmark này cho thấy tác động lớn hơn.

**Fix + capability mới (2026-08-22, trigger: benchmark ở trên chỉ chứng minh nhanh trong MỘT
entity — chủ dự án chỉ ra đúng: nghiệp vụ thật trải dài nhiều entity, list Issue kiểu Jira cần
hiển thị tên Project/Assignee, không chỉ ID thô).** Xác nhận bằng code: `QueryPlanner` không có
JOIN, và `CrudService::list()` không hề enrich field `Reference` (cơ chế enrich duy nhất có sẵn
chỉ chạy cho single-record, chỉ phục vụ permission condition, không lộ ra response). Phân tích
đầy đủ 3 hướng giải quyết (không loại trừ nhau) + implement hướng nhẹ nhất ở
`docs/features/05-cross-entity-relations.md`:

- **Mode 2 (done)**: `CrudService::hydrate_related_display` — batch-hydrate field `Reference`
  có khai báo `refDisplayField` sau khi `list()` filter/mask xong, một query `WHERE id = ANY($1)`
  mỗi entity liên quan cho cả trang (không phải mỗi row). `RecordDto` thêm `related_display`
  (additive, vắng mặt ở mọi response khác). 1 e2e test mới
  (`crates/metap-crud/tests/crud_service_postgres.rs`), verify sống qua HTTP thật (`demo.projects`/
  `demo.tickets` tạo qua low-code — `GET /api/demo.tickets` trả đúng `relatedDisplay.projectId`).
- **Mode 1 (denormalize lúc ghi) và Mode 3 (JOIN thật, cho filter/sort xuyên entity)** — ghi lại
  thiết kế sơ bộ + trigger cụ thể, chưa code. Chi tiết đầy đủ ở feature brief trên.

## Phase 18: Organization & Identity — P0 (2026-08-22)

`docs/features/03-organization-identity.md` P0 done — org structure vẫn không cần bảng lõi mới
(đúng thesis ban đầu: định nghĩa qua low-code builder như entity thường), gap thật duy nhất là
`RequestContext` chưa mang được attribute của caller ngoài role/tenant cho `PolicyCondition`'s
`fromContext`. Đóng gap đó:

- `RequestContext` (`crates/metap-permission/src/context.rs`) thêm
  `context_attributes: Option<serde_json::Map<String, Value>>`, `#[serde(flatten)]` — tự động
  lộ ra ở top-level của `to_value()`, `fromContext` đọc được ngay không cần sửa
  `evaluate_condition`.
- `AuthContext` (`crates/metap-http/src/auth.rs`) — opt-in qua `AUTH_CONTEXT_ENTITY` (tên một
  entity quy ước có field `userId`): sau khi role resolve xong, tra thêm record của caller trên
  entity đó, gán vào `context_attributes`. `None` (mặc định) là no-op tuyệt đối — không query
  thêm, không đổi hành vi cho deployment không bật tính năng.
- `context_attributes` **được cache** (`metap_http::cache::ContextAttributesCache`, mới, cùng
  mẫu `metap-control::RegistryCache`: `moka::future::Cache`, TTL `AUTH_CONTEXT_CACHE_TTL_SECONDS`
  mặc định 30s) — khác nguyên tắc "role không bao giờ cache" vì đây đọc một record nghiệp vụ
  thường, không phải role. Hai đường invalidate: đợi TTL, hoặc
  `POST /admin/users/{userId}/context/invalidate` (gate `AdminContext`) để ép hiệu lực ngay.
  Quyết định + đánh đổi ghi ở ADR mới (`docs/architectures/09-adr.md`).
- Verify: unit test `context_attributes` flatten đúng
  (`crates/metap-permission/src/context.rs`); e2e test tự động mới
  (`crates/metap-http/tests/http_server.rs`'s
  `auth_context_entity_enriches_org_scoped_policies_and_supports_explicit_cache_invalidation`) —
  dựng `test.profiles`/`test.tasks`, policy org-scoped qua `fromContext`, xác nhận 200/403 đúng
  theo phòng ban và hành vi cache-stale-rồi-invalidate. Verify sống thủ công thêm trên
  `crm-server` thật (`AUTH_CONTEXT_ENTITY=hr.employees`): `hr.departments`/`hr.employees` tạo qua
  chính low-code builder (`PUT /admin/lowcode/entities/{name}/draft` + `publish`), employee đọc
  record `hr.employees` cùng phòng ban → 200, khác phòng ban → 403. Toàn bộ e2e suite hiện có
  (không set `AUTH_CONTEXT_ENTITY`) chạy lại nguyên vẹn, không regression.

P1 (`hr.positions`/`hr.locations`, `managerId` self-reference + policy "chỉ manager trực tiếp"),
P2 (Legal Entity/Business Unit/Approval Authority...) vẫn `proposed`, chưa có trigger để code.

## Phase 19: Table-per-entity — Bước 1/5 (2026-08-23)

Trigger: benchmark 10M row/10 tenant (xem phần "Verify fix thật" ở trên) đo được
`pg_total_relation_size('records')` = 13GB chỉ với **một** entity 10M-row trên host 9.7GB RAM —
đúng cơ chế `docs/multi-tenant-platform-design.md` §3.1 cảnh báo ("N entity × 10M trong một bảng
chung = 100M+ → index phình"), dù tune Postgres giải quyết được cho quy mô một-entity hiện tại.
Chủ dự án chốt: bắt đầu implement thật thay vì tiếp tục chỉ giữ ở readiness brief
(`docs/features/04-table-per-entity.md`) — bước 1 trong 5 bước đã sequencing ở đó.

**Bước 1 — `FieldStorage`/tier suy từ cờ metadata (§3.2), done:**

- `crates/metap-metadata/src/entity.rs`: `FieldStorage` enum (`Native`/`Column`, override tường
  minh), `FieldStorageTier` enum (`Jsonb`/`GeneratedColumn`), `resolve_field_storage_tier` (suy
  tier từ `indexed`/`sortable`/`unique`/`searchable`, override thắng khi có), `field_kind_sql_type`
  (map `FieldKind` → SQL type đúng bảng §3.2: `Money`→`numeric(18,4)`, `Reference`/`Id`→`uuid`,
  ...). `EntityField` thêm field `storage: Option<FieldStorage>`, optional + `#[serde(default)]`
  — entity cũ (kể cả low-code đã lưu trong `low_code_entity_versions`) deserialize bình thường,
  không cần backfill.
- Metadata-only, đúng scope bước 1: chưa có reconciler nào tiêu thụ `FieldStorageTier`, bảng
  `records` dùng chung vẫn lưu mọi field trong `data jsonb` bất kể tier — bước này chỉ chuẩn bị
  input cho bước 2 (reconciler diff).
- Đồng bộ chỗ cần: `crates/metap-metadata/src/openapi.rs`'s hand-written `EntitySummary` schema
  (thêm property `storage`), `crates/metap-metadata/src/lib.rs`'s re-export. `metap::metadata::*`
  (facade) đã tự động thấy qua module re-export sẵn có, không cần sửa `metap::prelude` (tier
  resolution không phải thứ một boot sequence cần).
- 56 chỗ dựng `EntityField { .. }` bằng struct-literal trên toàn repo (entity demo, test) đều
  cần thêm `storage: None,` — sửa bằng script tự động rồi build lặp lại tới khi sạch lỗi biên
  dịch, xác nhận không có literal nào bị bỏ sót.
- Kiểm chứng: 11 unit test mới (`entity.rs`'s `storage_tier_tests` — derive từ từng cờ, override
  cả hai chiều, mapping SQL type, round-trip JSON camelCase, entity cũ thiếu key `storage`
  deserialize ra `None`). `cargo build/clippy --all-targets -D warnings/fmt --check` sạch,
  `cargo test --workspace` (unit, không cần DB) sạch, không regression.

**Dọn DB dev + regenerate FE types (2026-08-23, sau khi chủ dự án xác nhận)**: quy trình chuẩn
"sau khi đổi meta-model, chạy `pnpm dev:rs` + `pnpm --filter @metap/platform-react generate:types`"
(CLAUDE.md) từng bị chặn vì DB dev tồn đọng **608 entity rác** (540 draft + 608 version, khớp
đúng pattern `test.<kịch bản e2e>_<hex>` của 21 kịch bản trong `crates/metap-lowcode`'s test
suite + `teste`) trong `low_code_entity_versions`/`low_code_entity_drafts`. Đã xoá đúng phạm vi
đó, giữ nguyên 8 entity thật (`hr.departments`/`hr.employees` — Phase 18 P0,
`helpdesk.tickets`/`bench.issues`/`demo.projects`/`demo.tickets`/`ops.demo_a_fcbfb890`/
`ops.demo_b_3ad50e6c`). Regenerate lại sạch: `generated-types.ts` giờ chỉ +3 dòng (property
`storage`, xem trên).

**Bước 2/5 — Reconciler cho một entity, đầy đủ §5.1-§5.8 (2026-08-23), done.** Crate mới
`crates/metap-reconciler`: `reconcile(desired) = introspect(actual) → diff → plan → execute`.
`compile()` field không cờ → giữ JSONB; `indexed`/`sortable`/`unique` → expression index thẳng
trên `data` (không tạo cột, đúng §5.6); `searchable` → GIN (tsvector/trigram); `storage: column`
→ cột thật + trigger đồng bộ + backfill; **`Reference` field luôn cần cột thật khi có
`ref_entity`** bất kể cờ khác — phát hiện qua e2e, không có trong pseudocode gốc (FK không thể
trỏ vào JSONB path). `diff()` đúng 5 bước §5.4 + `topo_sort`; `normalize_expr` (§5.3) dò thực
nghiệm qua Postgres thật trước khi viết (không đoán) — Postgres tự thêm `::text` vào literal và
đổi số lớp ngoặc khi lưu index definition, đoán sai sẽ khiến reconcile lặp vô hạn không hội tụ.
`executor::execute` dùng advisory lock **ghim trên một connection riêng** — phát hiện bug thật
lúc code: `pg_try_advisory_lock` gắn theo connection/session, gọi qua `&PgPool` trần sẽ đổi
connection mỗi lần, lock không bảo vệ được gì. `backfill` (§5.7) checkpoint cùng transaction với
update, cờ `completed` để `diff()` phân biệt "đang backfill dở" (resume) với "đã xong" (không
làm lại) — chi tiết phải tự thiết kế thêm để `diff()` vẫn là hàm thuần so hai `PhysicalSchema`.
`watchdog` (§5.8) lease+heartbeat, requeue hoặc `error` sau 5 lần retry. Migration mới:
`crates/migrations/0017_reconciler_tables.sql`.

**Kiểm chứng bằng e2e thật trên Postgres dev**, không chỉ unit test:
`crates/metap-reconciler/tests/reconcile_postgres.rs`, quan trọng nhất
`reconcile_converges_to_zero_ops_on_a_second_pass` — thuộc tính đúng đắn cốt lõi của reconciler
level-triggered (reconcile lần 2 với desired không đổi phải ra 0 op). E2e bắt được và sửa 3 bug
thật trước khi merge: (1) thiếu một lớp ngoặc bọc ngoài cho biểu thức có cast trong
`CREATE INDEX` (`"syntax error near ::"`), (2) FK field chưa được promote thành cột thật trước
khi gắn constraint, (3) advisory lock không ghim connection. 29 unit test + 3 e2e test,
`cargo build/clippy --all-targets -D warnings/fmt --check` sạch toàn workspace, `cargo test
--workspace` không regression. **Chưa wire vào binary nào** — không boot sequence nào gọi
`reconcile()`, bảng `records` chung vẫn là nơi `CrudService` đọc/ghi thật; vẫn là bước 2/5, chưa
phải "table-per-entity chạy thật". Chi tiết đầy đủ:
`docs/features/04-table-per-entity.md`.

**Bước 3/5 — Migration declarative-only + preflight/quarantine (2026-08-23), done.**
`crates/metap-reconciler/src/migration.rs`: `MigrationOp` (`RenameField`/`AddField`/
`WidenType`/`DropField`/`RemoveEnum`, đúng tập op §4.2, mỗi op sinh một SQL cố định, không code
hook). Chỉ `WidenType` cần preflight thật (chỉ nó có thể fail trên data bẩn) — dùng
`pg_input_is_valid` (hàm built-in Postgres 16, kiểm tra cast an toàn không cần try/catch), xác
nhận qua Postgres thật trước khi dùng chứ không đoán. `QuarantinePolicy` (`Block` mặc định |
`Coerce{fallback}` | `Quarantine`, đúng §4.4). `quarantine.rs`: bảng `{table}_quarantine` đúng
schema §4.5; `quarantine_bad_rows` di chuyển **từng dòng một** để bắt riêng lỗi
`foreign_key_violation` và bỏ qua đúng dòng đang bị tham chiếu — nhờ vậy không cần tự dò đồ thị
FK liên-entity, để Postgres (`ON DELETE RESTRICT` từ bước 2) tự chặn. Bug thật bắt được qua e2e:
CTE `batch` trong `backfill.rs` thiếu alias `t` trong khi `where_extra` của migration lại dùng
`t.data` → lỗi "missing FROM-clause entry" — chỉ lộ ra khi test transform thật có điều kiện WHERE
bổ sung. 5 e2e test (`tests/migration_postgres.rs`).

**Bước 4/5 — Orchestrator fan-out multi-tenant (2026-08-23), done.**
`crates/metap-reconciler/src/orchestrator.rs` + `crates/migrations/
0018_reconciler_orchestrator.sql` (`reconciler_entity_deployments`). `claim_due` đúng SQL
`FOR UPDATE SKIP LOCKED` §6.2; thêm `entity_name_filter` **so với thiết kế gốc** (hợp lý cho
production — sharding worker theo entity — và phát hiện cần thiết qua e2e: nhiều test chạy song
song trong cùng process tranh nhau một hàng đợi toàn cục, không phải lỗi SKIP LOCKED mà là thiếu
scope). `classify_error` (§6.4) đọc SQLSTATE thật từ error chain (không chỉ lớp lỗi ngoài cùng,
vì `executor`/`backfill` bọc lỗi qua nhiều tầng) → Transient (tự retry) / DataError / Fatal
(chuyển `blocked`, cần người). `run_claimed_batch` bound concurrency qua
`futures::stream::buffer_unordered`, cô lập lỗi từng entity đúng §6.4. `wave_size`/
`advance_wave` (canary 1-2 → 5% → 25% → 100%, halt nếu wave trước vượt ngưỡng error rate). Kiểm
chứng quan trọng nhất: 8 worker gọi `claim_due` đồng thời **thật** trên 40 dòng — không dòng nào
bị claim 2 lần, không dòng nào bị bỏ sót. 6 e2e test (`tests/orchestrator_postgres.rs`). Không
phải service chạy nền — giống `metap-cron` (thư viện) so với `cron-scheduler` (binary), một
binary orchestrator thật là việc riêng, ngoài phạm vi crate này.

**Bước 5/5 — Relations + FK thật (§3.3): thực chất đã done từ bước 2.** FK cấp DB cần một cột
vật lý thật để gắn vào — phát hiện lúc code bước 2 (không nằm trong kế hoạch ban đầu), nên
`compile()` đã phải tự suy ra field `Reference` có `ref_entity` luôn cần cột thật, độc lập với
`indexed`/`storage`. Phần còn lại (tắt fallback check ở `CrudService` cho entity đã tách bảng)
chưa làm — cần khái niệm "vị trí lưu trữ entity" chưa tồn tại, và vô nghĩa trước khi có entity
đầu tiên chạy thật qua table-per-entity.

**Trạng thái tổng**: cả 5/5 bước đã code + kiểm chứng bằng e2e trên Postgres dev thật (không
mock) — 31 unit test + 14 e2e test, `cargo build/clippy --all-targets -D warnings/fmt --check`
sạch toàn workspace, `cargo test --workspace` không regression. **Chưa wire vào bất kỳ binary
nào** — không entity sản phẩm nào thật sự chuyển sang table-per-entity, chưa có service
orchestrator chạy nền thật. Chi tiết đầy đủ: `docs/features/04-table-per-entity.md`.

## Định hướng chưa lên phase (chưa có trigger)

Tám ý nảy sinh từ thảo luận kiến trúc, hợp lý về sản phẩm nhưng chưa có trigger cụ thể nên chưa
được lên thành phase: workflow hai chế độ (in-process + cross-module), workflow
visualize/hướng BPM nhẹ, Tiny deployment profile (single binary, không RabbitMQ), migration
path generic-table-sang-bảng-riêng, computed/derived field, schema versioning cho entity, entity
variant kiểu polymorphic/discriminated-union, và metadata low-code theo từng Tenant (hướng dài
hạn cho Phase 11C's "quy tắc cô lập schema cấp Tenant", ghi lại 2026-08-22). Chi tiết và lý do
chưa lên phase ở `docs/team-charter.md`'s "Định hướng đang ghi nhận, chưa có trigger". Không bắt
đầu việc nào trong số này mà chưa có feature brief (`docs/features/`) nêu trigger cụ thể.


