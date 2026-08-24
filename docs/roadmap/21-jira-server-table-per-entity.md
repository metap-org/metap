## Phase 21: `apps/jira-server` — table-per-entity thật, lần đầu `reconcile()` chạy trong boot sequence (2026-08-23)

Quay lại việc bị pause trước Phase 20 (test kit): "bước 6" đã chốt — không chỉ dựng app jira mẫu,
mà sửa thật `CrudService`/`QueryPlanner` để một entity có thể dùng bảng riêng thay vì bảng
`records` chung, và gọi `metap_reconciler::reconcile()` từ một boot sequence thật lần đầu tiên.

**Khảo sát trước khi sửa xác nhận `EntityDefinition.table_name` chết hoàn toàn**: mọi entity
hiện có hardcode `table_name: "records".to_string()`, `CrudService`/`QueryPlanner` không bao giờ
đọc field này — luôn `FROM records` + filter `entity = $n`. `metap-reconciler` (đã "code-complete"
5 bước) sẵn sàng compile một `EntityDefinition` thành bảng riêng thật (`FRAMEWORK_COLUMNS` — cùng
hình dạng `records` trừ cột `entity`, cộng cột thật/FK/trigger-sync cho field `Reference` hoặc
`storage: Column`), nhưng chưa binary nào từng gọi `reconcile()`.

**Phát hiện quan trọng đổi nhẹ phạm vi**: `compile()` luôn build FK thẳng vào
`table_name_for(ref_entity)` bất kể entity đó có bảng riêng thật hay không (xác nhận bằng test có
sẵn `reference_field_gets_a_real_fk_once_both_entities_are_tables`) — nên quyết định cho **cả**
`jira.projects` lẫn `jira.issues` dùng bảng riêng (không chỉ Issue như thoả thuận gốc), để FK
`issue.project → jira_projects(id)` hợp lệ, đồng thời cho demo FK thật giữa 2 bảng riêng.

**Sửa core (không đổi hành vi entity cũ)**:
- `crates/metap-metadata/src/entity.rs`: `field_has_real_column()` (dùng chung bởi
  `metap-reconciler::compile` và `metap-query`, không lệch nhau về field nào có cột thật) +
  validate `table_name` phải là `"records"` hoặc khớp `^[a-z][a-z0-9_]*$` (nội suy thẳng vào SQL,
  Postgres không parameterize identifier được).
- `crates/metap-reconciler/src/compile.rs`: re-export `table_name_for`; **fix một bug thật lộ ra
  lúc build jira.issues** — `compile()` `bail!` khi field trùng tên cột framework, nhưng
  `code`/`status` LÀ những field một entity được kỳ vọng khai báo (workflow's `state_field`
  thường chính là `"status"`) — `CrudService` đã tự mirror 2 field này vào cột framework từ
  trước (`data.get("code")`, `get_initial_status`), độc lập với cơ chế trigger-sync của
  reconciler. Sửa: cho phép `code`/`status` đi qua (skip, không tạo cột trùng), mọi tên cột
  framework khác vẫn `bail!` như cũ.
- `crates/metap-query/src/query_planner.rs`: field có cột thật (Reference/`storage: Column`) trên
  bảng riêng dùng thẳng tên cột (có cast `::uuid` khi cần), field thường vẫn
  `jsonb_extract_path_text(data, ...)` như cũ dù trên bảng riêng (đa số field promoted chỉ được
  expression index, không phải cột thật — xem doc comment `compile.rs`); bỏ `entity = $n` khi
  bảng riêng.
- `crates/metap-crud/src/crud_service.rs`: mọi query (`list`/`create`/`update`/`transition`/
  `delete`/`fetch_existing`/`fetch_related_data`/`fetch_related_records_batch`) route theo
  `entity.table_name`, nhánh `records` giữ nguyên 100% logic/SQL cũ. `find_referencing_record`
  (guard tham chiếu trước khi xoá) viết lại: nhóm entity tham chiếu theo bảng vật lý của chính
  chúng, 1 query/bảng thay vì luôn gộp vào `records` — bắt buộc phải làm để chính MVP đúng (xoá
  `jira.projects` phải kiểm tra được bảng `jira_issues`).
