## Phase 26: Làm đầy `apps/jira-server`/`apps/jira-fe` — auth thật, issue detail+comment, backlog, attachment (2026-08-24, đang làm)

Sau khi Phase 25 (tenant auth pluggable) xong, chủ dự án chọn 4 mảng để "làm đầy" jira demo tiếp,
theo đúng tinh thần "phải bắt đầu build thì mới biết metap cần thêm gì" đã chốt từ Phase 24. Plan
đầy đủ: xem plan file phiên làm việc lúc chốt (`/home/minhtuan/.claude/plans/
snazzy-squishing-phoenix.md` tại thời điểm viết).

**Bước 1/4 — Auth thật cho jira-fe, done (2026-08-24):**
- `apps/jira-fe/.env.example`/`.env` (mới) — `VITE_JIRA_TENANT_ID` (Vite expose `VITE_*` lúc
  build, cùng giá trị `apps/jira-server/.env`'s `JIRA_TENANT_ID` nhưng tách biến vì khác
  process/build). `apps/jira-fe/src/vite-env.d.ts` (mới) — khai báo type cho biến này (`types:
  ["vite/client"]` mặc định chỉ cho `string | boolean | undefined`, không khớp `LoginForm`'s
  `tenantId?: string`).
- `apps/jira-fe/src/demo/LoginPage.tsx` rút gọn còn `<LoginForm tenantId={...} />` — xoá hẳn
  `PasteTokenFallback` (không giữ "phòng khi", đúng quyết định trong plan).
- **Kiểm chứng sống**: tạo user test trực tiếp trên DB dedicated của tenant jira (`dev-tools
  create-user`, **phát hiện thật lúc verify**: chạy tay `dev-tools create-user` từ thư mục
  `apps/jira-server` vẫn ghi vào `DATABASE_URL` của `.env` đó — DB **platform chung**, không phải
  DB dedicated của tenant, vì lệnh này chỉ đọc `DATABASE_URL` qua `dotenvy::dotenv()`, không hề
  biết tới `Router`/`dsn_secret_ref` — phải override `DATABASE_URL=postgres://.../metap_myjira`
  tường minh mới ghi đúng chỗ; đã tạo nhầm 1 row ở DB chung rồi xoá, tạo lại đúng). Sau khi có user
  đúng chỗ: `curl POST /auth/login` với `tenantId` thật → 200, JWT đúng tenant — xác nhận gap cũ
  (`/auth/login` không tới được DB dedicated) **đã đóng thật khi có `tenantId`**, đúng như phân
  tích trong plan (code backend không đổi gì, chỉ là FE trước đây chưa từng gửi `tenantId`).
  `pnpm --filter @metap/jira-fe build` (tsc -b + vite build) sạch.
- **Gap mới phát hiện, ghi nhận (chưa fix, ngoài phạm vi bước này)**: `dev-tools create-user`/
  `seed-admin`/`mint-token` khi chạy cho 1 tenant `dedicated_db` đều chỉ dựa vào `DATABASE_URL` của
  `.env` hiện có trong thư mục chạy — không tự resolve qua `Router`/`dsn_secret_ref` như
  `provision-tenant` đã làm. Cùng họ với gap "mint-token mặc định tenant chưa provision" tìm được
  lúc demo trước đó (2026-08-24) — nguyên nhân gốc giống nhau: các subcommand CLI dev-only chưa
  từng được thiết kế tenant-aware đầy đủ.

**Bước 2/4 — Issue detail + comment UI, done (2026-08-24):**
- `apps/jira-fe/src/pages/IssueDetailPage.tsx` (mới) — **không viết lại field rendering tay**:
  compose thẳng `RecordDetail` (generic, đã có sẵn field/`WorkflowActionBar`/edit/delete cho
  `jira.issues` miễn phí) với 1 `CommentsPanel` mới bên dưới — chỉ phần comment mới thật sự thiếu
  UI, đúng phân tích trong plan. `CommentsPanel` dùng `useApiQuery`/`useApiMutation` đã có sẵn,
  không hook mới: list qua `GET /api/jira.comments?issue={id}&sort=-createdAt`, thêm qua
  `POST /api/jira.comments` body `{data: {issue, authorEmail, body}}` (đúng shape `GeneratedForm`
  đã dùng — không phải field phẳng).
- Route `/issues/{id}` (mới, song song `/records/jira.issues/{id}` cũ, không xoá) —
  `BoardPage`/`DashboardPage` đổi link sang route mới.
- **Kiểm chứng sống qua HTTP thật** (không đoán, đúng chính sách): tạo comment thật qua
  `POST /api/jira.comments` → filter `GET ...?issue={id}` trả đúng 2 comment (comment mới + 1
  comment seed cũ), filter theo issue khác trả rỗng — xác nhận `filters: ["issue"]` đã có sẵn
  trong `comment_entity.rs` hoạt động đúng như dự đoán trong plan, **không cần sửa backend gì
  cả**. `GET /api/jira.issues/{id}` xác nhận có field `capabilities` đúng shape `RecordDetail`
  cần. `pnpm --filter @metap/jira-fe build`/`lint` sạch.

