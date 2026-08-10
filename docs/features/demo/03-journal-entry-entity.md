# Journal Entry — module thứ tư, cuối Phase 7 (report/export flow)

- **Trạng thái:** done
- **Người đề xuất:** (thảo luận roadmap 2026-08-10)
- **Track sở hữu:** App/Entity
- **Phase roadmap liên quan:** Phase 7 (Module Migration Strategy) — module cuối, đóng phase.

## Vấn đề / động lực

Ba module trước test field kind và workflow shape. Module cuối của Phase 7 theo roadmap gốc là
một "flow report/export" — nhưng `docs/architectures/11-risks.md` đã ghi rõ nền tảng **chưa có**
đường query report/analytics riêng (deliberately chưa xây, chưa có trigger). Nên phạm vi ở đây
không phải "xây tính năng export mới", mà là chứng minh: report = một `EntityListView` thứ hai
trên cùng entity, khai báo qua metadata, không cần route/backend mới.

## Phạm vi

**Trong phạm vi:**
- Entity `accounting.journal`, list view thứ hai (`ledger`) khai báo bên cạnh `default` trong
  metadata — khác field hiển thị, khác default sort, khác filter set. (Xem phát hiện quan trọng
  ở "Tiêu chí chấp nhận": chưa có cách chọn view này qua API list thật, chỉ mới khai báo được.)
- Guard dùng `PolicyCondition::Any` lần đầu trong codebase này (mọi guard trước chỉ có một
  `Attribute` condition) — "debit hoặc credit phải khác 0".
- `referenceMovement`: field `Reference` thứ ba trỏ tới `inventory.movements`.

**Ngoài phạm vi (cố ý, không phải thiếu sót):**
- Endpoint export CSV/Excel thật — không tồn tại ở đâu trong `metap-http`, không thêm ở đây.
- Đường query report/analytics tách khỏi OLTP `records` table — đúng risk đã ghi ở
  `11-risks.md`, chưa có trigger (chưa có consumer report thật).
- Double-entry balancing thật (tổng debit = tổng credit toàn bộ journal) — cần một aggregate
  query không có trong `QueryPlanner` hiện tại; ngoài phạm vi module chứng minh pattern.

## Tiêu chí chấp nhận

- `GET /metadata/entities/accounting.journal` trả về đủ 2 list view (`default`, `ledger`).
- Guard `Any[debitAmount!=0, creditAmount!=0]`: entry với cả hai bằng 0 → `post` không
  available, gọi thẳng trả `400 guard_failed`; entry với một trong hai khác 0 → `post` thành
  công.
- `post → void` chạy đúng, version tăng đúng.
- `GET /api/accounting.journal?account=<code>` filter đúng (dùng list view `default`).

Tất cả đã verify live, bao gồm cả hai nhánh guard (fail và pass), không chỉ happy path.

**Phát hiện thật trong lúc verify (không phải giả định):** `ledger` — list view thứ hai — thực
ra **không thể gọi được qua API list hiện tại**. `crates/metap-query/src/query_planner.rs`'s
`plan_list` luôn dùng `entity.list_views.first()`, không có cách chọn list view khác qua query
param hay path. Vậy `EntityListView` nhiều-hơn-một hiện chỉ có tác dụng ở metadata
(`GET /metadata/entities/...`), chưa có tác dụng gì ở list API thật. Đã ghi lại thành risk mới
ở `docs/architectures/11-risks.md` thay vì âm thầm bỏ qua.

## Ranh giới kiến trúc bị đụng tới

Không. Cùng pattern với 3 module trước — không route mới, không đụng `QueryPlanner`/
`metap-http`. Không cần ADR.

## Rủi ro / phụ thuộc

Không. Đây là module cuối của Phase 7 — không có module 5 nào phụ thuộc vào nó trong phạm vi
hiện tại. Rủi ro đồng bộ với Stream A (đổi shape `EntityDefinition`) không còn áp dụng vì
Phase 7 coi như đóng sau module này.