- `condition_to_sql.rs` (ABAC field-level) **cố ý không đổi** — vẫn đúng vì `data` luôn là nguồn
  authoritative (trigger đồng bộ 2 chiều), chỉ mất lợi ích hiệu năng cột thật cho path này, không
  phải bug.

**`apps/jira-server`** (app mới, mirror boot sequence `apps/crm-server/src/main.rs`, bớt bước
merge low-code/lowcode_http/control_http/static-file/inline-worker vì PoC không cần):
`jira.projects` (key/name/description, không workflow) + `jira.issues` (title/description/
priority/`project` Reference thật/assigneeEmail/reporterEmail/status, workflow todo→in_progress→
done, cộng reopen). `assigneeEmail`/`reporterEmail` là text thường, không phải Reference — `users`
là bảng platform/auth, không phải `EntityDefinition` đã đăng ký.

**Sửa lỗi boundary thật sau khi chủ dự án review** (bản đầu tiên của phase này dùng tenant dev cố
định `00000000-...-0001` — cùng tenant `pnpm seed:admin` của `crm-server` — và `reconcile()` ghi
thẳng vào `pool` platform (`config.database_url`). Đúng như chủ dự án chỉ ra: nhầm lẫn "DB sandbox
của chính platform low-code" với "DB của một tenant khách hàng thật" — nếu KH đăng ký subscription
để build custom Jira riêng, họ phải là 1 tenant thật (`my-jira`) trỏ DB riêng, không phải dùng
chung DB/tenant dev của platform):
- `Router` (`crates/metap-control/src/router.rs`) trước đó chỉ có `begin()` (transaction-scoped) —
  không có cách nào lấy `PgPool` đã resolve theo tenant cho `reconcile()`'s DDL không-transaction.
  Thêm `Router::pool_for(tenant) -> anyhow::Result<PgPool>`, tách phần resolve
  status/strategy dùng chung với `begin()` qua hàm private `resolve()` (không đổi hành vi
  `begin()`, verify bằng `router_postgres.rs`'s 7 test + `tenant_isolation_postgres.rs` đều pass).
- Provision tenant **thật** qua đúng cơ chế multi-tenant sẵn có: tạo database Postgres riêng
  (`CREATE DATABASE metap_myjira`), `dev-tools provision-tenant <uuid> dedicated_db MY_JIRA_DSN
  postgres://.../metap_myjira admin@my-jira.example ...` (ghi `control.tenants` row thật, migrate
  toàn bộ schema nền tảng vào DB riêng, tạo admin user trên DB riêng đó).
- `apps/jira-server/src/main.rs`: build `Router` trước, đọc `JIRA_TENANT_ID` bắt buộc từ env (lỗi
  rõ ràng nếu chưa provision — không còn fallback "tenant chưa đăng ký = public schema" như
  `Router::begin` vẫn giữ cho dev flow cũ), gọi `router.pool_for(jira_tenant_id)` rồi mới
  `reconcile()` — bảng `jira_projects`/`jira_issues` giờ nằm đúng trong `metap_myjira`, không phải
  DB platform. **Verify sống**: dọn sạch 2 bảng jira còn sót lại trong DB platform từ lần chạy sai
  trước đó (bằng chứng cụ thể của đúng loại bug vừa sửa); chạy lại — `\dt` xác nhận
  `jira_projects`/`jira_issues` chỉ tồn tại trong `metap_myjira`, `metap` (platform) sạch; tạo
  Project qua HTTP thật với token mint cho user admin của `my-jira` — row landed đúng
  `metap_myjira`, không lẫn vào DB platform.
- **Giới hạn còn lại, ghi rõ trong doc comment `main.rs`, chưa sửa (ngoài phạm vi fix này)**:
  `AppState.pool` (login/`preferences`/cron routes) vẫn luôn dùng pool platform, chưa
  `Router`-resolve theo tenant — gap có sẵn từ trước ở `crm-server`, không phải do phase này gây
  ra, nhưng nghĩa là `/auth/login` cho user của tenant `DedicatedDb` chưa hoạt động (user tồn tại
  trên DB riêng, không phải DB platform); dùng `dev-tools mint-token` (chỉ cần keypair, không query
  DB) để xác minh thay cho `/auth/login` cho tới khi gap này được sửa riêng. `reconcile()` vẫn là
  gọi trực tiếp 1 tenant lúc boot, không phải orchestrator đa-tenant (`claim_due`/wave rollout vẫn
  chưa binary nào chạy) — tenant mới đăng ký sau khi process đã chạy sẽ không tự được reconcile.

**Kiểm chứng sống** (không chỉ đọc code suy luận): build release + chạy thật `jira-server` —
boot log xác nhận `reconcile()` tạo `jira_projects`/`jira_issues`; `\d jira_issues` qua psql xác
nhận cột `project` (uuid) thật + `FOREIGN KEY ... REFERENCES jira_projects(id) ON DELETE RESTRICT`
+ trigger `trg_sync_jira_issues_project`. Tạo Project + Issue thật qua HTTP: filter list theo
`project` (cột thật, có cast `::uuid`) trả đúng; Reference hydration (`relatedDisplay.project`)
đọc đúng từ bảng riêng khác; transition `todo→in_progress` qua `POST .../transitions/start` đúng;
xoá Project đang bị Issue tham chiếu → `409 record_referenced` đúng thông điệp; xoá Issue trước
rồi Project → cả 2 thành công. `cargo test --workspace` (57 suite) + e2e thật
(`crud_service_postgres`/`query_planner_postgres`/`reconcile_postgres`) không regression cho 4
entity `records`-table cũ của crm-server. `cargo fmt --check`/`clippy --workspace --all-targets
-D warnings` sạch.

**Chưa làm**: orchestrator đa-tenant thật (vẫn đúng như ghi nhận cũ, không phải việc phase này);
`jira.issues` chưa có endpoint/scenario nào trong bộ test kit (`testing/`) — có thể thêm sau nếu
cần benchmark bảng riêng so với `records`.

**Đổi tiền tố schema-per-tenant từ `tenant_*` sang `t_*`** (yêu cầu riêng, cùng đợt review): đổi
`Router::validate_schema_name`'s whitelist (`^tenant_[a-z0-9]+$` → `^t_[a-z0-9]+$`), cập nhật
đồng bộ `docs/multi-tenant-platform-design.md`'s ví dụ (`tenant_ab12` → `t_ab12`) và toàn bộ tên
schema dùng trong test (`router_postgres.rs`, `tenant_isolation_postgres.rs`). Không đụng những
chỗ `"tenant_"` chỉ là substring của tên khác không liên quan tới schema (`tenant_unavailable`,
`tenant_not_found` — mã lỗi API, không phải tên schema). Verify sống: cả 8 test liên quan
(`router_postgres.rs` 7 test + `tenant_isolation_postgres.rs`) pass qua Postgres thật với tiền tố
mới.

**Sửa boundary schema thật trong chính DB tenant** (chủ dự án review lần 2, sau khi đã có DB
riêng đúng: "trỏ DB mới có vẻ ok, nhưng nên chia schema... `control` -> thứ chỉ nên có trong
low-code platform"). Bên trong `metap_myjira`, mọi bảng — cả nền tảng (`users`/`policies`/
`records`/...) lẫn nghiệp vụ tenant (`jira_issues`/`jira_projects`) — đang nằm chung schema
`public`, và `control.tenants` (registry toàn cục, chỉ nên tồn tại đúng 1 nơi trong toàn hệ
thống) bị migrate lẫn vào mọi DB dedicated một cách vô nghĩa (verify sống: đúng là có, `\dn` xác
nhận schema `control` rỗng nằm trong `metap_myjira`).
- **`control.tenants` không còn bị migrate vào DB dedicated**: `crates/metap-control/src/
  provisioning.rs`'s `provision_dedicated_db_tenant` vẫn chạy `sqlx::migrate!` đầy đủ (không có
  cách chọn lọc migration nào trong 1 lời gọi macro), nhưng `DROP SCHEMA control CASCADE` ngay
  sau đó trên DB dedicated — `_sqlx_migrations` của DB đó vẫn ghi nhận đã áp dụng đúng, chỉ là
  schema không tồn tại (không migration nào khác đụng vào `control.tenants` nên an toàn).
  **Verify sống**: provision tenant throwaway mới hoàn toàn (`metap_verify_test`) — `\dn` chỉ còn
  `public`, không có `control`; dọn `control` schema thừa còn sót lại trong `metap_myjira` từ
  trước khi fix.
