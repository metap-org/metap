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

**Đưa `/admin/lowcode/*` vào OpenAPI/FE codegen (2026-08-27).** Gap thật tìm được khi chủ dự án
chuẩn bị build UI admin cho low-code trên FE: `GET /metadata/openapi.json`
(`metap_metadata::generate_openapi_document`) từ trước tới giờ chỉ mô tả `/metadata/*` và
`/api/{entity}*` sinh động theo `MetadataRegistry` — toàn bộ `/admin/lowcode/*` (9 route: draft/
publish/preview/rollback/published/versions/audit/export/import), cộng `/platform/tenants*`
(`metap-control-http`) và các route tĩnh khác của `metap-http` (`auth`/`admin`/`cron`/
`dashboards`/`attachments`/`preferences`/`users`/`workflow-events`) chưa từng được khai báo, nên
`openapi-typescript` không sinh được type gì cho chúng — FE không thể bắt đầu build UI admin low-
code với type an toàn. Thêm `openapi_paths()` viết tay ở cả 3 crate (`metap-http`,
`metap-lowcode-http`, `metap-control-http`), đúng phong cách hand-written JSON Schema
`generate_openapi_document` đã dùng từ đầu (repo chưa có bước derive/reflection nào, xem doc
comment hàm đó) — cân nhắc `utoipa` nhưng bỏ vì phần lớn handler trả `Json(json!({...}))` ad-hoc,
không có struct request/response, nên derive sẽ đòi refactor lớn hơn giá trị mang lại lúc này.
`AppState` thêm field `extra_openapi_paths` để `metap-lowcode-http`/`metap-control-http` (2 crate
`metap-http` cố tình không phụ thuộc, xem đầu file mỗi crate) đóng góp path vào doc được
`GET /metadata/openapi.json` phục vụ mà không phá ranh giới phụ thuộc. 3 hàm schema builder
(`entity_field_json_schema`/`entity_list_view_json_schema`/`entity_workflow_json_schema`) đổi
thành `pub` để `metap-lowcode-http` tái dùng cho draft/publish/export/import body (cùng wire
shape `EntitySummary` dùng) thay vì viết lại. Tiện thể phát hiện + sửa luôn 1 gap khác không
liên quan low-code: `/api/{entity}/{id}` sinh động trước đó chỉ khai `PATCH`, thiếu hẳn `GET`/
`DELETE` dù `routes::records::router()` có đăng ký cả hai. Verify sống: build + chạy `crm-server`
thật trên Postgres local (Docker Hub bị chặn bởi network policy môi trường build, dựng Postgres
qua `apt` thay vì `docker compose`), số path trong `/metadata/openapi.json` từ 14 lên 54; `cargo
build/test/fmt/clippy -D warnings` sạch; `pnpm typecheck` sạch; `generated-types.ts` regenerate
lại từ server thật sau mỗi thay đổi. (`PR #4`, 3 commit: regenerate types cho Phase 43 +
mở rộng OpenAPI coverage + fix gap GET/DELETE.)

