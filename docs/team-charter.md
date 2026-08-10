# Team Charter

Viết ngày 2026-08-10, trước khi có contributor thứ hai thật sự tham gia — mục tiêu là chuẩn bị sẵn
module ownership và một roadmap có thể chia việc song song *trước khi* việc onboarding tạo áp lực
phải ứng biến chúng tại chỗ. Đây là tài liệu sống: khi có người thật tham gia, thay các nhãn track
trong "Phân công hiện tại" bằng tên thật. Phần còn lại (ranh giới, cách chia work-stream) vẫn nên
đúng bất kể số lượng người.

Tài liệu này bổ sung, không thay thế, các doc đã có:

- `docs/architectures/index.md` — cái gì đã được xây và tại sao (arc42/C4).
- `docs/roadmap.md` — trạng thái theo từng phase, nguồn sự thật duy nhất cho "cái gì đã xong."
- `docs/CONTRIBUTING.md` — quy trình cơ học (branch, check, review) để merge một thay đổi.
- `docs/agile-process.md` — nhịp làm việc: cadence review, Definition of Ready/Done.
- `docs/features/` — brief cho từng tính năng cụ thể (nhỏ hơn một phase), phạm vi + tiêu chí chấp nhận.

Tài liệu này trả lời hai câu hỏi mà các doc trên chưa trả lời: **ai nên review một thay đổi cụ
thể**, và **các phase còn lại chia cho nhiều người làm song song mà không đụng nhau như thế nào**.

## Bắt đầu từ đâu (contributor mới)

Đọc theo thứ tự:

1. `CLAUDE.md` — stack, cấu trúc monorepo, các lệnh, tóm tắt kiến trúc, quy ước bắt buộc.
2. `docs/architectures/index.md` và các phần nó dẫn tới cho khu vực bạn sẽ làm.
3. Bảng Current Status trong `docs/roadmap.md` — tìm phase bạn sẽ nhận.
4. Mục "Module Ownership & Track" bên dưới — tìm phase đó thuộc track nào, và những module bạn sẽ
   đụng vào.
5. `docs/CONTRIBUTING.md` — branch, check bắt buộc, cách route review.

Sau đó dựng dev stack theo mục Commands trong `CLAUDE.md` trước khi viết code.

## Đề xuất một tính năng mới

Nếu việc bạn muốn làm không nằm sẵn trong một phase của `docs/roadmap.md`: viết một feature brief
trong `docs/features/` (template có sẵn ở đó) thay vì bắt đầu code luôn. Track sở hữu module liên
quan (bảng ở dưới) là người duyệt — với team một người như hiện tại, tự duyệt cũng được, miễn là
file đã có đủ phạm vi/tiêu chí chấp nhận trước khi code, đúng Definition of Ready trong
`docs/agile-process.md`. Tính năng đủ lớn thì khi duyệt nên gắn luôn vào một phase mới hoặc phase
đang có trong roadmap, để `docs/roadmap.md` không bị lệch khỏi thực tế đang làm.

## Module Ownership & Track

Hiện chỉ có một contributor duy nhất, nên mọi track dưới đây đang chưa có ai giữ — bảng này tồn
tại để khi có contributor thứ hai, câu hỏi "ai review cái này" và "tôi được đụng tới đâu mà không
cần sign-off chéo track" đã có sẵn câu trả lời, thay vì phải thương lượng theo từng PR.

| Track | Sở hữu | Module |
|---|---|---|
| **Backend Core** | Execution engine không biết business entity: metadata compiler, permission engine, query planner, workflow engine, CRUD orchestration. Bán kính ảnh hưởng lớn nhất — thay đổi ở đây lan sang mọi track khác. | `crates/metap`, `crates/metap-metadata`, `crates/metap-permission`, `crates/metap-query`, `crates/metap-workflow`, `crates/metap-crud` |
| **HTTP/API Surface** | Router axum, auth extractor, shape request/error, security headers, rate limiting. | `crates/metap-http` |
| **Backend Ops/Infra** | Mọi thứ chạy như process/CLI riêng: drain outbox, event consumer, cron dispatch, migration, dev tooling, plumbing config/DB pool/EventBus. | `crates/metap-infra`, `crates/outbox-publisher`, `crates/notification-worker`, `crates/metap-cron`, `crates/cron-scheduler`, `crates/db-migrate`, `crates/dev-tools`, `crates/metap-peripherals` |
| **Frontend Platform** | Thư viện React tái sử dụng mà app khác import: api client, generated CRUD UI, field renderer, permission/auth primitive, shell, admin kit, i18n. | `packages/platform-react` |
| **App/Entity** | Consumer ví dụ cụ thể: đăng ký business entity, wire một binary chạy được, frontend demo harness. Nơi các module nghiệp vụ mới (Phase 7) sẽ nằm. | `apps/crm-server`, `apps/crm-fe` |

