# Frontend Build Checklist

Checklist chi tiết ở mức UI/UX cho `packages/platform-react` + `apps/crm-fe`, mịn hơn cấp độ
phase của `docs/roadmap.md`. Rà soát lần gần nhất: 2026-08-20, đối chiếu với `docs/roadmap.md`
(trạng thái phase) và source thật của FE. Mục đã `[x]` có chú thích phase/thành phần liên quan để
trace ngược; mục `[ ]` là gap thật, chưa có gì trong sản phẩm — không phải lúc nào cũng là việc
cần làm ngay, xem "Chưa lên kế hoạch" ở cuối file.

Đừng nhầm các nhóm bên dưới (1-6) với "Phase N" của `docs/roadmap.md` — hai hệ đánh số độc lập.
Tầm nhìn kiến trúc dài hạn hơn checklist này (Jira/Confluence-style app trên metadata, durable
workflow runtime kiểu Temporal/Cadence) không nằm ở đây — xem `docs/team-charter.md`'s "Định
hướng đang ghi nhận, chưa có trigger".

---

## 1. Dynamic Table

- [x] Dynamic columns — `GeneratedList` (Phase 6)
- [x] Column metadata — từ `EntityListView` (Phase 6)
- [x] Pagination — cursor-based (Phase 4)
- [x] Cursor pagination — keyset, opaque cursor (Phase 4)
- [x] Sorting — `QueryPlanner`, giới hạn theo metadata (Phase 4)
- [x] Filtering — `QueryPlanner`, giới hạn theo metadata (Phase 4)
- [x] Search — ILIKE mặc định / FTS opt-in qua `searchMode` (Phase 4)
- [ ] Column visibility
- [ ] Column ordering
- [ ] Column resizing
- [ ] Row selection
- [ ] Bulk actions
- [x] Row actions — cột action View/Delete theo dòng (Phase 6)
- [ ] Export
- [ ] Refresh
- [ ] Saved views
- [x] Virtualization — `@tanstack/react-virtual` (Phase 6)

---

## 2. Generic CRUD

Frontend đã hỗ trợ generic CRUD cho entity Metap bất kỳ qua
`GeneratedList`/`GeneratedForm`/`RecordDetail`.

### List

- [x] Entity list page
- [x] Dynamic columns
- [x] Search
- [x] Filter
- [x] Sort
- [x] Pagination
- [ ] Bulk selection
- [ ] Bulk action
- [ ] Export
- [ ] Saved view

### Create

- [x] Dynamic create form — `GeneratedForm` (Phase 6)
- [x] Metadata-driven validation — field-metadata validator, không phải Zod schema riêng
      (Phase 0-6)
- [ ] Default values
- [ ] Conditional fields
- [x] Submit
- [ ] Submit & create another

### Detail

- [x] Dynamic detail page — `RecordDetail` (Phase 6)
- [ ] Field groups
- [ ] Tabs
- [ ] Related entities
- [ ] Activity
- [ ] Audit information — `workflow_events` append-only log tồn tại ở backend (Phase 5),
      chưa lộ ra UI

### Update

- [x] Dynamic edit form
- [ ] Dirty state
- [ ] Unsaved changes guard
- [ ] Partial update
- [ ] Optimistic update

### Delete

- [x] Confirmation — `window.confirm` (Phase 6)
- [x] Soft delete — backend (`CrudService.delete()`, Phase 6)
- [ ] Restore
- [ ] Bulk delete

---

## 3. Workflow Builder

- [x] Trigger configuration — chọn state field (Phase 11 Phase B)
- [ ] Condition builder — guard hiện là JSON thô trong `Textarea` (cùng pattern
      `PolicyCondition` editor của `PoliciesAdminPage`), chưa có UI builder có cấu trúc
- [x] Action configuration — transition action/from/to/label (Phase 11 Phase B)
- [x] Workflow validation — validate phía client trước khi save (Phase 11 Phase B)
- [x] Workflow publish — draft/publish/rollback, hot-reload không cần restart (Phase 11 Phase A)
- [x] Workflow version — version history append-only + preview publish (Phase 11 Phase A/B)
- [ ] Execution history
- [ ] Execution detail
- [ ] Retry execution
- [ ] Cancel execution

Basic model:

```
Trigger
   |
   v
Condition
 ┌─┴─┐
YES  NO
 │    │
 v    v
Action End
```

---

## 4. Admin

- [ ] Tenant / workspace — backend đã có (`POST /platform/tenants`, Phase 16 Giai đoạn 3),
      chưa có FE
- [x] Users — `UsersAdminPage` (Phase 15)
- [x] Roles — assign/revoke role qua `UsersAdminPage` (Phase 15)
- [x] Permissions — `PoliciesAdminPage` (Phase 15)
- [x] Entities — `LowCodeEntitiesAdminPage` (Phase 11 Phase A)
- [x] Fields — field builder trong `LowCodeEntitiesAdminPage` (Phase 11 Phase A/B)
- [ ] Relations — chưa có UI riêng ngoài field kind `reference`
- [x] Views — list-view builder (Phase 11 Phase A)
- [x] Workflows — `WorkflowBuilder` (Phase 11 Phase B)
- [ ] Integrations
- [ ] API keys
- [ ] Webhooks
- [ ] Audit logs
- [ ] System settings
- [ ] Feature flags

---

## 5. Developer / Platform UI

