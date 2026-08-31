# Feature Briefs

Nơi theo dõi tính năng ở mức nhỏ hơn một phase trong `docs/roadmap.md`. Ba tài liệu process hiện có
mỗi cái trả lời một câu hỏi khác nhau — thư mục này lấp đúng chỗ trống còn lại:

| Tài liệu | Trả lời câu hỏi |
|---|---|
| `docs/roadmap.md` | Đang ở phase lớn nào, phase đó xong chưa |
| `docs/architectures/09-adr.md` | Vì sao chọn giải pháp kiến trúc này (quyết định *kỹ thuật*) |
| `docs/features/*.md` (thư mục này) | Một tính năng cụ thể làm gì, phạm vi tới đâu, khi nào coi là xong (yêu cầu *sản phẩm*) |

Không phải việc nhỏ nào cũng cần một file ở đây — xem Definition of Ready trong
`docs/agile-process.md`: bugfix rõ ràng, sửa doc, refactor cục bộ thì không cần. File ở đây dành
cho tính năng đủ lớn để cần thống nhất phạm vi *trước khi* code, để tránh việc code xong rồi mới
tranh cãi nó có nên làm vậy không.

## Chỉ còn 1 thư mục — brief của demo app đã chuyển ra repo riêng

`docs/features/*.md` (ở đây) — feature/change-log cho **core metap**: thay đổi trong
`crates/metap-*` (execution engine) hoặc `@metap/platform-ui` (reusable frontend library, repo
riêng) — thứ một downstream project thật sự import và phụ thuộc vào.

**`docs/features/demo/` không còn nữa (2026-08-31)** — 3 brief của nó (`crm.customers`'s entity
đồng hành: `sales.orders`/`inventory.movements`/`accounting.journal`) đã chuyển sang
`../metap-demo-crm/docs/features/demo/` cùng lúc `apps/crm-server`/`apps/crm-fe` tách ra repo
riêng (xem `docs/roadmap/51-example-apps-repo-split.md`). Demo app giờ không còn nằm trong `metap`
nữa — brief của nó thuộc về repo của chính nó.

## Quy trình

1. Copy `TEMPLATE.md` thành `NN-<slug-tinh-nang>.md` — `NN` là số thứ tự 2 chữ số tăng dần
   trong đúng thư mục đó (cấp gốc và `demo/` đánh số độc lập nhau, giống cách
   `docs/architectures/` và `crates/migrations/` đã làm). Đặt vào cấp gốc hay `demo/` theo tiêu
   chí ở trên.
2. Điền các mục, đặt `Trạng thái: proposed`.
3. Khi được duyệt (ai duyệt: track sở hữu theo `docs/team-charter.md`, hoặc tự quyết nếu chỉ có
   một người), đổi `Trạng thái: approved` và thêm vào bảng bên dưới.
4. Khi bắt đầu code, đổi `Trạng thái: in-progress`. Nếu tính năng đủ lớn để gắn với một phase
   trong `docs/roadmap.md`, ghi rõ phase đó trong file.
5. Khi xong, đổi `Trạng thái: done` và để nguyên file lại — đây là lịch sử, không xoá.
6. Nếu quyết định không làm nữa, đổi `Trạng thái: rejected` kèm lý do ngắn, không xoá file.

## Danh sách

**Core metap** (`crates/metap-*`, `@metap/platform-ui` — repo riêng):

| Tính năng | Trạng thái | Track | Phase liên quan |
|---|---|---|---|
| [Nâng cấp Frontend Platform](01-fe-platform-overhaul.md) | proposed (1 trong 4 gap đã xong) | Frontend Platform | chưa gắn |
| [Metadata-driven Workflow Engine](02-workflow-engine.md) | Increment 1 done | Backend Core | Phase 17 |
| [Organization & Identity Layer](03-organization-identity.md) | P0 done | Backend Core | Phase 18 |
| [Table-per-entity — readiness brief](04-table-per-entity.md) | in-progress — 5/5 bước code+e2e xong (2026-08-23), chưa wire vào binary nào | Backend Core | chưa gắn phase |
| [Cross-entity relations trong list view (3 mode)](05-cross-entity-relations.md) | Mode 2 done | Backend Core | không thuộc phase nào |
| [Pattern xác minh bất đồng bộ + gap logic tùy biến cho low-code](06-async-verification-pattern-and-lowcode-custom-logic.md) | proposed (ghi chú thảo luận, chỉ Option B là đề xuất code) | Backend Core | không thuộc phase nào |

Brief của demo app (Sales Order/Inventory Movement/Journal Entry, từng nằm ở `demo/`) — xem
`../metap-demo-crm/docs/features/demo/`.
