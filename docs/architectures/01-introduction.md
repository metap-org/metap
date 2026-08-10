# 1. Giới thiệu và Mục tiêu

Metap duy trì một mô hình phát triển metadata-driven nhanh: khai báo metadata một lần, sau đó nhận được CRUD, list, workflow, audit, export, và UI metadata một cách nhất quán.

Điểm khác biệt là các helper chỉ là một facade, không phải là kiến trúc. Nền tảng được chia thành các service tường minh, mỗi service có một ranh giới cố định — xem [05. Building Block View](05-building-blocks.md).

## Tầm nhìn

Metap được thiết kế để trở thành backbone của một nền tảng low-code — chứ không phải một ứng dụng đơn mục đích. `crates/metap-*` (metadata, permission, query planner, workflow, outbox infra) là core platform tái sử dụng được — một Cargo workspace gồm các library crate, entity-agnostic (không biết gì về entity cụ thể); mỗi business app là một consumer binary riêng (vd: `apps/crm-server`), phụ thuộc vào `crates/metap-*` và chỉ đăng ký các entity của chính nó (xem [04. Solution Strategy](04-strategy.md) và [07. Deployment View](07-deployment.md)).

Đây là phiên bản ngắn gọn, "as-built" của tuyên bố đó — để có bức tranh định hướng đầy đủ hơn (tại sao low-code là đích đến cao hơn, điều đó có ý nghĩa gì với các quyết định được đưa ra bây giờ) xem `docs/vision.md`; để có một lộ trình theo pha cụ thể hướng tới phiên bản low-code đầu tiên, xem `docs/low-code-platform-v1.md`. Cả hai đều cố ý nằm ngoài bộ tài liệu arc42 này, vì chúng mô tả một đích đến, không phải những gì đã được triển khai.

## Tổng quan Yêu cầu

- Khai báo một entity một lần (fields, list views, workflow) và có được CRUD, list/filter/sort, permission enforcement, và workflow behavior cho nó — không cần boilerplate route/handler/repository theo từng entity.
- Mọi business record đều được tenant-scoped; không một query, read, hay write nào có thể vượt qua ranh giới tenant.
- Kiểm soát truy cập ở cấp field và record được điều khiển bởi metadata/policy, không hardcode theo từng entity.
- Đảm bảo event delivery đáng tin cậy cho các consumer phía sau (workflow transitions, record changes) mà không mất event khi message broker tạm thời không khả dụng.
- `docs/roadmap.md` theo dõi chi tiết quá trình xây dựng theo từng pha; tài liệu này mô tả kiến trúc của những gì đã thực sự được xây dựng, không phải một mục tiêu chưa được triển khai.

## Các bên liên quan

| Vai trò | Mối quan tâm |
|---|---|
| End User | Sử dụng một business app được xây dựng trên Metap — records, lists, workflow actions |
| Admin | Quản lý việc gán role và các permission policy cho tenant của mình |
| Entity Author (developer) | Thêm một business entity mới bằng cách viết một entity-definition Rust module (xem `apps/crm-server/src/customer_entity.rs` để biết pattern) — cần metadata contract dễ dự đoán và được validate lúc boot |
| Operator | Vận hành API server (`apps/crm-server`), outbox publisher worker (`outbox-publisher`), PostgreSQL, và RabbitMQ — cần khả năng graceful degradation khi xảy ra sự cố một phần |

## Mục tiêu Chất lượng (3 mục tiêu hàng đầu, chi tiết tại [10. Quality Requirements](10-quality.md))

1. **Tính đúng đắn / toàn vẹn dữ liệu (Correctness / data integrity)** — mọi business record có thể mutate đều dùng concurrency control tường minh: optimistic locking là chiến lược mặc định cho CRUD update, các thao tác đặc thù theo domain có thể dùng cơ chế concurrency mạnh hơn hoặc chuyên biệt khi cần. Transactional outbox đảm bảo một business change và event của nó không bao giờ lệch nhau.
2. **Bảo mật (Security)** — tenant scope và permission enforcement luôn diễn ra ở phía server; không có gì được tin tưởng từ client ngoài những gì metadata cho phép tường minh.
3. **Khả năng bảo trì (Maintainability)** — metadata được validate như một runtime artifact hạng nhất (fail lúc boot, không phải ở request đầu tiên), và mọi core service đều có một ranh giới được cố định ngay từ ngày đầu, ngay cả khi phần bên trong của nó vẫn còn là scaffold.
