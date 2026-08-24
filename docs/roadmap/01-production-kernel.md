## Phase 1: Production-shaped Platform Kernel

**Trạng thái: Đã xong.** Auth middleware, `RequestContext` (`tenantId`/`userId`/`roles`/`functionId`), structured error response kèm request/trace id, enforce tenant scope, outbox publisher worker, và service test cho CRUD/query đều đã có đủ. `defaultContext()` đã được thay thế hoàn toàn bằng real JWT-derived context — không còn code nào trong `src/` reference đến nó nữa. Một điểm lệch có chủ đích: không xây riêng class `TransactionManager`/`BaseRepository` — DB transaction được xử lý inline qua `db.client.transaction()` của Drizzle, và cách này đã đủ dùng cho đến nay (YAGNI thay vì abstraction sớm).

Mục tiêu:

- Thêm auth middleware.
- Thêm request context với `tenantId`, `userId`, `roles`, `functionId`.
- Thay thế default context trong `CrudService`.
- Enforce tenant scope ở mọi nơi.
- Thêm structured error response.
- Thêm request id và trace id.
- Thêm service test cho CRUD/query/metadata.
- Thêm outbox publisher worker.
- Thêm DB transaction helper.

Deliverables:

- `AuthService`
- `RequestContext`
- `TransactionManager`
- `OutboxPublisherWorker`
- `BaseRepository`
- migration thật đầu tiên

