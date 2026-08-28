## Phase 46: `rust-e2e` ra khỏi CI tự động + sửa gốc 2 test flaky (2026-08-28)

Trigger: PR #6 (Increment 2) hit CI đỏ 3 lần liên tiếp ở `rust-e2e`, mỗi lần một lỗi khác nhau,
đều không liên quan diff — chủ dự án chỉ ra đúng: e2e phụ thuộc quá nhiều vào timing/data-volume
của môi trường CI song song, và job này còn làm CI chậm hẳn so với 4 job kia cộng lại. Quyết
định: sửa gốc 2 lỗi flaky đã tìm được, và chuyển hẳn `rust-e2e` ra khỏi gate tự động — coi ngang
hàng với security checklist/performance benchmark trong `testing/` (coverage thật, chạy chủ động,
không phải gate mọi commit), không phải bỏ hẳn.

**Sửa gốc 2 test flaky (không phải "retry cho qua"):**

1. `crates/metap-peripherals/tests/peripherals_postgres.rs` — 2 test khẳng định Postgres planner
   *thực sự chọn* index vừa tạo (`reconcile_creates_an_index_postgres_actually_selects_for_the_exact_query_form`,
   `reconcile_creates_a_trigram_index_postgres_actually_selects_for_ilike`), không chỉ index tồn
   tại. Root cause tìm được qua verify trực tiếp bằng `psql` (không suy đoán): với 0 hoặc rất ít
   row cho entity test riêng, planner hợp lý chọn Seq Scan (bảng `records` chung nhỏ) hoặc chọn
   index composite `(tenant_id, entity, created_at)` có sẵn thay vì index field-cụ thể vừa tạo —
   cả hai đều là lựa chọn cost-based *đúng* của Postgres, không phải bug, chỉ là điều kiện test
   không phản ánh được ý định thật ("planner chọn đúng index này"). Fix 2 phần:
   - `SET LOCAL enable_seqscan = off` (trong 1 transaction, rollback sau `EXPLAIN`, không rò sang
     test khác qua connection pool dùng chung) — loại trừ khả năng Seq Scan thắng chỉ vì bảng
     chung đang nhỏ lúc test này chạy.
   - Với riêng test trigram/`ILIKE`: seed thêm 15.000 row cùng entity trước khi `EXPLAIN` (ngưỡng
     thật đo được qua `psql`: 10.000 row là điểm index trigram bắt đầu thắng composite index khi
     đã tắt seqscan; 15.000 để có margin) — khớp đúng lý do trigram index chỉ thắng ở khối lượng
     dữ liệu thật (đã chứng minh ở benchmark 500K-1M row của Phase 17), không phải artifact cần
     né tránh.
   - Phát hiện thêm khi verify lặp lại nhiều lần: 2 test này còn nhiễu lẫn nhau khi chạy song song
     trong cùng file (cùng đụng bảng `records` chung) — mở rộng phạm vi `INDEX_BUILD_LOCK` (mutex
     có sẵn, trước đó chỉ bọc `reconcile_indexes` để tránh deadlock `CREATE INDEX CONCURRENTLY`
     kép) để bọc luôn cả đoạn seed+`ANALYZE`+`EXPLAIN`, không chỉ phần tạo index.
   - Verify: 10/10 lần chạy `cargo test -p metap-peripherals --test peripherals_postgres --
     --ignored` (test-threads 1/4/8 khác nhau) pass liên tục sau fix, so với fail ngẫu nhiên
     trước đó.
2. `crates/metap-infra/tests/handler_registry_rabbitmq.rs` — 2 test publish message trước khi
   subscription kịp bind vào RabbitMQ (message bị drop âm thầm, không lỗi, không phải redeliver
   — root cause đã ghi sẵn trong comment cũ dẫn từ `retry_policy_rabbitmq.rs`). Sửa: tăng delay
   chờ trước khi publish từ 500ms lên 2000ms — không phải deterministic wait thật (cần đổi API
   `run_resilient_consumer` dùng chung cho `cron-scheduler`/`notification-worker`, ngoài phạm vi
   sửa 2 test flaky), nhưng đủ margin rộng dưới tải CI song song thực tế đã quan sát. Verify: 3/3
   lần chạy pass sau fix.

**`rust-e2e` ra khỏi `ci.yml`, sang `.github/workflows/e2e-manual.yml` (`workflow_dispatch` only,
không push/PR/schedule)** — cùng convention `nightly-benchmark.yml` đã dùng (file YAML riêng cho
job không-blocking). `ci.yml` giờ còn 4 job tự động (`rust`/`security`/`semgrep`/`frontend`), tất
cả không cần service container ngoài. Chạy e2e: `pnpm test:rs:e2e` ở dev (không đổi, đã có sẵn
trong `docs/CONTRIBUTING.md`), hoặc trigger `e2e-manual.yml` thủ công trên GitHub Actions. Cập
nhật 3 file docs đang mô tả cấu trúc CI cũ: `testing/regression/README.md` (bảng job),
`testing/README.md`, `docs/CONTRIBUTING.md` — các entry roadmap lịch sử (Phase 8/11/20) giữ
nguyên làm bản ghi thời điểm quyết định, không sửa lại.

`cargo build/clippy --all-targets -D warnings/fmt --check` sạch toàn workspace sau mọi thay đổi.
