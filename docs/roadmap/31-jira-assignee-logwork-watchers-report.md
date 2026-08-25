## Phase 31: Jira — assignee picker thật, logwork (time tracking), watchers, sprint burndown report (2026-08-24 → 08-25)

Sau Phase 30, chủ dự án yêu cầu "TOÀN BỘ nhé, cả logwork" — làm hết những mục còn lại đã gợi ý ở
cuối Phase 30 (trừ custom field qua low-code, xem phần "Chưa làm" bên dưới).

- **`GET /users`** (`crates/metap-http/src/routes/users.rs`, `metap_peripherals::list_tenant_users`)
  — danh sách `{id, email}` mọi user trong tenant, gate bằng `AuthContext` thường (không phải
  `AdminContext` — gán issue cho ai không phải hành động admin, khác `GET /admin/users` vốn trả
  role assignment). Đây là primitive "chọn 1 user" cho assignee/reporter/watcher picker — `users`
  không phải `EntityDefinition` nên không dùng `ReferenceFieldInput` được.
- **`AssigneePicker`** (FE, `IssuePanels.tsx`) — `Select` PATCH thẳng `assigneeEmail` (field
  `String` thường, không phải Reference — không có gì để reference). `useCurrentUserEmail()` cross-
  reference `GET /auth/me`'s `userId` với danh sách `/users` (JWT chỉ mang `sub`, không có email).
- **`jira.worklogs`** (entity mới, dedicated table) — logwork thật: `issue`(Reference, required),
  `authorEmail`, `timeSpentMinutes`, `workDate` (sortable), `description`. `issue_entity.rs` thêm
  `originalEstimateMinutes` để so sánh estimate-vs-logged. `WorklogsPanel` (FE): form log giờ +
  tổng đã log + cảnh báo over-estimate, cùng công thức "original estimate / time spent" Jira thật
  dùng.
- **`jira.watchers`** (entity mới, dedicated table) — `issue`+`userEmail`, không có composite-
  unique (metap chưa có khái niệm này) và không có delivery thật (chỉ là subscription list —
  `notification-worker` mới chỉ log stdout, chưa có email/webhook nào). `WatchersPanel` (FE):
  toggle Watch/Unwatch cho chính mình.
- **`GET /api/{entity}/{id}/workflow-events`** (route generic mới, `metap-http`, đọc
  `metap_workflow::list_events` — hàm read mới, `workflow_events` trước giờ chỉ có ghi qua
  `record_event`) — lịch sử chuyển trạng thái của 1 record, tenant-scoped, entity-agnostic hoàn
  toàn (parallel với `attachments.rs`'s pattern). Đây là primitive platform thật, không phải
  jira-specific — bất kỳ entity nào có workflow đều có sẵn.
- **`SprintReportPage`** (FE, `jira-fe`) — sprint report + **burndown chart thật** (không phải chỉ
  snapshot tĩnh): dùng `GET .../workflow-events` để tái dựng "issue này done từ ngày nào", kết hợp
  `storyPoints` hiện tại để tính "remaining points mỗi ngày" trong khoảng `sprint.startDate` →
  `sprint.endDate`, vẽ SVG line chart (actual vs ideal) tay — không thêm chart-library mới, đúng
  tinh thần "native HTML5 DnD, không thêm dependency" đã làm ở `BoardPage`. Tính toán lại toàn bộ
  phía client mỗi lần load (không có snapshot theo ngày lưu sẵn) — đủ cho quy mô demo (1 sprint,
  vài chục issue), không nhằm scale lớn.

**Phát hiện quan trọng — gap thật, không phải jira-specific**: PATCH một record được tạo *trước
khi* 1 field trở thành `required: true` (ở đây: `issueType`, thêm ở Phase 30) sẽ fail validation
dù chỉ sửa field khác không liên quan — vì `CrudService::update()` merge `raw_data` vào
`existing.data` rồi validate lại **toàn bộ** theo metadata hiện tại, và record cũ không có key
`issueType` trong JSONB. Verify sống: PATCH `assigneeEmail` cho issue demo cũ → 400
`fieldErrors: {issueType: ["required"]}`; PATCH lại có kèm `issueType` trong cùng request → 200.
**metap chưa có cơ chế migration/backfill ở tầng metadata** cho tình huống "thêm field required
vào entity đã có record" — khác với table-per-entity's `migration`/`quarantine` (Phase 19 §4, đó
là backfill ở tầng DDL/cột vật lý, không phải validation JSONB). Đã backfill thủ công record demo
duy nhất (`issueType: "story"`, `storyPoints: 5`) qua PATCH thật, không sửa migration/DB trực
tiếp. Ghi nhận vào "Định hướng chưa lên phase" — chưa có trigger đủ lớn để build cơ chế backfill
chung.

**Kiểm chứng sống đầy đủ qua HTTP thật**: `/users` trả đúng danh sách. Assignee PATCH thành công
sau khi backfill `issueType`. Log work `POST /api/jira.worklogs` → 201, `GET ?issue=` → đúng tổng
phút. Watch/unwatch `POST`/`DELETE /api/jira.watchers` → đúng, version tăng đúng. Dữ liệu test
(worklog/watcher tạm, email test) đã dọn sau khi verify — record demo giờ có `assigneeEmail`
thật, `storyPoints: 5` để burndown chart có dữ liệu khi chủ dự án tự test.
`/api/jira.issues/{id}/workflow-events` trả đúng 9 sự kiện chuyển trạng thái đã có sẵn từ trước.
`cargo build/fmt --check/clippy --workspace --all-targets -D warnings` + `cargo test --workspace`
(70 test suite) sạch. `pnpm --filter @metap/jira-fe build`/`lint` sạch (bắt + fix 1 lỗi TS thật:
`IssueData` thiếu field mới thêm ở Phase 30/31, và 1 lỗi format thật ở 3 file mới — cả hai không
phải false positive).

**Cố ý bỏ qua (quyết định của chủ dự án 2026-08-25)**: custom field theo tenant qua low-code — rà
code phát hiện `metap-lowcode-http` (`crates/metap-lowcode-http/src/lib.rs`) hardcode
`state.pool` (pool platform dùng chung) ở **mọi** handler (~15+ chỗ dùng `&state.pool` trực tiếp,
không qua `Router`), thay vì `Router`-resolve theo tenant như mọi route khác. Wire crate này vào
`jira-server` nguyên trạng sẽ khiến tenant `DedicatedDb` của jira (đã provision thật) ghi nhầm
draft/publish entity custom vào database platform dùng chung — đúng loại lỗi rò tenant đã gặp và
fix nhiều lần trong session này (login, dev-tools). Sửa đúng cần refactor `metap-lowcode-http` để
`Router`-aware trước — rủi ro lớn hơn, đụng vào crate `crm-server` đang phụ thuộc — được trình bày
rõ đánh đổi này, chủ dự án chọn **bỏ qua mục này**, coi "TOÀN BỘ nhé, cả logwork" đã hoàn thành ở
4/5 mục (assignee picker, logwork, watchers, sprint burndown report). Không có trigger để làm lại
trừ khi có yêu cầu mới.

Diff chưa commit.
