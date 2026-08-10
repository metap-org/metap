# Đóng góp cho Metap

Repo này do một người duy trì đến hết Phase 15 của `docs/roadmap.md`. Tài liệu này chuẩn bị cho
lúc điều đó không còn đúng nữa — một quy trình cụ thể, không cần bàn cãi, để một contributor mới có
thể merge một thay đổi mà không phải hỏi lại những thứ đáng lẽ đã được quyết định sẵn. Nếu bạn nhận
làm một phase trong roadmap, đọc thêm `docs/team-charter.md` để biết module nào thuộc ai và các
phase còn lại được chia thành các luồng làm song song ra sao.

## Trước khi bắt đầu

1. Đọc `CLAUDE.md` (ở root repo) — đây là mô tả chuẩn về stack, cấu trúc monorepo, các lệnh, và
   ranh giới kiến trúc mà mọi thay đổi phải tuân theo. Đây cũng chính là file instruction mà Claude
   Code tuân theo trong repo này, nên contributor là người hay có AI hỗ trợ đều theo cùng một bộ
   quy tắc.
2. Đọc `docs/architectures/index.md` và lướt qua các phần liên quan tới chỗ bạn sẽ đụng vào.
3. Xem `docs/roadmap.md` để biết thay đổi của bạn thuộc phase nào. Nếu không thuộc phase nào, ghi
   rõ điều đó trong PR description — không sao cả, nhưng phải nói rõ ra, không được mặc định.
   Nếu là một tính năng đủ lớn cần thống nhất phạm vi trước khi code, viết brief trong
   `docs/features/` trước (xem Definition of Ready trong `docs/agile-process.md`).
4. Setup môi trường theo mục Commands trong `CLAUDE.md` (Postgres/RabbitMQ qua `docker compose`,
   `pnpm install`, `pnpm db:migrate`, dev keys, v.v.) — không nhắc lại ở đây.

## Branch và PR

- Đặt tên branch: `phase-<n>-<slug>` cho việc thuộc roadmap (vd: `phase-7-sales-order`),
  `fix-<slug>` cho bug fix, `chore-<slug>` cho việc khác.
- Mỗi PR là một thay đổi logic duy nhất. Một phase roadmap có nhiều sub-project (xem phần chia
  work-stream trong `docs/team-charter.md`) nên tách thành nhiều PR, không gộp một PR khổng lồ —
  giống cách Phase 13/14/15 đã ship trong lịch sử repo này.
- PR description nêu rõ đang phục vụ phase/goal nào trong roadmap (hoặc nói rõ là không thuộc
  phase nào), và nêu rõ nếu có đụng tới ranh giới kiến trúc nào (xem "Ranh giới" bên dưới).
- Commit message: một dòng tóm tắt ngắn, mệnh lệnh cách (`Add Dynamic Cron Jobs (Phase 13)`,
  `Fix LoginForm not redirecting after a successful login`) — xem `git log` để theo đúng style
  hiện có. Không bắt buộc prefix kiểu conventional-commits.

## Các check bắt buộc trước khi mở PR

Chạy phần nào liên quan tới chỗ bạn sửa:

```bash
pnpm typecheck      # frontend
pnpm lint            # frontend
pnpm test            # frontend, vitest
cargo test --workspace              # backend unit test, không cần DB
cargo test --workspace -- --ignored # backend e2e — cần DATABASE_URL + Postgres/RabbitMQ đang chạy
```

`.github/workflows/ci.yml` chạy tương đương (job `rust`, `rust-e2e`, `frontend`) — hiện chưa phải
merge gate bắt buộc (chưa cấu hình branch protection, xem Phase 8 trong `docs/roadmap.md`), nên
trước khi việc đó thay đổi, coi CI là tham khảo và tự chạy check ở trên trước khi xin review.

Với bất kỳ thay đổi nào đụng tới `apps/crm-fe` hoặc UI của `packages/platform-react`, phải test tay
qua dev stack thật (`pnpm dev:rs` + `pnpm dev:web`) — typecheck/lint/unit test không bắt được bug
kiểu thiếu proxy entry trong Vite hay crash chỉ lộ ra khi click qua một luồng thao tác thật. Dự án
này đã từng gặp cả hai loại lỗi trên; xem ghi chú các phase gần đây trong `docs/roadmap.md`.

Với thay đổi ở tầng backend chạm tới một trong 4 entity demo (`apps/crm-server/src/entities/`),
`apps/crm-server/scripts/smoke.sh` chạy curl thủ công qua toàn bộ CRUD + workflow (kể cả nhánh
guard-fail) của cả 4 entity trên một dev stack thật — không phải test suite được commit, chỉ là
script lặp lại được để poke server bằng tay (`./apps/crm-server/scripts/smoke.sh` sau khi
`pnpm dev:rs` đã chạy).

Với thay đổi ở `crates/metap-permission`/`metap-http`'s admin routes,
`apps/crm-server/scripts/permission-smoke.sh` chạy tương tự nhưng cho model RBAC/ABAC: admin
route gating (401/403), context-level role restriction, field-level read/write gating,
record-level row filtering, và `PolicyExplainer` — tự dọn mọi policy/role nó tạo nên chạy lại
nhiều lần không tích tụ state.

## Ranh giới reviewer phải kiểm soát

Đây là quy ước bắt buộc của dự án (từ `CLAUDE.md`, nhắc lại ở
`docs/architectures/02-constraints.md`), không phải gợi ý về style — một PR vi phạm một trong các
điều dưới đây không được merge nếu chưa có mục ADR (`docs/architectures/09-adr.md`) giải thích lý
do ngoại lệ:

- Route/handler code (`crates/metap-http`) không được import `sqlx`/`lapin` trực tiếp — phải đi
  qua `CrudService` / `EventBus` của `metap-infra`.
- Input query từ client/frontend không được map trực tiếp sang toán tử SQL — phải đi qua
  `QueryPlanner`, bị ràng buộc bởi entity metadata.
- Side effect của workflow phải phát qua outbox, không publish thẳng lên RabbitMQ.
- Không crate thư viện `metap-*` nào được biết business-entity cụ thể — đó là việc của
  `apps/crm-server` (hoặc một binary thứ hai trong tương lai).
- Mọi business route đều giả định có tenant scope và tra `user_roles` thật cho mỗi request — role
  không bao giờ được cache trên JWT.

## Kỷ luật viết docs

- `docs/roadmap.md` là nguồn sự thật duy nhất cho trạng thái phase — cập nhật nó trong cùng PR làm
  thay đổi trạng thái phase đó, không để làm sau.
- Quyết định kiến trúc không tầm thường phải có mục trong `docs/architectures/09-adr.md`, không
  chỉ nằm trong comment code.
- Ngôn ngữ viết docs: xem `CLAUDE.md` để biết policy hiện hành (đang là tiếng Việt, xem ghi chú
  trong đó về việc chuyển sang tiếng Anh sau này).

## Review code

Route yêu cầu review theo module — xem bảng ownership trong `docs/team-charter.md` để biết track
nào (Backend Core, HTTP/API, Frontend Platform, App/Entity, Ops/Infra) sở hữu phần bạn vừa sửa.
Thay đổi đụng nhiều track cùng lúc (vd: đổi shape `EntityField` đụng cả `metap-metadata` lẫn
generated types của `packages/platform-react`) cần sign-off từ cả hai track.
