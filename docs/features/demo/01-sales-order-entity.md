# Sales Order — module thứ hai (Phase 7)

- **Trạng thái:** done
- **Người đề xuất:** (thảo luận roadmap 2026-08-10)
- **Track sở hữu:** App/Entity
- **Phase roadmap liên quan:** Phase 7 (Module Migration Strategy)

## Vấn đề / động lực

`crm.customers` là entity duy nhất từng chạy trên stack Rust — mọi tuyên bố "Metap là backbone
generic, không phải app đơn lẻ" vẫn chỉ là giả thuyết chừng nào chưa có module thứ hai thật sự
dùng field kind/workflow shape khác. Đây cũng là cách rẻ nhất để có trigger thật cho các câu hỏi
đang treo ở `docs/team-charter.md`'s "Định hướng đang ghi nhận, chưa có trigger" (workflow
cross-module có thật sự cần không?).

## Phạm vi

**Trong phạm vi:**
- Entity `sales.orders` mới, đăng ký trong `apps/crm-server` cùng process với `crm.customers`
  (không phải binary/service riêng — đó là trigger của Phase 9, không phải Phase 7).
- Field kind chưa từng dùng: `Reference` (tới `crm.customers`), `Money`, `Date`.
- Workflow nhiều state hơn `crm.customers` (draft → confirmed → shipped, cộng nhánh cancel).

**Ngoài phạm vi:**
- Cross-module workflow (sales order không gọi ngược vào customer's workflow) — không có nhu
  cầu thật, xem ghi chú "chưa có trigger" ở `team-charter.md`.
- Inventory/accounting module (bước tiếp theo của Phase 7 theo thứ tự gợi ý trong roadmap).

## Tiêu chí chấp nhận

- `EntityDefinition` mới validate được lúc `MetadataRegistry::register()` (app boot không lỗi).
- Tạo được sales order tham chiếu một customer thật qua `POST /api/sales.orders`.
- `GET /api/sales.orders/:id` trả đúng `capabilities.transitions` theo state hiện tại (chỉ
  `confirm`/`cancel` available ở `draft`).
- Transition `confirm` → `ship` chạy được qua `POST /api/sales.orders/:id/transitions/:action`,
  optimistic locking hoạt động (version tăng đúng).
- `GET /api/sales.orders?customer=<id>` filter đúng theo field `Reference`.

Tất cả đã verify live trên dev stack thật (Postgres/RabbitMQ, `apps/crm-server` chạy qua
`pnpm dev:rs`), không chỉ `cargo check`/`cargo test`.

## Ranh giới kiến trúc bị đụng tới

Không. Không route/handler mới, không sqlx/lapin trực tiếp, entity vẫn đăng ký ở tầng
`apps/crm-server` như `customer_entity.rs` — không `metap-*` crate nào biết về
`sales.orders`. Không cần ADR.

## Rủi ro / phụ thuộc

Không phụ thuộc Stream A/C. Rủi ro duy nhất đã nêu ở `team-charter.md`: nếu Stream A
(metadata control plane) đổi shape `EntityDefinition` trong lúc module thứ ba đang được thêm,
cần đồng bộ trước khi merge.
