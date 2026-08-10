# Inventory Movement — module thứ ba (Phase 7, workflow nặng)

- **Trạng thái:** done
- **Người đề xuất:** (thảo luận roadmap 2026-08-10)
- **Track sở hữu:** App/Entity
- **Phase roadmap liên quan:** Phase 7 (Module Migration Strategy)

## Vấn đề / động lực

`sales.orders` chứng minh field kind mới nhưng workflow của nó vẫn tuyến tính (draft →
confirmed → shipped/cancelled). Mục tiêu roadmap Phase 7 gốc tách riêng một module "nặng về
workflow" — cần một shape phức tạp hơn: có nhánh rẽ (approve/reject) và một transition đi ra
khỏi một state không phải initial (`reverse` từ `posted`, pattern "undo một hành động đã chốt"
rất phổ biến trong nghiệp vụ thật nhưng chưa entity nào ở đây test tới).

## Phạm vi

**Trong phạm vi:**
- Entity `inventory.movements`, đăng ký cùng process với 2 entity trước.
- Workflow 6 state, 5 transition, có nhánh rẽ và một transition xuất phát từ state không phải
  initial/không phải "chỉ tiến".
- `referenceOrder`: field `Reference` optional, minh hoạ một field có thể trỏ tới entity khác
  tuỳ theo record — không đăng ký gì đặc biệt ở platform, chỉ khai báo `refEntity` trong
  metadata (đã trỏ sang `sales.orders` thay vì `crm.customers`).
- Guard trên `submit` dùng field kiểu `Number` (`quantity != 0`) — trước giờ guard duy nhất có
  (ở `customer_entity.rs`/`sales_order_entity.rs`) chỉ test trên field `String`/`Reference`.

**Ngoài phạm vi:**
- Entity `Warehouse`/`Product` riêng — `warehouse` vẫn là field `String` tự do, không phải
  reference tới một entity kho thật (chưa cần, chưa có trigger).
- Cross-module workflow thật (movement không tự động cập nhật ngược `sales.orders`).

## Tiêu chí chấp nhận

- Guard `quantity != 0` chặn đúng: tạo movement với `quantity=0`, `GET` trả
  `transitions: [{action: "submit", available: false, reason: ...}]`, và gọi thẳng
  `POST .../transitions/submit` trả `400 guard_failed` với message rõ ràng.
- Chuỗi đầy đủ `submit → approve → post → reverse` chạy được, mỗi bước version tăng đúng 1.
- Nhánh `reject` (từ `pending_approval`) hoạt động độc lập, không phụ thuộc nhánh approve.
- `referenceOrder` filter/hiển thị đúng khi trỏ tới một `sales.orders` record thật.

Tất cả đã verify live trên dev stack thật (không chỉ `cargo check`/test), bao gồm cả trường hợp
lỗi (guard fail) — không chỉ happy path.

## Ranh giới kiến trúc bị đụng tới

Không. Cùng pattern với `customer_entity.rs`/`sales_order_entity.rs` — không route mới, không
`metap-*` crate nào biết về entity cụ thể. Không cần ADR.

## Rủi ro / phụ thuộc

Không phụ thuộc Stream A/C. Cùng rủi ro đã nêu ở module 2: nếu Stream A đổi shape
`EntityDefinition` giữa lúc module thứ tư (accounting journal) đang được thêm, cần đồng bộ
trước khi merge.