**Dọn CI trong lúc babysit `PR #4` (2026-08-27).** Không thuộc scope OpenAPI ở trên nhưng phát
hiện lúc theo dõi PR tới khi xanh hoàn toàn: (1) `clippy::explicit_counter_loop`/
`clippy::result_large_err` mới xuất hiện do runner CI resolve `dtolnay/rust-toolchain@stable` lên
Rust 1.98.0 (cache local trước đó là 1.94.1) — fix bằng cách bọc thêm 2 hàm nội bộ trả
`Result<_, Box<Response>>` (mẫu có sẵn ở `routes/dashboards.rs`) và đổi 1 vòng lặp thủ công thành
`.enumerate()`; (2) `aws-sdk-s3` kéo cùng lúc 2 stack HTTP client (một hiện đại qua
`default-https-client`, một cũ qua `rustls`/hyper-0.14/h2-0.3) do bật default features — advisory
thật từ `cargo audit`, fix bằng `default-features = false` + khai rõ feature cần; (3) `prettier`
lệch định dạng ở 7 file FE không liên quan PR này — chạy `prettier --write`; (4) `metap-cache`'s
`redis_cache` e2e test luôn fail "Connection refused" vì `rust-e2e` job chưa từng có Redis — thêm
service `redis:7-alpine`; (5) race thật: `provisioning_postgres.rs`'s
`provision_dedicated_db_tenant_migrates_and_creates_admin` tái dùng `DATABASE_URL` chung của CI
làm database "dedicated" giả (tradeoff có ghi chú từ trước), nhưng `provision_dedicated_db_tenant`
production code chạy `DROP SCHEMA control CASCADE` thật trên DB đó — vô hại khi chạy đơn lẻ,
nhưng crate e2e mới của Phase 44 (bên dưới) đẩy nhiều test chạy đồng thời hơn, lần đầu trúng race
`relation "control.tenants" does not exist` trên các test khác đang chạy song song. Fix bằng
database throwaway thật (tạo/dùng/xoá qua database `postgres` mặc định), sửa luôn assertion admin
user để query đúng pool "dedicated" mới thay vì pool chung (trước đó "đúng" chỉ vì tình cờ trỏ
chung 1 DB). (6) Gap lớn nhất, tồn tại từ lâu nhưng chưa ai thấy: `metap-control`'s `vault_store.rs`
(AppRole login/renew, đọc/ghi DSN qua Vault KV) chưa từng chạy được trong CI — không có Vault
service, nên luôn fail connection-refused. Không lộ ra trước đây vì `cargo test --workspace --
--ignored` không có `--no-fail-fast`: `metap-cache` (lỗi Redis ở trên) đứng trước `metap-control`
theo thứ tự alphabet nên cả run luôn dừng sớm, chưa bao giờ chạy tới `vault_store.rs`. Xác nhận có
thật trên `master` (không phải do PR này) bằng cách đọc log CI gần nhất của `master`: đúng y hệt,
dừng ngay sau khi `redis_cache` fail. Fix: thêm service `vault` (`hashicorp/vault:1.15`, dev mode,
root token cố định, cùng cấu hình `docker-compose.yml`'s `vault` service) cộng 1 step provision
bằng `curl`+`jq` (không cài Vault CLI riêng) — bật `approle` auth, ghi policy `metap-dsn-read`, tạo
3 role đúng TTL từng test cần (`metap-crm-server`/`metap-renew-test`/`metap-renew-single-use`),
generate `role_id`/`secret_id` rồi export qua `$GITHUB_ENV`. Không dựng được Vault thật trong sandbox
dev (Docker Hub bị chặn, không có Vault CLI qua `apt`) nên không chạy trực tiếp được — verify bằng
cách khác: parse `ci.yml` bằng `pyyaml` để chắc cấu trúc hợp lệ, `bash -n` cho step script, và giả
lập `curl`/`jq` cục bộ để xác nhận từng JSON payload gửi cho Vault API hợp lệ (bật auth, ghi policy,
tạo role, đọc `role_id`/`secret_id`) đúng escaping. `cargo fmt --all --check` sạch (không đổi code
Rust nào cho phần Vault, chỉ `.github/workflows/ci.yml`).

Vault service chạy thật trong CI xác nhận đúng thiết kế — `vault_store.rs` không còn lỗi
connection-refused, đúng như kỳ vọng — nhưng vì `cargo test` giờ chạy sâu hơn vào workspace
(qua khỏi `metap-cache`/`metap-control`), lộ ra tiếp (7) một race khác từng bị che dấu cùng lý do:
`metap-cron`'s `cron_store_postgres.rs` có 2 test assert đúng số lượng `claimed` từ
`claim_due_retries` — hàm này **cố ý** global/cross-tenant (ticker thật poll due-retry của mọi
tenant trong 1 query, giống `claim_due_jobs`), nên assert "claimed đúng 1" chỉ đúng nếu không có
row due nào của test khác lởn vởn cùng lúc; mặc định test harness chạy song song mọi
`#[tokio::test]` trong 1 file, nên không có gì ngăn 2 test này giẫm lên nhau. Xác nhận: `master`'s
log CI cũng dừng đúng tại `metap-cache` nên chưa từng chạy tới đây — cùng một kiểu che dấu domino
như (6) ở trên. Fix theo đúng mẫu có sẵn trong repo (`metap-peripherals/tests/peripherals_postgres.rs`'s
`INDEX_BUILD_LOCK`): thêm `static Mutex` giữ khoá suốt **toàn bộ thân test** (không chỉ quanh lệnh
gọi `claim_due_retries`, vì race nằm ở cấp độ row chứ không chỉ ở câu query) cho 2 test liên quan.
Verify sống: build lại toàn workspace sau khi dọn `target/` bị đầy đĩa giữa chừng (`cargo clean`,
`du -sh target` xuống 14G, dưới ngưỡng 40G CLAUDE.md cảnh báo), `cargo test -p metap-cron --
--ignored` chạy lặp lại nhiều lần đều pass ổn định, `cargo build/fmt --check` sạch.

Domino tiếp tục sang (8): sau khi qua khỏi `metap-cron`, `cargo test` chạy tới `metap-crud` và fail
2 test trong `crud_service_postgres.rs` — nhưng đây không phải bug, mà là gap thật trong chính
`ci.yml`: file này dùng `#[ignore]` cho 2 mục đích khác nhau — phần lớn là "e2e test cần
`DATABASE_URL`" (quy ước chuẩn toàn repo), nhưng 3 test cuối file là **manual benchmark**, tự ghi rõ
trong `ignore` reason của chính nó ("not part of a normal e2e run"): 2 test cần seed thật ngoài
băng thông (out-of-band) hàng triệu row/nhiều tenant CI chưa từng tạo, nên panic ngay
("hr.departments must already be seeded"); test thứ 3 tự chứa dữ liệu nên không fail nhưng vẫn tốn
~60s chạy sustained-load 20-worker mà kết quả (`eprintln!` số liệu) không hiện ra do CI không chạy
`--nocapture` — tốn CI time, không giá trị. `-- --ignored` quá thô để phân biệt 2 loại `#[ignore]`
này. Fix: thêm `--skip <tên test>` đích danh cho cả 3 test thay vì đổi ý nghĩa `--ignored` toàn cục.
Verify sống: `cargo test -p metap-crud --test crud_service_postgres -- --ignored --skip ... --skip
... --skip ...` chạy đúng 11 test e2e thật (3 filtered out đúng), `cargo fmt --all --check` sạch
(không đổi code Rust, chỉ `ci.yml`).

Còn lại của Phase C: **publish approval workflow** — chính spec gốc
(`docs/low-code-platform-v1.md`) ghi là "nếu cần", chưa có tín hiệu nhu cầu thật (mọi hành động
draft/publish hôm nay chỉ gate bởi `AdminContext` chung, chưa có nhu cầu tách vai trò soạn/duyệt).
**Quy tắc cô lập schema cấp tenant** — bị chặn bởi một quyết định kiến trúc lớn hơn nhiều (metadata
low-code hôm nay GLOBAL, không theo tenant, quyết định tường minh từ Phase A); hướng dài hạn đã
ghi lại ở `docs/team-charter.md`'s "Metadata low-code theo từng Tenant" (2026-08-22), chưa có
trigger để bắt đầu.