- [ ] Metadata explorer — một phần qua `LowCodeEntitiesAdminPage`, chưa phải explorer riêng
- [ ] Entity explorer
- [ ] API explorer — `GET /metadata/openapi.json` có sẵn (public, không auth), chưa có UI duyệt
- [ ] Event explorer
- [ ] Webhook manager
- [ ] Integration manager
- [x] Job monitor — `CronJobsAdminPage`, lịch sử run theo từng job (Phase 13/15)
- [ ] Workflow execution monitor
- [ ] System health — `/health` endpoint có, chưa có UI
- [ ] Feature flag manager
- [ ] Configuration viewer

---

## 6. UX Infrastructure

- [ ] Keyboard shortcuts
- [ ] Command palette
- [ ] URL state
- [ ] Deep linking
- [x] Browser back/forward — mặc định của `react-router`
- [ ] State persistence — chỉ có locale preference (Phase 14), chưa có state khác
- [ ] Local storage abstraction
- [ ] Unsaved changes guard
- [ ] Global loading state
- [ ] Global error handling
- [ ] Error recovery
- [ ] Accessibility
- [ ] Responsive behavior

---

## Nhóm ưu tiên tiếp theo

### Security / Platform

- [x] Permission / ABAC — `PermissionService` (Phase 3)
- [x] Audit — `workflow_events` (Phase 5), chưa có UI
- [ ] File
- [ ] Notification — outbox stub + `notification-worker` chỉ log stdout (Phase 5), chưa phải
      kênh notification thật (email/SMS/webhook) hay UI
- [ ] Realtime
- [x] Admin — xem mục 4 ở trên

### Advanced

- [ ] Workflow builder nâng cao — phần cơ bản đã có (mục 3), chưa "advanced" (condition
      builder có cấu trúc, execution monitor)
- [ ] Dashboard builder — **chưa từng được ghi nhận ở roadmap hay team-charter**; cần một
      feature brief riêng trong `docs/features/` nếu muốn theo đuổi, theo đúng kỷ luật
      trigger-based hiện tại
- [ ] Developer tools
- [ ] Integrations
- [ ] Advanced customization

### Production

- [ ] Unit tests FE — có ở backend (`cargo test`), FE chỉ tối thiểu — xem ghi chú Definition of
      Done bên dưới
- [ ] Integration tests FE
- [ ] E2E tests FE — có chạy Playwright thủ công một lần cho Phase 15 (verify live), không phải
      suite tự động trong CI
- [x] Performance — load test cho list/query path, backend only (Phase 8, 2026-08-17)
- [ ] Accessibility
- [ ] Error monitoring
- [x] Production hardening — phần lớn xong ở backend (Phase 8: CORS/CSP/rate limit/structured
      logging/Docker non-root/CI gate); secret manager, production deployment topology, và
      hardening riêng cho FE vẫn còn treo

---

## Definition of Done

Ghi chú: bản gốc yêu cầu unit/integration/E2E test cho mọi feature — mâu thuẫn với hướng test
tối giản, có mục tiêu đang áp dụng cho dự án hiện nay. Giữ checklist tham khảo bên dưới nhưng áp
dụng theo phạm vi thực tế của từng feature, không áp dụng máy móc từng dòng cho mọi PR.

Một feature được coi là hoàn thành chỉ khi:

- [ ] UI implemented
- [ ] Loading state implemented
- [ ] Empty state implemented
- [ ] Error state implemented
- [ ] Permission handling implemented
- [ ] API integration implemented
- [ ] Validation implemented
- [ ] Responsive behavior implemented
- [ ] Accessibility considered
- [ ] Unit tests added — chỉ khi có logic thật sự cần bảo vệ, không phải mặc định cho mọi feature
- [ ] Integration tests added where applicable
- [ ] E2E test added for critical flows
- [ ] No duplicated business logic
- [ ] No hard-coded metadata that should come from backend
- [ ] Documentation updated

---

## Critical Architecture Principle

Metap FE là một UI platform, không phải một tập hợp màn hình ERP.

Implementation nên theo hướng:

```
                METAP BACKEND
                     |
                     | Metadata
                     v
             +---------------+
             | Metadata      |
             | Engine        |
             +-------+-------+
                     |
                     v
             +---------------+
             | UI Engine     |
             +---------------+
             | Field         |
             | Form          |
             | Table         |
             | Detail        |
             | Filter        |
             | View          |
             +-------+-------+
                     |
          +----------+----------+
          |          |          |
          v          v          v
        CRUD      Workflow   Dashboard
          |          |          |
          +----------+----------+
                     |
                     v
                APPLICATION
```

Nhánh "Dashboard" trong sơ đồ trên hiện chưa có gì trong roadmap (xem "Dashboard builder" ở
"Nhóm ưu tiên tiếp theo" bên trên) — giữ trong sơ đồ như định hướng dài hạn, không phải cam kết
đã lên kế hoạch.

Tiêu chí thành công chính:

"Thêm một entity hoặc field type mới nên chỉ cần configuration/metadata và một extension nhỏ,
biệt lập, thay vì tạo thêm một implementation frontend hard-code hoàn toàn mới."

## Chưa lên kế hoạch

Các mục `[ ]` ở trên không tự động là backlog theo thứ tự ưu tiên — dự án đang theo kỷ luật
trigger-based (`docs/architectures/02-constraints.md`), không xây trước khi có nhu cầu cụ thể.
Trước khi bắt đầu bất kỳ mục nào trong nhóm "Admin" (Integrations/API keys/Webhooks/Audit
logs/System settings/Feature flags), "Developer / Platform UI", hay "Dashboard builder", nên có
một feature brief trong `docs/features/` nêu rõ trigger, theo đúng cách các mục tương tự đã được
ghi nhận ở `docs/team-charter.md`.
