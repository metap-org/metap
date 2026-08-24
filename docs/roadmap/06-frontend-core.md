## Phase 6: Frontend Core

**Trạng thái: Đã xong.** React + TypeScript app shell, TanStack Query API client (`packages/platform-react/src/api`), metadata client, `GeneratedList` (kèm cursor-based infinite-scroll pagination và row windowing bằng `@tanstack/react-virtual`), và `FieldRenderer` (cả hai nửa — `FieldValue`/`fieldKindConfig` cho read, `FieldInput` cho write) đều đã xong. `GeneratedForm` đã xong. `WorkflowActionBar` đã xong. Permission-aware UI state đã xong — `CrudService.get()` giờ trả về `capabilities` chủ động (writable field, `canUpdate` ở record-level, kết quả guard thật cho từng transition) mà `GeneratedForm`/`WorkflowActionBar`/`FieldValue` dùng để disable/đánh dấu trước những gì sẽ fail, trước khi user thử. Điều hướng danh sách và delete được thêm vào như một gap-fix follow-up sau khi verify thủ công phát hiện `GeneratedList` không có cách nào thực sự đến được route create của `GeneratedForm` hay `RecordDetail`, và delete thì chưa tồn tại ở đâu cả: `GeneratedList` giờ có nút "New" và một cột action View/Delete theo từng dòng, `RecordDetail` có nút Delete, và backend có thêm hỗ trợ soft-delete (`EntityAction` mở rộng thêm `"delete"`, `PermissionService.canDeleteEntity`, `CrudService.delete()`, `DELETE /api/:entity/:id`, `WorkflowEngine.emitDeleted`). Tất cả những cái này đã pass typecheck/lint/bộ test backend và đã được commit; vẫn chưa được verify trên browser trong sandbox này (không có headless Chromium chạy được — thiếu system library, không có `sudo`, không có phương án cache thay thế). Frontend giờ nằm trong `packages/platform-react` + `apps/crm-fe` (đổi tên từ `web/`) như một phần của việc restructure monorepo ngày 2026-08-02 — xem mục "Frontend Platform Package" của [Architecture](docs/architectures/04-strategy.md). Sự phụ thuộc còn lại của `packages/platform-react` vào `react-router-dom` (`ApiErrorMessage`/`GeneratedList`/`RecordDetail` gọi trực tiếp `Link`/`useNavigate`) cũng đã được fix: một `NavigationAdapter` được inject qua React Context thay thế cả 3 chỗ import trực tiếp, và `apps/crm-fe` cung cấp implementation thật duy nhất. Đã pass typecheck/build/lint/toàn bộ bộ test backend; chưa verify trên browser vì cùng lý do sandbox như trên.

Mục tiêu:

- React + TypeScript app shell.
- TanStack Query API client.
- Generated list renderer.
- Generated form renderer.
- Workflow action UI.
- Permission-aware UI state.
- Table virtualization.

Deliverables:

- `metadata-client`
- `api-client`
- `GeneratedList`
- `GeneratedForm`
- `WorkflowActionBar`
- `FieldRenderer`

