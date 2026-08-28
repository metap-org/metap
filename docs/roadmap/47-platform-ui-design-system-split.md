## Phase 47: Tách frontend library ra repo riêng — `@metap/platform-ui` + `@metap/ui`, gỡ Mantine khỏi `apps/crm-fe`/`apps/jira-fe` (2026-08-28 → 2026-08-29)

Chủ dự án muốn migrate `packages/platform-react` (Mantine-based) sang dùng design system riêng
(`../design-system`, Radix + Tailwind + shadcn-style) thay Mantine, và tách ra repo riêng thay vì
tiếp tục nằm trong pnpm workspace của `metap` — chuẩn bị cho việc tách `apps/crm-server`/`jira-server`
ra repo riêng sau này (chưa làm, việc riêng).

**Bước 1 — tạo repo mới `@metap/platform-ui` (`../platform-ui`), move nguyên trạng:**
- Đặt tên `@metap/platform-ui` (không phải `platform-react`, chủ dự án từ chối tên đó). Scope
  "full ngay từ đầu" — move toàn bộ module (`admin/`, `api/`, `auth/`, `charts/`, `detail/`,
  `field/`, `form/`, `i18n/`, `list/`, `metadata/`, `navigation/`, `shell/`, `workflow/`), không
  chỉ shell tối thiểu.
- Chiến lược move-trước-swap-sau (theo yêu cầu chủ dự án): copy nguyên code Mantine sang trước để
  đạt 100% feature parity ngay, swap từng module sang design-system ở bước riêng — tránh vừa move
  vừa rewrite cùng lúc.
- `@ui/ui-lib` (tên ban đầu của design-system) link cục bộ qua `link:../design-system` (chưa
  publish package nào).

**Bước 2 — gỡ Mantine khỏi `platform-ui`, done:**
- Port toàn bộ 14 file có UI thật (không tính file thuần logic như `api/`/`auth/AuthContext`) sang
  design-system, theo 1 bảng mapping component nhất quán (Table/Select/Input/Checkbox/Badge/...
  đổi API; `Container`/`Stack`/`Group`/`Text`/`Title` không có tương đương → Tailwind div thuần).
  File lớn nhất, cuối cùng: `admin/LowCodeEntitiesAdminPage.tsx` (1296 dòng) — cần viết mới 2
  helper component (`TagsField`, `MultiFieldSelect`) bù gap `TagsInput`/`MultiSelect` design-system
  không có.
- Gỡ hẳn `@mantine/*` khỏi `devDependencies` sau khi xác nhận `grep "@mantine"` sạch.
- Verify: `typecheck`/`lint`/`format:check` sạch sau mỗi batch port.

**Bước 3 — trỏ `apps/crm-fe`/`apps/jira-fe` sang `@metap/platform-ui`, done:**
- Đổi `"@metap/platform-react": "workspace:*"` → `"@metap/platform-ui": "link:../../../platform-ui"`
  trong `package.json` cả 2 app, đổi toàn bộ `import ... from "@metap/platform-react"` (21 file,
  export list 2 bên giống hệt nhau nên đổi string thuần, không cần sửa logic).
- **Gap thật phát hiện khi verify**: Vite dev server 403 mọi request tới source của `platform-ui`
  qua `/@fs/...` vì nó là symlink ra ngoài pnpm workspace — `server.fs.allow` mặc định chỉ cho
  phép workspace root. Fix: thêm `fs.allow` trỏ tới `platform-ui` (sau đó cả `design-system`, xem
  bước 4) trong `vite.config.ts` cả 2 app.
- Verify: `tsc -b`/`oxlint`/`prettier --check`/`vite build` sạch cả 2 app.

