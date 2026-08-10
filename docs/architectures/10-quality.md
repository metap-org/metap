# 10. Quality Requirements

## Quality Tree

1. **Correctness / Data Integrity** — một write không bao giờ được âm thầm làm hỏng hoặc mất dữ liệu, kể cả khi có concurrency hay hạ tầng gặp sự cố một phần.
2. **Security** — cô lập tenant và enforce permission phải giữ vững bất kể client gửi gì lên; không có gì liên quan đến bảo mật được tin tưởng từ phía client.
3. **Maintainability** — một entity definition sai hoặc một vi phạm layering phải được phát hiện sớm (lúc boot hoặc lúc review), không phải khi đã lên production.
4. **Performance** — các thao tác list/filter/search trên hot field phải được backed bởi index thật, không phải full scan, và không bao giờ trả về tập kết quả không giới hạn.

## Quality Scenarios (cụ thể, kiểm chứng được)

| # | Scenario | Mechanism | Verified by |
|---|---|---|---|
| 1 | Hai client update cùng một record đồng thời | `UPDATE ... WHERE version = $expected_version`; bên thua trả về `409 version_conflict`, không bao giờ bị ghi đè âm thầm | `metap-crud` tests |
| 2 | RabbitMQ không thể kết nối khi một record được tạo | Business write vẫn commit bình thường (outbox pattern); event được gửi đi ngay khi publisher kết nối lại được với RabbitMQ | `outbox-publisher`/`metap-infra` tests |
| 3 | Một admin list record trong khi tồn tại một policy mức record scope cho một role không phải admin | Bypass cho admin được kiểm tra trước tiên trong `record_policy_where_clause` — kết quả của admin không bao giờ bị làm rỗng bởi một policy không áp dụng cho họ | `metap-crud`/`metap-query` tests (regression test, ban đầu được thêm trong codebase TS 2026-08-01 sau khi phát hiện đây là bug thật, được triển khai lại trong bản port Rust) |
| 4 | Một policy đọc mức field từ chối một field được mirror vào một cột top-level của `records` (`code`/`status`) | Đường mask record của `CrudService` mask cả bản sao JSONB lẫn cột mirror, không chỉ một trong hai | `metap-crud` tests |
| 5 | Một entity module mới có tên field trùng lặp hoặc một listView tham chiếu tới field không tồn tại | `MetadataCompiler::validate` báo lỗi tại `MetadataRegistry::register()` — app không boot lên được, không phải lỗi ở request đầu tiên | `metap-metadata` tests |
| 6 | Database không kết nối được khi `IndexReconciler`/`MetadataDriftService` chạy lúc boot | Cả hai đều bắt lỗi và ghi log cảnh báo; quá trình boot vẫn tiếp tục | `metap-peripherals` tests |
| 7 | Client gửi lên một cursor được sinh từ một sort khác, hoặc một chuỗi cursor rác | `plan_list` trả về lỗi cursor không hợp lệ, `CrudService` map thành `400 invalid_cursor` — không bao giờ là `500` | `metap-query` tests |
| 8 | Client gửi lên một giá trị filter mang tính tấn công (ví dụ `"active' OR '1'='1"`) | Được xử lý như dữ liệu literal thông qua bound SQL parameter (`sqlx`) — không bao giờ được nối chuỗi trực tiếp vào query | `metap-query` tests |

Chạy unit test (không cần DB) bằng `pnpm test:rs` (`cargo test --workspace`); chạy e2e test (Postgres/RabbitMQ thật, HTTP server live, JWT RS256 thật) bằng `pnpm test:rs:e2e` (`cargo test --workspace -- --ignored`).

## Ghi chú

- Chưa có load/performance testing — quality scenario 4 (Performance) ở trên được kiểm chứng bằng thiết kế (regression test kiểm tra việc dùng index bằng `EXPLAIN`) chứ chưa bằng throughput/latency đo được dưới tải thực tế. Được ghi nhận ở [11. Risks and Technical Debt](11-risks.md).
- Phạm vi test trong codebase này chủ ý ở mức tối thiểu và có mục tiêu rõ ràng (một số case quan trọng cho mỗi feature), không phải một ma trận toàn diện — xem `docs/roadmap.md` Phase 12 để biết số lượng test hiện tại.
