## Phase 29: Jira — sub-task (self-reference), story points, labels (2026-08-24)

Sau Phase 28, chủ dự án chọn hướng "tính năng mới" cho jira (thay vì UX polish hay dọn nợ nhỏ) —
đúng tinh thần "build để biết metap thiếu gì" đã theo suốt các phase trước. Chọn 3 field mới cho
`jira.issues`, ưu tiên cái nào có khả năng lộ ra giới hạn/khả năng thật của metap cao nhất.

- **`parentIssue`** — `Reference` **tự tham chiếu** (`ref_entity == "jira.issues"`, chính entity
  đó) cho sub-task. Đây là self-reference đầu tiên trong toàn bộ codebase — verify sống xác nhận
  metap xử lý đúng hoàn toàn, không cần đặc cách gì: `metap-reconciler::compile()` tạo đúng cột
  thật + FK constraint thật (`fk_jira_issues_parentIssue ... REFERENCES entities.jira_issues(id)
  ON DELETE RESTRICT`) + trigger sync, filter `?parentIssue={id}` hoạt động đúng qua
  `QueryPlanner`, và **quan trọng nhất**: `find_referencing_record`'s guard chặn xoá issue cha
  đang có sub-task hoạt động đúng y hệt Reference field thường (verify tách biệt hẳn khỏi
  comments để không nhầm lẫn 2 loại reference).
- **`storyPoints`** (`Number`, optional) — thêm bình thường, không phát hiện gì mới.
- **`labels`** (`FieldKind::Json`, mảng string) — **phát hiện gap thật**: metap chưa có
  `FieldKind` multi-select/tag chuyên biệt. `packages/platform-react`'s `FieldInput` render mọi
  field `Json` thành `Textarea` raw text — sửa label qua form generic nghĩa là gõ tay
  `["bug","urgent"]`. Ghi nhận rõ, không tự ý build `FieldKind::MultiSelect` mới (1 entity cần
  không đủ mạnh làm trigger cho 1 thay đổi platform-wide) — để dành nếu có nhu cầu thật thứ 2.
- FE: `SubtasksPanel` mới trong `IssueDetailPage.tsx` (list sub-task qua `?parentIssue={id}`,
  đọc-only — tạo sub-task vẫn qua form "New issue" chung, combobox Parent Issue giờ đã tự load
  sẵn nhờ fix `ReferenceFieldInput` ở Phase 26).

**Kiểm chứng sống đầy đủ qua HTTP thật**: tạo issue có đủ 3 field mới → đúng. Filter sub-task theo
`parentIssue` → đúng. Xoá issue cha có sub-task (tách biệt khỏi comment để test sạch) → 409 đúng,
message chỉ đúng `"parentIssue" on "jira.issues"`. `cargo build/fmt --check/clippy --workspace
--all-targets -D warnings` + `cargo test --workspace` sạch. `pnpm --filter @metap/jira-fe
build`/`lint` sạch.

Diff chưa commit.
