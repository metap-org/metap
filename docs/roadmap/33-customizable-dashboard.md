## Phase 33: Customizable dashboard — layout, widget catalog, org default (2026-08-25)

Mục cuối trong 5 mục chủ dự án nêu ("search nâng cao, JQL, customize dashboard, chart, view
logwork") — cố ý để sau (Phase 32) vì cần chốt 3 quyết định thiết kế trước khi code. Đã chốt: nơi
lưu = bảng ops nhỏ kiểu `cron_jobs`; phạm vi = **2 cấp** (per-user override, per-tenant default —
chỉ admin/quyền tương đương sửa được default); drag/resize = thêm thư viện `react-grid-layout`.

- **`metap-dashboards`** (crate mới, plain library, cùng hình dạng `metap-cron`) — bảng
  `dashboard_configs` (migration `0022`): `owner_user_id IS NULL` = default toàn tenant (1 dòng/
  tenant, unique index `NULLS NOT DISTINCT` — Postgres coi nhiều NULL là phân biệt theo mặc định,
  cần khai báo rõ để tránh nhiều dòng "default" chồng nhau); `owner_user_id = <user>` = layout
  riêng của user đó. `get_effective_dashboard`: layout riêng nếu có, không thì layout mặc định
  tenant, không thì `None` — client tự có bộ widget khởi đầu cứng khi cả 2 đều chưa có. `layout`
  là `jsonb` mờ với crate này lẫn route — chỉ FE hiểu cấu trúc widget bên trong.
- **`GET/PUT /dashboards/me`** (mọi user, layout riêng) và **`GET/PUT /dashboards/tenant-default`**
  (`PUT` qua `AdminContext` — cùng tư thế `routes/admin.rs`, `GET` cho mọi user để họ xem được
  default trước khi có layout riêng) — route generic trong `metap-http`, không phải app nào biết.
- **`CustomizableDashboardPage`** (`jira-fe`, route `/dashboard/custom`, giữ nguyên `/` Dashboard
  cũ không đổi — 2 trang mục đích khác nhau) — widget catalog v1: **bar chart** (group theo
  status/priority/issueType, tái dùng `BarChart` từ Phase 32) và **stat tile** (đếm theo 1 câu
  JQL, dogfood tiếp JQL engine). `recentList` và widget khác để dành sau, chưa build. Edit mode:
  thêm/xoá widget, kéo-thả/resize qua `react-grid-layout` (thêm làm dependency riêng của
  `jira-fe`, **không** đẩy vào `packages/platform-react` — khác `BarChart`, ép mọi app tương lai
  cõng thư viện grid-layout chỉ vì 1 app cần chưa hợp lý). Admin thấy thêm `SegmentedControl` "My
  dashboard" / "Organization default" để sửa cấp tenant.
- **`react-resizable`** phải thêm làm dependency **trực tiếp** dù chỉ dùng gián tiếp qua
  `react-grid-layout` — phát hiện thật lúc build: `import "react-resizable/css/styles.css"` build
  fail vì pnpm strict linking không expose transitive dependency's file ra ngoài, chỉ named deps
  trong `package.json` mới resolve được.

**Kiểm chứng đầy đủ**: migration `0022` áp cho cả DB platform lẫn DB dedicated `metap_myjira` của
jira-server (bảng `dashboard_configs` xác nhận đúng schema qua `\d`). Verify sống qua HTTP thật:
`GET /dashboards/me` khi chưa lưu gì → `null`; `PUT` → lưu đúng; `GET` lại → đúng dữ liệu vừa lưu;
`PUT /dashboards/tenant-default` → lưu default tenant; user chưa có layout riêng `GET /dashboards/
me` → fallback đúng về default tenant. `cargo build/fmt --check/clippy --workspace --all-targets
-D warnings` + `cargo test --workspace` (72 test suite, +2 so với trước) sạch — bắt + fix 2 lỗi
compile thật lúc viết `metap-dashboards` (thiếu sqlx `macros` feature cho `#[derive(FromRow)]`,
và `Copy` bound sai trên `&mut PgConnection`). `pnpm --filter @metap/jira-fe build`/`lint` sạch —
bắt + fix lỗi build thật (`react-resizable` CSS resolve). `pnpm --filter @metap/crm-fe build` xác
nhận dependency mới không lan sang app khác.

Dữ liệu demo: cả layout cá nhân lẫn layout mặc định tenant đã có ví dụ thật (2 bar chart + 1 stat
tile) qua đúng route vừa build, không phải seed trực tiếp DB — sẵn sàng để chủ dự án mở
`/dashboard/custom` và thấy dashboard có dữ liệu ngay, chưa cần tự cấu hình từ đầu.

Diff chưa commit.
