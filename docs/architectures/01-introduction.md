# 1. Giới thiệu và Mục tiêu

Metap duy trì một mô hình phát triển metadata-driven nhanh: khai báo metadata một lần, sau đó nhận được CRUD, list, workflow, audit, export, và UI metadata một cách nhất quán.

Điểm khác biệt là các helper chỉ là một facade, không phải là kiến trúc. Nền tảng được chia thành các service tường minh, mỗi service có một ranh giới cố định — xem [05. Building Block View](05-building-blocks.md).

## Tầm nhìn

Metap được thiết kế để trở thành backbone của một nền tảng low-code — chứ không phải một ứng dụng đơn mục đích. `crates/metap-*` (metadata, permission, query planner, workflow, outbox infra) là core platform tái sử dụng được — một Cargo workspace gồm các library crate, entity-agnostic (không biết gì về entity cụ thể); mỗi business app là một consumer binary riêng (vd: `apps/crm-server`), phụ thuộc vào `crates/metap-*` và chỉ đăng ký các entity của chính nó (xem [04. Solution Strategy](04-strategy.md) và [07. Deployment View](07-deployment.md)).

Đây là phiên bản ngắn gọn, "as-built" của tuyên bố đó — để có bức tranh định hướng đầy đủ hơn (tại sao low-code là đích đến cao hơn, điều đó có ý nghĩa gì với các quyết định được đưa ra bây giờ) xem `docs/vision.md`; để có một lộ trình theo pha cụ thể hướng tới phiên bản low-code đầu tiên, xem `docs/low-code-platform-v1.md`. Cả hai đều cố ý nằm ngoài bộ tài liệu arc42 này, vì chúng mô tả một đích đến, không phải những gì đã được triển khai.

## Tổng quan Yêu cầu

Nhóm theo stakeholder (bảng "Các bên liên quan" bên dưới) — mỗi gạch đầu dòng là một yêu cầu
chức năng **đã thực sự được đáp ứng** bởi kiến trúc hiện tại, không phải một backlog hay mục
tiêu tương lai (đó là việc của `docs/roadmap.md`/`docs/vision.md`).

**Entity Author (developer)**
- Khai báo một entity một lần (fields, list views, workflow) trong một Rust module duy nhất —
  không viết route/handler/repository riêng cho từng entity.
- Metadata sai (field trùng tên, list view tham chiếu field không tồn tại, workflow shape hỏng)
  phải bị chặn ngay lúc boot, không phải khi request đầu tiên chạm vào entity đó.

**End User**
- List/filter/sort/full-text-search trên mọi entity, giới hạn đúng bằng field mà entity đó khai
  báo là filterable/sortable/searchable trong metadata — không phải toán tử SQL tuỳ ý từ client.
- Thực hiện workflow transition có guard — chỉ thấy transition nào thật sự khả dụng ở state hiện
  tại (`capabilities.transitions`), không phải đoán/thử rồi nhận lỗi.
- Optimistic locking trên mọi update — không bao giờ bị ghi đè âm thầm bởi một write đồng thời
  khác (`409 version_conflict` thay vì mất dữ liệu trong im lặng).

**Admin**
- Gán/thu hồi role cho user trong tenant của mình, có hiệu lực ngay ở request tiếp theo (role
  luôn được tra mới, không cache trên JWT).
- Tạo policy RBAC/ABAC ở 3 mức (entity/field/record) qua API, không cần deploy lại code.
- Lên lịch job định kỳ (cron) gọi ngược vào chính platform hoặc một webhook ngoài, qua metadata
  chứ không phải một cron entry hard-code trong binary.

**Operator**
- Event (workflow transition, record thay đổi) phải tới được consumer phía sau kể cả khi
  RabbitMQ tạm thời down — không mất event, không chặn API availability.
- Server phải graceful-degrade (log cảnh báo, tiếp tục chạy) khi một phần hạ tầng gặp sự cố lúc
  boot (vd: DB không sẵn sàng cho index reconcile/drift check), không crash toàn bộ tiến trình.

Yêu cầu phi chức năng (non-functional) không lặp lại ở đây để tránh hai nguồn sự thật — xem
[10. Quality Requirements](10-quality.md) (quality scenario cụ thể, kiểm chứng được) và
[02. Architecture Constraints](02-constraints.md) (ràng buộc kỹ thuật/tổ chức). `docs/roadmap.md`
theo dõi chi tiết quá trình xây dựng theo từng pha; tài liệu này mô tả kiến trúc của những gì đã
thực sự được xây dựng.

## Các bên liên quan

| Vai trò | Mối quan tâm |
|---|---|
| End User | Sử dụng một business app được xây dựng trên Metap — records, lists, workflow actions |
| Admin | Quản lý việc gán role và các permission policy cho tenant của mình |
| Entity Author (developer) | Thêm một business entity mới bằng cách viết một entity-definition Rust module (xem `apps/crm-server/src/entities/customer_entity.rs` để biết pattern) — cần metadata contract dễ dự đoán và được validate lúc boot |
| Operator | Vận hành API server (`apps/crm-server`), outbox publisher worker (`outbox-publisher`), PostgreSQL, và RabbitMQ — cần khả năng graceful degradation khi xảy ra sự cố một phần |

## Mục tiêu Chất lượng (3 mục tiêu hàng đầu, chi tiết tại [10. Quality Requirements](10-quality.md))

1. **Tính đúng đắn / toàn vẹn dữ liệu (Correctness / data integrity)** — mọi business record có thể mutate đều dùng concurrency control tường minh: optimistic locking là chiến lược mặc định cho CRUD update, các thao tác đặc thù theo domain có thể dùng cơ chế concurrency mạnh hơn hoặc chuyên biệt khi cần. Transactional outbox đảm bảo một business change và event của nó không bao giờ lệch nhau.
2. **Bảo mật (Security)** — tenant scope và permission enforcement luôn diễn ra ở phía server; không có gì được tin tưởng từ client ngoài những gì metadata cho phép tường minh.
3. **Khả năng bảo trì (Maintainability)** — metadata được validate như một runtime artifact hạng nhất (fail lúc boot, không phải ở request đầu tiên), và mọi core service đều có một ranh giới được cố định ngay từ ngày đầu, ngay cả khi phần bên trong của nó vẫn còn là scaffold.
