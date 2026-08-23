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

## Hai thư mục, hai loại thay đổi khác nhau

**`docs/features/*.md` (ở đây, cấp gốc)** — feature/change-log cho **core metap**: thay đổi
trong `crates/metap-*` (execution engine) hoặc `packages/platform-react` (reusable frontend
library) — thứ một downstream project thật sự import và phụ thuộc vào.

**`docs/features/demo/*.md`** — feature brief cho **demo app** (`apps/crm-server`,
`apps/crm-fe`). `apps/` là demo/test app cho toàn bộ dự án, không phải sản phẩm (xem CLAUDE.md's
"Sample apps") — chứng minh bề mặt thư viện hoạt động thật, không phải thứ downstream project
import trực tiếp. Entity demo (`apps/crm-server/src/entities/` — `crm.customers`,
`sales.orders`, `inventory.movements`, `accounting.journal`) là fixture chứng minh pattern,
không phải module nghiệp vụ thật của một sản phẩm CRM/ERP cụ thể — brief của chúng nằm riêng ở
`demo/` để không lẫn với thay đổi core.

Khi viết brief, tự hỏi: thay đổi này có ảnh hưởng gì tới một downstream project import
`crates/metap`/`packages/platform-react` không? Có → cấp gốc. Không, chỉ là ví dụ/fixture trong
`apps/crm-*` → `demo/`.

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

**Core metap** (`crates/metap-*`, `packages/platform-react`):

| Tính năng | Trạng thái | Track | Phase liên quan |
|---|---|---|---|
| [Nâng cấp Frontend Platform](01-fe-platform-overhaul.md) | proposed (1 trong 4 gap đã xong) | Frontend Platform | chưa gắn |
| [Metadata-driven Workflow Engine](02-workflow-engine.md) | Increment 1 done | Backend Core | Phase 17 |
| [Organization & Identity Layer](03-organization-identity.md) | P0 done | Backend Core | Phase 18 |
| [Table-per-entity — readiness brief](04-table-per-entity.md) | in-progress — 5/5 bước code+e2e xong (2026-08-23), chưa wire vào binary nào | Backend Core | chưa gắn phase |
| [Cross-entity relations trong list view (3 mode)](05-cross-entity-relations.md) | Mode 2 done | Backend Core | không thuộc phase nào |

**Demo app** (`apps/crm-server`, `apps/crm-fe` — xem `demo/`):

| Tính năng | Trạng thái | Track | Phase liên quan |
|---|---|---|---|
| [Sales Order — module thứ hai](demo/01-sales-order-entity.md) | done | App/Entity | Phase 7 |
| [Inventory Movement — module thứ ba](demo/02-inventory-movement-entity.md) | done | App/Entity | Phase 7 |
| [Journal Entry — module thứ tư](demo/03-journal-entry-entity.md) | done | App/Entity | Phase 7 |
