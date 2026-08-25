## Phase 30: Jira — epics, issue type, issue linking, comment edit/delete, search (2026-08-24)

Tiếp Phase 29, chủ dự án yêu cầu "cứ crud full đủ tính năng đi" sau khi được gợi ý danh sách còn
thiếu so với Jira thật. Làm 5 mục ưu tiên cao nhất trong danh sách đã gợi ý.

- **`jira.epics`** (entity mới) — mirror đúng pattern `jira.sprints` (dedicated table, `Reference`
  vào `jira.projects`, workflow `open→closed`). `jira.issues` thêm field `epic` (Reference
  optional).
- **`issueType`** (Enum: bug/task/story, required, indexed+sortable) trên `jira.issues`.
- **`jira.issue_links`** (entity mới) — quan hệ có kiểu (`relates_to`/`blocks`/`duplicates`) giữa
  2 issue, khác `parentIssue` (1-nhiều phân cấp). **Phát hiện quan trọng**: entity này có **2
  field Reference cùng trỏ 1 entity** (`fromIssue`+`toIssue` đều → `jira.issues`) — lần đầu trong
  codebase. Verify sống xác nhận `metap-reconciler::compile()` xử lý đúng hoàn toàn: 2 FK
  constraint tên riêng biệt không đụng nhau (`fk_jira_issue_links_fromIssue`/`..toIssue`), 2
  trigger sync riêng, và `relatedDisplay` hydrate đúng **cả 2** field Reference trên cùng 1 record
  (không chỉ field đầu tiên).
- **Sửa/xoá comment** — `jira.comments` đã là entity CRUD đầy đủ từ trước, không cần đổi backend
  gì — chỉ thêm nút Edit/Delete ở FE (`CommentsPanel`), gọi thẳng `apiFetch` (không qua
  `useApiMutation` vì path cần động theo từng comment).
- **Search issue theo title** — `title` đã `searchable` từ trước (substring/ILIKE), chỉ thiếu ô
  tìm kiếm ở FE — thêm `SearchBox` trên `DashboardPage` (debounce 300ms).
- FE thêm: `IssueLinksPanel` (hiện link cả 2 chiều, label khác nhau theo hướng — "blocks" vs "is
  blocked by").

**Kiểm chứng sống đầy đủ qua HTTP thật**: tạo epic → gán issue vào epic + issueType → filter theo
epic đúng. Tạo issue link `blocks` giữa 2 issue → filter theo cả `fromIssue` lẫn `toIssue` đúng,
`relatedDisplay` đúng cả 2 phía. Search `?title=kanban` (partial) → đúng kết quả. Sửa comment
(PATCH) → nội dung đổi đúng, version tăng. Xoá comment (DELETE) → 200. `cargo build/fmt --check/
clippy --workspace --all-targets -D warnings` + `cargo test --workspace` sạch. `pnpm --filter
@metap/jira-fe build`/`lint` sạch.

**Còn lại chưa làm** (đã gợi ý, chủ dự án chưa yêu cầu): assignee/reporter picker thật (cần
`users` thành pseudo-entity), time tracking, burndown/report, watcher/notification UI, custom
field theo tenant qua low-code (jira-server không merge router đó).

Diff chưa commit.
