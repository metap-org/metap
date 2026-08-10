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

**Đã xong (2026-08-10).** Phase 7 đóng đủ 4/4 module (`crm.customers`, `sales.orders`,
`inventory.movements`, `accounting.journal`) — chi tiết ở `docs/features/demo/`. Pattern
metadata-driven generalize tốt qua field kind/workflow shape/list view khác nhau mà không cần
đổi `crates/metap-*`. Không phát sinh nhu cầu cross-module workflow thật trong lúc làm.

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
Stream B (App/Entity)     ──đã xong (4/4 module, 2026-08-10)
Stream C (Ops/Infra)      ──ADR chọn topology trước──▶ rồi Phase 8 + Phase 12 song song với A/B
```

A, B, C có thể chạy với ba người bắt đầu cùng tuần. Trong một stream, các bước con vẫn phải tuần
tự (spec của Stream A trước khi implement; ADR của Stream C trước khi làm hardening).

## Định hướng đang ghi nhận, chưa có trigger — không phải stream, chưa nên bắt đầu

Nảy sinh từ các buổi thảo luận kiến trúc, hợp lý về mặt sản phẩm nhưng đi trước trigger-based
discipline hiện tại (`docs/architectures/02-constraints.md`'s "Tiến hóa theo trigger"). Ghi lại
ở đây để không quên, không phải để bắt đầu code — mỗi mục cần một feature brief trong
`docs/features/` (trạng thái `proposed`) nêu rõ trigger cụ thể trước khi ai đó bắt tay vào:

- **Workflow hai chế độ** (in-process trong một module, cross-module qua command/event) mà
  cùng một logical model chạy được ở cả hai, không rewrite khi deployment đổi. Đối lập trực
  tiếp với kết luận hiện tại trong `docs/architectures/09-adr.md`: `WorkflowRuntime` là một
  trong các Capability SPI **chưa có trigger**, chưa nên xây. Cần một trigger cụ thể (một
  module thứ hai thật sự cần cross-module workflow — Phase 7/Phase 9) trước khi đảo lại.
- **Workflow visualize được / hướng BPM nhẹ** — chưa có ở đâu trong roadmap hay entity nào hiện
  tại yêu cầu điều này. Giá trị sản phẩm hợp lý cho low-code, nhưng là yêu cầu mới, chưa phải
  kiến trúc đã quyết.
- **Tiny deployment profile** (single binary, SQLite, in-memory EventBus, không cần RabbitMQ)
  — đã được đặt tên trong `docs/modular-spi-architecture.md`'s Deployment Profiles, nhưng chính
  tài liệu đó khuyến nghị "Option 1: giữ một triết lý deployment duy nhất" cho hiện tại. Chọn
  Tiny nghĩa là sửa `docs/architectures/02-constraints.md`'s ràng buộc Postgres/RabbitMQ-duy-
  nhất và kiểm toán dialect Postgres-specific của `QueryPlanner` — một quyết định sản phẩm
  (có nhắm khách self-host không?), không phải gap kỹ thuật.
- **Migration path từ generic `records` table sang bảng riêng cho một entity** — chưa viết ở
  đâu. Chỉ nên viết thành spec khi Data Model Strategy Step 3
  (`docs/architectures/05-building-blocks.md`) thực sự được kích hoạt bởi một nhu cầu hiệu năng
  đo được của một entity cụ thể, không phải chuẩn bị sẵn trước.
