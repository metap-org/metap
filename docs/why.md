# Vì sao chọn stack này

Stack đã chọn:

```txt
axum + sqlx + PostgreSQL + RabbitMQ + Outbox Pattern
```

Chi tiết lựa chọn framework/DB-access (axum/sqlx) nằm ở
[09. Architecture Decisions](architectures/09-adr.md) và
[04. Solution Strategy](architectures/04-strategy.md). Tài liệu này giải thích lý do đằng sau
các lựa chọn nền tảng bên dưới — PostgreSQL, RabbitMQ, Outbox Pattern, và cách tiếp cận
metadata-driven — những lựa chọn không đổi qua lần chuyển ngôn ngữ implementation.

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

RabbitMQ phù hợp vì các module cần các integration event đáng tin cậy:

- workflow transitioned
- record created/updated
- notification requested
- export requested
- file uploaded
- webhook dispatch requested

RabbitMQ tốt hơn một in-memory queue cho việc tích hợp nhiều service (multi-service).

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

Một metadata-driven core hoạt động tốt cho tốc độ phát triển. Metap giữ lại:

- CRUD tổng quát
- metadata list/form tổng quát
- workflow metadata
- định nghĩa field có thể tái sử dụng
- hành vi được sinh ra có nhận biết permission (permission-aware)