**Bước 4 — gỡ Mantine khỏi chính `apps/crm-fe`/`apps/jira-fe` (không chỉ phần port), done:**
- Phát hiện: 2 app vẫn dùng Mantine trực tiếp cho code riêng (không qua `platform-react`) —
  `MantineProvider`/`Notifications` ở `main.tsx`, cộng 12 file (`jira-fe`'s pages phần lớn:
  `DashboardPage`/`BoardPage`/`BacklogPage`/`IssuePanels`/`IssueDetailPage`/`AdvancedSearchPage`/
  `LogworkReportPage`/`SprintReportPage`/`CustomizableDashboardPage`, `crm-fe`'s `EntitiesPage`).
- Dựng pipeline Tailwind thật cho cả 2 app (trước đó thiếu — chỉ đổi import, chưa có CSS build):
  `tailwind.config.cjs` mới (`presets: [uiLibPreset]`, `corePlugins.preflight: false` để không
  double-reset với `@ui/ui-lib/style.css`, `content` quét cả `src/` riêng lẫn
  `../../../platform-ui/src`), `postcss.config.cjs` đổi `postcss-preset-mantine` → `tailwindcss`+
  `autoprefixer`, `src/index.css` mới (`@tailwind base/components/utilities`).
- `main.tsx`: `MantineProvider`/`Notifications` → `TooltipProvider`/`ToastProvider` (`@ui/ui-lib`).
  `notifications.show()` → `toast()`. `SegmentedControl` → `Tabs`/`TabsList`/`TabsTrigger`.
  `ActionIcon` → `IconButton`. `Anchor component={Link}` → `<Link>` styled bằng className thẳng
  (design-system không có polymorphism kiểu Mantine).
- **Gap thật thứ hai phát hiện khi verify**: `@ui/ui-lib/dist/style.css` cũng là file qua symlink
  ra ngoài workspace — cùng lỗi 403 như bước 3 nhưng cho CSS thay vì source TS. Thêm luôn
  `design-system` vào `fs.allow`.
- Kết quả đo được: CSS output build giảm từ 231KB (Mantine) xuống ~27-32KB (Tailwind thuần, đã xác
  nhận trong file build chứa đúng utility class + CSS variable).

**Bước 5 — đổi tên `@ui/ui-lib` → `@metap/ui`, done:**
- Chủ dự án chọn tên khác sau khi thấy `@ui/ui-lib` hoạt động — đổi `package.json`'s `name` của
  `design-system` + mọi import/dependency-key ở `platform-ui`/`crm-fe`/`jira-fe` (không đổi tên
  thư mục repo, chỉ đổi specifier). Xoá symlink `node_modules/@ui` cũ, `pnpm install` lại tạo
  `node_modules/@metap/ui` mới. Không đổi các file doc lịch sử có ngày tháng (`docs/superpowers/
  plans/*.md` của `design-system`) — giữ nguyên làm bản ghi thời điểm.
- Verify lại toàn bộ (`typecheck`/`lint`/`format:check`/`vite build`) sau đổi tên — sạch cả 3 nơi.

**Bước 6 — xoá `packages/platform-react`, done:**
- Xác nhận `grep "@metap/platform-react"` sạch trong toàn bộ `metap` (ngoài chính thư mục
  `packages/platform-react`) trước khi xoá — không còn app nào phụ thuộc.
- Xoá qua `git rm -r packages/platform-react` + dọn `node_modules` còn sót (gitignored, `git rm`
  không đụng tới).
- Cập nhật `CLAUDE.md` (không chỉ file này) — mọi mô tả `packages/platform-react` là package hiện
  hành đổi thành `@metap/platform-ui` (repo riêng `../platform-ui`, consume qua `link:`), thêm ghi
  chú `fs.allow`, sửa hướng dẫn `generate:types` (giờ chạy trong repo `platform-ui`, không phải
  `pnpm --filter` từ `metap`). Các entry roadmap lịch sử khác nhắc `packages/platform-react`
  (Phase 32/33) giữ nguyên — bản ghi đúng tại thời điểm đó, theo đúng convention Phase 46 đã nêu.

**Còn lại, cố ý chưa làm**: tách `apps/crm-server`/`apps/jira-server`/`apps/crm-fe`/`apps/jira-fe`
ra repo riêng (mục tiêu ban đầu chủ dự án nêu, nói rõ để sau — việc tách frontend library ra trước
làm bước này "dễ hơn" theo đúng lời chủ dự án). Không browser-test theo policy — chỉ verify qua
`typecheck`/`lint`/`format:check`/`vite build`, chưa xác nhận trực quan trên trình duyệt.