Các ranh giới khiến việc phân track có ý nghĩa (danh sách đầy đủ ở
`docs/architectures/02-constraints.md` và `CLAUDE.md`):

- HTTP/API Surface không bao giờ vòng qua Backend Core để đụng thẳng `sqlx`/`lapin`.
- App/Entity không bao giờ bị import ngược lại bởi bất kỳ crate `crates/metap-*` nào — hướng phụ
  thuộc là một chiều (thư viện ← consumer).
- Frontend Platform không bao giờ hardcode tên entity cụ thể, cũng không vượt qua HTTP API để đụng
  vào nội bộ backend.
- Thay đổi cần đụng nhiều track (vd: thêm property mới cho `EntityField`, đụng cả Backend Core lẫn
  generated types của Frontend Platform) cần sign-off từ cả hai track, và thường nên có mục ADR
  (`docs/architectures/09-adr.md`) vì đây đúng kiểu quyết định tốn kém nếu làm lại một mình.

### Phân công hiện tại

| Track | Người phụ trách |
|---|---|
| Backend Core | *(chưa có)* |
| HTTP/API Surface | *(chưa có)* |
| Backend Ops/Infra | *(chưa có)* |
| Frontend Platform | *(chưa có)* |
| App/Entity | *(chưa có)* |

## Các luồng làm song song (những phase roadmap còn lại)

Theo trạng thái `docs/roadmap.md` ngày 2026-08-10, các phase chưa xong/đang làm là 7, 8, 11, 12,
14. Nhóm lại thành các luồng chạy song song được, kèm phụ thuộc giữa chúng để hai người không vô
tình sửa lại cùng một chỗ:

### Stream A — Metadata Control Plane (track Backend Core)

Phần còn lại của Phase 11 (các sub-project của Phase A: runtime loader, publish validation
pipeline, admin API — spec lưu trữ/versioning đã viết ở `docs/low-code-metadata-storage-design.md`,
cần retarget từ bản nháp TS ban đầu sang Rust trước khi implement). Đây là việc nặng về thiết kế
trước khi nặng về code; coi "viết lại spec sang Rust" là một bước review riêng, không gộp vào PR
implement đầu tiên.

**Mở khóa cho:** phần metadata-label translation của Phase 14 (đang bị block bởi stream này — đừng
bắt đầu việc đó độc lập).

**Rủi ro cần phối hợp:** stream này nhiều khả năng nhất sẽ đổi shape của `EntityDefinition`
(`crates/metap-metadata`) và generated types phía frontend. Ai đang làm Stream B để thêm module
entity mới nên đồng bộ trước khi thay đổi schema của stream này được merge, không phải sau.

### Stream B — Module Migration (track App/Entity)

Phase 7: port thêm các module nghiệp vụ lên metadata model hiện tại. Thứ tự gợi ý đã có sẵn trong
`docs/roadmap.md` (master data → module giao dịch → module nhiều workflow → luồng report/export;
vd: sales/purchase order, dịch chuyển kho, sổ kế toán).

**Bắt đầu ngay được**, độc lập với Stream A — bề mặt `EntityDefinition`/`CrudService` hiện tại đã
ổn định. Chỉ cần dừng đồng bộ với Stream A nếu thay đổi schema của Stream A sắp được merge giữa
chừng.

### Stream C — Production Readiness (track Ops/Infra)

Phần còn lại của Phase 8 (tích hợp secret manager, load test, backup/restore drill) và quyết định
cutover thật sự của Phase 12. Cả hai đang bị block rõ ràng trong `docs/roadmap.md` bởi một điều
kiện tiên quyết còn thiếu: **chưa có quyết định về deployment topology cho production**
(`docs/architectures/11-risks.md`).

**Việc đầu tiên của stream này không phải code** — mà là một ADR chọn deployment topology
(`docs/architectures/09-adr.md`): thực tế sẽ chạy ở đâu, secret manager là gì, "production" nghĩa
là gì với một dự án ở giai đoạn này. Mọi thứ khác trong Phase 8/12 phụ thuộc vào quyết định đó và
không nên bắt đầu trước, để tránh harden theo một topology mà sau này lại chọn khác.

### Tóm tắt

```
Stream A (Backend Core)   ──viết spec──▶ implement control plane ──▶ mở khóa Phase 14
Stream B (App/Entity)     ──bắt đầu ngay, đồng bộ với A trước khi schema của A được merge
Stream C (Ops/Infra)      ──ADR chọn topology trước──▶ rồi Phase 8 + Phase 12 song song với A/B
```

A, B, C có thể chạy với ba người bắt đầu cùng tuần. Trong một stream, các bước con vẫn phải tuần
tự (spec của Stream A trước khi implement; ADR của Stream C trước khi làm hardening).
