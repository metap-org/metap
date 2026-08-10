# Vì sao chọn stack này

Stack đã chọn (vẫn là stack đang thực sự được deploy — stack TS/Node mà tài liệu này giải thích):

```txt
Fastify + Zod + Drizzle + PostgreSQL + RabbitMQ + Outbox Pattern
```

**2026-08-07:** `packages/core` hiện đã được quyết định chuyển sang Rust
(`docs/rust-core-viability.md`), việc này tái sử dụng nguyên vẹn các lựa chọn
PostgreSQL/RabbitMQ/outbox pattern bên dưới — chỉ có `Fastify`/`Zod`/`Drizzle` (lớp
framework/validation/ORM) bị thay thế (lần lượt bằng `axum`/validation viết tay dựa trên field
metadata/`sqlx`). Phần lý giải của tài liệu này cho ba lựa chọn đó là bối cảnh lịch sử cho lý do
chúng được chọn ban đầu, không phải một so sánh còn đang mở.

## Vì sao chọn Fastify

Fastify là lựa chọn phù hợp cho một metadata-driven ERP core vì nó:

- nhanh khi chạy runtime
- khởi động nhẹ
- explicit (tường minh)
- thân thiện với plugin
- ít nghi thức (ceremony) hơn NestJS
- dễ giữ sát với kiến trúc platform hơn

NestJS có năng suất tốt, nhưng nó thêm vào decorator, reflection, nghi thức module, và overhead
khi build/runtime. Metap nên giữ overhead của framework ở mức thấp và đặt kiến trúc vào các core
module của riêng mình.

## Vì sao chọn Zod

Zod quen thuộc và dễ đọc đối với các team TypeScript.

Dùng nó cho:

- validate environment config
- validate route payload
- schema input cho entity metadata
- sinh API docs thông qua chuyển đổi sang JSON schema

TypeBox nhanh hơn cho các app theo hướng JSON schema-first, nhưng Zod dễ onboard hơn và đủ linh
hoạt cho giai đoạn này.

## Vì sao chọn Drizzle

Drizzle được chọn thay vì Prisma vì ERP core này cần:

- build và runtime nhanh
- ít "magic"
- thiết kế thân thiện với SQL
- suy luận kiểu TypeScript mạnh
- hỗ trợ PostgreSQL tốt
- dễ dùng JSONB
- kiểm soát trực tiếp hình dạng query

Prisma vẫn là lựa chọn tốt cho các team muốn tối đa sự thoải mái khi onboard. Đánh đổi là
generated client/runtime nặng hơn và ít kiểm soát SQL trực tiếp hơn cho các báo cáo ERP phức tạp.

Drizzle phù hợp hơn với mục tiêu: có năng suất, nhưng vẫn đủ gần với SQL để việc tuning hiệu năng
luôn đơn giản.

## Vì sao chọn PostgreSQL

PostgreSQL là system of record.

So với MongoDB, nó hỗ trợ tốt hơn cho:

- transaction
- constraint
- tính toàn vẹn quan hệ (relational integrity)
- SQL cho reporting
- row lock
- index
- materialized view
- JSONB cho các field metadata động

Metap vẫn giữ phong cách phát triển linh hoạt (dynamic) thông qua `jsonb`, nhưng dùng PostgreSQL
để làm cho dữ liệu accounting, inventory, và dữ liệu nhạy cảm về permission an toàn hơn.

## Vì sao chọn RabbitMQ

RabbitMQ phù hợp cho ERP vì các module cần các integration event đáng tin cậy:

- workflow transitioned
- record created/updated
- notification requested
- export requested
- file uploaded
- webhook dispatch requested

RabbitMQ tốt hơn một in-memory queue cho việc tích hợp ERP nhiều service (multi-service).

## Vì sao chọn Outbox Pattern

Publish trực tiếp lên RabbitMQ bên trong một API request có thể làm mất event:

1. DB commit thành công.
2. RabbitMQ publish thất bại.
3. Thay đổi business đã tồn tại, nhưng các module khác không bao giờ biết về nó.

Outbox pattern khắc phục điều này:

1. Ghi business data và outbox event trong cùng một DB transaction.
2. Background publisher rút (drain) các row trong outbox.
3. RabbitMQ nhận event một cách đáng tin cậy.
4. Các lần publish thất bại có thể retry.

## Vì sao giữ Metadata-driven Core

Một metadata-driven core hoạt động tốt cho tốc độ phát triển ERP. Metap giữ lại:

- CRUD tổng quát
- metadata list/form tổng quát
- workflow metadata
- định nghĩa field có thể tái sử dụng
- hành vi được sinh ra có nhận biết permission (permission-aware)

Mục tiêu của việc rewrite không phải là giảm abstraction. Mục tiêu là abstraction sạch hơn.