**Bước 3/4 — Sprint backlog/planning view, done (2026-08-24):**
- **Bug thật tìm được đúng như plan dự đoán, đã sửa**: `?sprint=` (giá trị rỗng) trên field kiểu
  `Reference`/uuid gây **500** — Postgres reject `""::uuid` ("invalid input syntax for type
  uuid"). Root cause ở `crates/metap-query/src/query_planner.rs`'s nhánh equality-filter chung
  (dùng cho MỌI field, không riêng `sprint`) — bind giá trị rỗng thẳng vào rồi cast, không check
  trước. **Sửa tổng quát, không phải hack riêng cho jira**: giá trị filter rỗng giờ nghĩa là
  "field chưa gán" → sinh `{field_expr} IS NULL` thay vì cố cast — đúng ngữ nghĩa cho cả 2 kiểu
  lưu trữ (`data->>'field'` JSONB lẫn cột thật). Test mới
  `empty_filter_value_matches_unset_field` (`crates/metap-query/tests/query_planner_postgres.rs`)
  cover nhánh JSONB; nhánh uuid-cast verify sống qua `jira.issues.sprint` thật (500 → 200 đúng
  kết quả, filter theo sprint thật không đổi hành vi).
- `apps/jira-fe/src/pages/BacklogPage.tsx` (mới), route `/backlog` — cột "Backlog" (issue chưa
  gán sprint, dùng chính filter vừa sửa) + 1 cột/sprint (loại trừ sprint `completed`). Kéo-thả
  dùng lại đúng pattern native HTML5 DnD của `BoardPage`, nhưng khác `BoardPage` (gọi transition
  workflow): đây là **field update thường** — `PATCH /api/jira.issues/{id}` body
  `{version, data: {sprint: <id | null>}}`. Xác nhận sống qua `CrudService::update` **merge
  partial**, không phải full-replace (đọc code + verify curl: PATCH chỉ `{sprint: null}` giữ
  nguyên mọi field khác) — nếu là full-replace thì kéo-thả sẽ xoá sạch field khác của issue.
- **Kiểm chứng sống bổ sung**: `cargo test --workspace` (unit) sạch; e2e
  `crates/metap-crud/tests/crud_service_postgres.rs` chạy lại — 11/13 pass, 2 fail là do thiếu
  fixture 10M-row/`hr.departments` seed sẵn có trong môi trường dev hiện tại (không liên quan gì
  tới thay đổi lần này, pre-existing). `pnpm --filter @metap/jira-fe build`/`lint` sạch.

**Bước 4/4 — Attachment qua `metap-storage`, done (2026-08-24) — consumer thật đầu tiên của
`ObjectStore`:**
- Entity mới `jira.attachments` (`issue` Reference required, `filename`/`key`/`size`/
  `contentType`) — CRUD metadata (list/get/delete) chạy miễn phí qua `CrudService` như mọi
  entity khác, permission check/outbox/audit không cần code thêm.
- `crates/metap-http::AppState` thêm field `object_store: Option<Arc<dyn ObjectStore>>` (mặc
  định `None`, giống hệt pattern `auth_context_entity`) — không đổi hành vi `crm-server` (không
  set field này). `apps/jira-server/src/main.rs` set field này nếu `S3_BUCKET` có cấu hình (opt-in
  đúng convention `POLICY_CACHE_REDIS_URL`/`OUTBOX_WORKER_INLINE`).
- 2 route bespoke `apps/jira-server/src/attachment_routes.rs` (không phải `metap-http` chung —
  đúng ranh giới "no business-entity knowledge trong metap-* crate"): `POST
  /api/jira.issues/{id}/attachments` (multipart → `ObjectStore::put` → `CrudService::create` cho
  metadata; rollback xoá blob nếu bước metadata thất bại, tránh mồ côi) và `GET
  /api/jira.issues/{id}/attachments/{attachmentId}/download` (`CrudService::get` — permission
  check tự động — rồi `ObjectStore::get`, response `Content-Disposition: attachment` — không bao
  giờ `inline`, đúng cảnh báo XSS trong doc comment gốc của `metap-storage`).
- FE: `AttachmentsPanel` trong `IssueDetailPage` — upload/download qua `fetch` tay (không qua
  `apiFetch`/`useApiMutation`, 2 hàm đó luôn set `Content-Type: application/json` khi có body, sai
  cho multipart).
- **Kiểm chứng sống đầy đủ qua HTTP thật** (không mock): `docker compose up -d seaweedfs` +
  tạo bucket tay (`curl -X PUT`, S3 bucket provisioning là việc ops, không phải code app) → upload
  file thật → download lại **byte-for-byte giống hệt** (`diff` xác nhận) → xoá issue đang có
  attachment tham chiếu → **409 `record_referenced` tự động chặn đúng**, không cần code thêm gì
  (cùng `find_referencing_record` cơ chế đã có cho Reference field khác). `cargo build/fmt --check/
  clippy --workspace --all-targets -D warnings` + `cargo test --workspace` sạch. `pnpm --filter
  @metap/jira-fe build`/`lint` sạch.
- **Gap ghi nhận, chưa fix (ngoài phạm vi bước này)**: xoá metadata attachment (soft-delete) không
  tự xoá blob thật trong `ObjectStore` — object mồ côi ở SeaweedFS. `CrudService::delete` không
  biết gì về `ObjectStore` (đúng ranh giới hiện có), nên cần code riêng ở tầng bespoke route nếu
  muốn dọn — chưa làm vì chưa có yêu cầu cụ thể.

**Tổng Phase 26**: cả 4/4 bước done, verify sống đầy đủ (bao gồm 1 bug thật tìm+sửa ở
`metap-query`, dùng chung mọi entity). Diff chưa commit.