- **Bảng nghiệp vụ tenant (table-per-entity) chuyển sang schema `entities`, tách khỏi `public`**:
  `metap-reconciler` thêm `ENTITY_SCHEMA = "entities"` + `qualified_table_name_for()` (dùng thay
  `table_name_for()` cho `PhysicalSchema.table`/FK `ref_table`/`EntityDefinition.table_name`) —
  `table_name_for()` (bare, không schema) vẫn giữ nguyên riêng cho việc đặt tên index/trigger.
  Đụng tới toàn bộ nơi build SQL nhận diện bảng: `introspect.rs` (3 query hardcode `'public'`
  trước đó, giờ parse schema động từ chuỗi `schema.table`, kể cả introspect ngược `FkSpec.ref_table`
  qua join `pg_namespace` để không lệch với `compile()`'s desired state), `executor.rs`/
  `quarantine.rs`/`backfill.rs`/`migration.rs` (thêm `quote_qualified_ident` — `quote_ident` cũ
  quote cả chuỗi `"entities.jira_issues"` thành 1 identifier chứa dấu chấm, sai). `metap-metadata`'s
  validate `table_name` nới ra để chấp nhận dạng `schema.table` an toàn (mỗi đoạn vẫn phải khớp
  charset cũ). **2 bug thật phát hiện lúc verify sống, không phải suy đoán**:
  - `compile.rs`'s check "field trùng tên cột framework" từng chỉ áp dụng đúng cho `code`/`status`
    (đã fix ở lần verify trước) nhưng đó là bug riêng, không liên quan phần này.
  - `CREATE SCHEMA IF NOT EXISTS` **không an toàn dưới concurrency thật** — 2 lệnh `reconcile()`
    chạy song song (test suite mặc định chạy test song song) cùng thấy "schema chưa tồn tại" rồi
    cùng cố tạo, gây lỗi `23505` (`pg_namespace_nspname_index` unique violation) — không phải
    `42710` như tài liệu Postgres hay mô tả cho trường hợp không-concurrent. Fix: tách bước
    `ensure_schema_exists()` ra khỏi vòng lặp DDL chung, bắt cả `23505` lẫn `42710` và coi là
    thành công (đúng nghĩa `IF NOT EXISTS` muốn đảm bảo). Verify: chạy lại toàn bộ
    `metap-reconciler` e2e suite (chạy song song, đúng điều kiện gây race) 3 lần liên tiếp, sạch
    cả 3.
  - Test e2e cũ (`reconcile_postgres.rs`) tự dọn bảng bằng `DROP TABLE IF EXISTS "<bare_name>"`
    (không schema-qualify) — sau khi bảng chuyển sang `entities.*`, lệnh dọn này âm thầm no-op,
    để lại rác giữa các lần chạy. Đã sửa cùng lúc.
  - **Verify sống cuối cùng, đầy đủ vòng đời**: `metap_myjira` giờ chỉ còn 2 schema
    (`entities` chứa `jira_issues`/`jira_projects`, `public` chứa bảng nền tảng), FK
    `entities.jira_issues.project → entities.jira_projects(id)` đúng qua schema-qualified name;
    tạo Project qua HTTP thật, `CrudService` đọc/ghi đúng `entities.jira_projects`. Toàn bộ
    `cargo test --workspace` + e2e `metap-reconciler`/`metap-control`/`metap-crud` liên quan không
    regression.

