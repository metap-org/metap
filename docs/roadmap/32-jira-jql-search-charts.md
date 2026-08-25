## Phase 32: Generic JQL query engine, advanced search, logwork report, dashboard charts (2026-08-25)

Chủ dự án chỉ ra jira-server chưa thật sự "done": search issue nâng cao, JQL, customize
dashboard, chart, view logwork — và nhấn mạnh đây là những năng lực **metap chắc chắn sẽ cần
chung**, không riêng jira. Thống nhất thứ tự: search nâng cao (build trên JQL full parser) → view
logwork → chart → dashboard tuỳ chỉnh (mục cuối để sau, xem "Còn lại" bên dưới).

- **`metap-query::jql`** (crate mới trong `metap-query`, không phải app nào) — query language nhỏ
  kiểu Jira: `field OP value` kết hợp `AND`/`OR`/`NOT`/ngoặc đơn, operator `= != > >= < <= ~ !~ IN
  NOT IN IS EMPTY IS NOT EMPTY`, `ORDER BY field [ASC|DESC]` (1 field — khớp giới hạn sort 1-cột
  toàn hệ thống, kể cả keyset cursor). Cùng chuẩn an toàn mọi filter path khác trong codebase:
  **tên field validate theo `entity.fields` thật** (không phải cột/JSON path tự do), **operator
  allowlist cố định theo `FieldKind`** (không cho `>` trên `String`, không cho `~` trên
  `Boolean`), **mọi value bind qua `ParamBuilder`** — không nội suy chuỗi vào SQL bao giờ. Lỗi cú
  pháp/field lạ/operator sai kiểu → `InvalidJqlError` → HTTP 400 `invalid_jql` với message người
  đọc được (downcast pattern giống `InvalidCursorError`/`UnknownListViewError`). 10 unit test
  thuần (không cần DB) cho lexer/parser/compiler.
- **`ListInput.jql`/`?jql=`** — wire vào **route generic** `/api/{entity}` (`records.rs`, không
  phải endpoint riêng cho jira) — `AND`ed cùng filter cấu trúc sẵn có (`?status=done&jql=...` kết
  hợp được), JQL's `ORDER BY` ưu tiên hơn `?sort=` khi cả 2 cùng có (giống JQL thật ưu tiên hơn
  quick-sort). Verify sống qua HTTP thật: equality/AND/OR/NOT/ngoặc, `IN`/`IS EMPTY`, so sánh
  khoảng trên field `Date` (cast `::date` đúng cả LHS/RHS), `~` (ILIKE) trên field text, `ORDER
  BY` field sortable đúng, và **đúng như thiết kế** — field không sortable/không tồn tại/operator
  sai kiểu đều trả `400 invalid_jql` rõ ràng, không phải 500 hay silent-ignore.
- **`AdvancedSearchPage`** (`jira-fe`) — ô nhập JQL + bảng kết quả, lỗi hiển thị trực tiếp message
  từ `invalid_jql` (đã là text người đọc được, không cần map lại).
- **`jira.worklogs` được đọc qua generic `GET /api/{entity}/{id}/workflow-events`** (route mới,
  Phase 31) — không liên quan trực tiếp phase này nhưng cùng `metap-query`/`metap-http` boundary.
- **`LogworkReportPage`** ("view logwork") — báo cáo time-tracking xuyên issue theo khoảng ngày,
  gom theo `authorEmail`, dogfood chính JQL vừa build (`workDate >= "..." AND workDate <= "..."
  ORDER BY workDate DESC`) — khác `WorklogsPanel` (Phase 31, theo từng issue).
- **`BarChart`** (component mới, **`packages/platform-react`**, không phải `jira-fe`) — bar chart
  SVG nhỏ gọn, không thêm dependency, đọc màu qua Mantine CSS variable nên tự theo theme app chủ
  (kể cả dark mode) thay vì tự mang bảng màu riêng. Nhận `{label, value, color?}[]` thuần — không
  biết gì về `jira.issues`. `DashboardPage`'s "By status"/"By priority" đổi từ stat-card sang chart
  thật, dùng lại đúng `PRIORITY_COLOR` mapping đã có (không phát minh bảng màu mới). Build/lint
  xác nhận không phá `crm-fe` (consumer thứ 2 của `platform-react`).

**Sự cố hạ tầng gặp phải giữa chừng (không liên quan code)**: container `postgres`/`rabbitmq`
tự thoát ~22 phút trước khi verify — khởi động lại bằng `docker compose up -d postgres rabbitmq`,
không phải do thay đổi trong phase này.

**Kiểm chứng đầy đủ**: `cargo build/fmt --check/clippy --workspace --all-targets -D warnings` +
`cargo test --workspace` (70 test suite + 10 test JQL mới = tổng tăng) sạch. `pnpm --filter
@metap/jira-fe build`/`lint` sạch qua nhiều vòng (bắt + fix lỗi TS thật `issue.status` vs
`issue.data.status`, unused import `Group`, và vài lỗi format thật). `pnpm --filter
@metap/crm-fe build` xác nhận thay đổi `platform-react` không phá consumer còn lại. Mọi JQL case
(hợp lệ lẫn lỗi) verify qua curl thật, không giả định.

**Còn lại — "customize dashboard"**: cố ý CHƯA làm trong phase này — đây là khoản đầu tư lớn nhất
trong 5 mục ban đầu (widget catalog, persist layout theo user/tenant, drag/resize), cần chốt thiết
kế riêng (nơi lưu layout — entity mới qua low-code hay bảng ops kiểu `cron_jobs`; per-user hay
per-tenant; danh sách widget v1) trước khi code, không rush vào cuối phase này.

Diff chưa commit.
