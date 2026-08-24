## Phase 7: Module Migration Strategy

**Trạng thái: Đã xong (2026-08-10).** Mục tiêu: chứng minh pattern metadata-driven generalize
được qua nhiều module khác nhau (field kind khác, workflow shape khác, list view khác), không
chỉ đúng cho `crm.customers`. Cả 4 module đăng ký cùng process trong `apps/crm-server` — tách
thành binary/service riêng là trigger của Phase 9, không phải Phase 7.

Mục tiêu:

- ~~Port một module master-data đơn giản~~ — **Đã xong**: `crm.customers`
  (`apps/crm-server/src/entities/customer_entity.rs`), có từ bản port Rust.
- ~~Port một module transaction~~ — **Đã xong (2026-08-10)**: `sales.orders`
  (`apps/crm-server/src/entities/sales_order_entity.rs`) — field kind mới (`Reference` tới
  `crm.customers`, `Money`, `Date`), workflow 4 state (draft/confirmed/shipped/cancelled). Chi
  tiết + tiêu chí chấp nhận đã verify live ở `docs/features/demo/01-sales-order-entity.md`.
- ~~Port một module nặng về workflow~~ — **Đã xong (2026-08-10)**: `inventory.movements`
  (`apps/crm-server/src/entities/inventory_movement_entity.rs`) — 6 state, nhánh rẽ approve/reject, và
  một transition (`reverse`) đi ra khỏi state không phải initial; guard trên field `Number`.
  Chi tiết + tiêu chí chấp nhận đã verify live ở
  `docs/features/demo/02-inventory-movement-entity.md`.
- ~~Port một flow report/export~~ — **Đã xong (2026-08-10)**: `accounting.journal`
  (`apps/crm-server/src/entities/journal_entry_entity.rs`) — 2 list view trên cùng entity (`default`,
  `ledger`) chứng minh "report" là một list view khai báo qua metadata, không phải backend
  mới (nền tảng chưa có đường query report/analytics riêng, xem `11-risks.md` — cố tình chưa
  xây); guard đầu tiên dùng `PolicyCondition::Any`. Chi tiết + tiêu chí chấp nhận đã verify
  live ở `docs/features/demo/03-journal-entry-entity.md`.

**Kết luận Phase 7:** pattern metadata-driven (field kind, workflow — kể cả nhánh rẽ và
transition ngược, list view kép, guard đơn/`Any`) generalize tốt qua 4 entity khác nhau mà
không cần đổi gì ở `crates/metap-*`. Không phát sinh nhu cầu cross-module workflow thật trong
lúc làm — củng cố (chưa phải xác nhận dứt khoát) hướng "chưa có trigger" đã ghi ở
`docs/team-charter.md` cho ý tưởng workflow hai chế độ.

