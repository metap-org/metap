# 2. Ràng buộc Kiến trúc

## Ràng buộc Kỹ thuật

- **Stack đã cố định**: Rust (axum + sqlx) + PostgreSQL + RabbitMQ (outbox pattern). Xem `docs/why.md` để biết lý do đằng sau các lựa chọn có từ trước khi migrate sang Rust (PostgreSQL, RabbitMQ, outbox pattern) và [09. Architecture Decisions](09-adr.md) để biết quyết định chuyển execution engine từ TypeScript/Fastify/Zod/Drizzle sang Rust/axum/sqlx — không nhắc lại ở đây.
- **Một Rust toolchain stable gần đây**, chưa pin MSRV; workspace dùng edition 2021 (`resolver = "2"` trong `Cargo.toml`). Không có vấn đề CommonJS/ESM ở backend — ràng buộc đó chỉ áp dụng cho frontend (`apps/crm-fe`/`packages/platform-react`, Node >=24.15.0, ESM xuyên suốt).
- **Chiến lược persistence ban đầu dùng một bảng `records` generic duy nhất**, không có bảng riêng cho từng entity — đây là implementation strategy hiện tại, không phải một ràng buộc kiến trúc vĩnh viễn. Dữ liệu của mọi business entity nằm trong `records.data jsonb`; không có schema migration cho mỗi entity mới, chỉ cần một entity-definition Rust module mới (xem `apps/crm-server/src/entities/customer_entity.rs`). Trigger và migration path sang bảng riêng cho từng entity đã được ghi lại ở [05. Building Block View](05-building-blocks.md#data-model)'s Data Model Strategy (Step 3) và [11. Risks and Technical Debt](11-risks.md)'s hàng `records` table — không cần "architecture reset" để đổi hướng khi trigger đó xảy ra, chỉ cần đi theo lộ trình đã vạch sẵn.
- **PostgreSQL là datastore hệ thống của business record.** Không có search engine riêng (full-text search dùng Postgres `tsvector`/GIN, không dùng Elasticsearch). Có một cache layer opt-in (`metap-cache`, `docs/roadmap.md` Phase 23): trait `Cache` với `MokaCache` (in-process) và `RedisCache` (qua bất kỳ server tương thích RESP nào — Redis/DragonflyDB/Valkey/KeyDB), dùng cho `PermissionService::with_cache` cache policy-row lookup — role/`user_roles` không bao giờ được cache. Có object storage opt-in tương tự (`metap-storage`, Phase 22): `S3ObjectStore` qua bất kỳ backend tương thích S3 API nào (SeaweedFS local dev), được `metap-attachments` (Phase 27) dùng làm backend lưu file cho route generic `/api/{entity}/{id}/attachments*`.
- **RabbitMQ là message broker duy nhất** cho các outbound event — không dùng Kafka, không dùng SNS/SQS. (`metap-infra::EventBus` là một trait với một implementation duy nhất, `RabbitEventBus`; xem [05. Building Block View](05-building-blocks.md#event-bus) — đây là một seam đã có sẵn, không phải kế hoạch thêm broker thứ hai.)

## Ràng buộc Tổ chức

- **Các quyết định kiến trúc không tầm thường đều được ghi lại**, không chỉ code âm thầm — xem [09. Architecture Decisions](09-adr.md), decision log của dự án. (Đến trước 2026-08-07, việc này đi qua một chu trình formal spec → plan → implementation dưới `docs/superpowers/{specs,plans}/`; thư mục đó đã bị xóa để giảm ceremony/context overhead, và các quyết định giờ được ghi trực tiếp vào `09-adr.md` hoặc file `docs/*.md` liên quan.)
- **`docs/roadmap.md` là single source of truth cho biết dự án đang ở phase nào** — tài liệu này mô tả kiến trúc của những gì đã thực sự được ship, có tham chiếu chéo tới các roadmap phase khi liên quan, không phải một mục tiêu chưa được xây dựng.
- **Tiến hóa theo trigger (trigger-based evolution)**: hạ tầng mang tính speculative (bảng riêng cho từng entity, một report/analytics query path) không được xây trước khi có một trigger cụ thể. Ngoại lệ có chủ ý duy nhất: việc tách workspace/module-packaging (`crates/metap-*` + `apps/<consumer>`) đã được đẩy lên sớm hơn trigger đã tài liệu hóa ban đầu của nó (một second module thực sự) — xem [04. Solution Strategy](04-strategy.md) và [11. Risks and Technical Debt](11-risks.md).
- **Việc sở hữu module (module ownership) và định tuyến review giữa các track**, cho thời điểm repo này không còn do một người duy trì nữa, được theo dõi tại `docs/team-charter.md` thay vì ở đây — tài liệu đó cũng chia các roadmap phase còn lại thành các work stream có thể chạy song song. `docs/CONTRIBUTING.md` bao quát quy trình đóng góp mang tính cơ học (branching, các check bắt buộc).

## Quy ước (bắt buộc, theo `CLAUDE.md`)

- Code route/handler không được import `sqlx`/`lapin` trực tiếp — phải đi qua `CrudService` (`metap-crud`) / `EventBus` của `metap-infra`.
- Query input từ frontend/client không bao giờ được map trực tiếp sang SQL operator — nó phải đi qua `QueryPlanner` (`metap-query`), bị ràng buộc bởi entity metadata.
- Các workflow side effect được emit qua outbox, không bao giờ publish trực tiếp lên RabbitMQ từ một service.
- Mọi business route đều giả định có tenant scope.
- Không có `metap-*` library crate nào được biết về business entity — đó là việc của `apps/crm-server` (hoặc một binary thứ hai trong tương lai).
