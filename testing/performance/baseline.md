# Performance baseline (direct mode)

Số liệu tham chiếu cho chế độ **direct** (`crates/metap-crud/tests/support/mod.rs`, bỏ qua HTTP,
đo thẳng `CrudService`) — xem `testing/README.md`'s Performance section. Không có
seed/nightly-workflow tự động so lệch với các số 10M-row này — 10 phút traffic thật trên DB đã
seed sẵn (`seed_10m.sql`) không hợp với ngân sách thời gian/tài nguyên của GitHub-hosted runner,
vẫn là việc chạy tay trên máy dev/perf thật; cập nhật file này bằng tay mỗi khi chạy lại benchmark
trên máy/ngày mới. Tự động hoá thật sự tồn tại ở một tầng khác — xem "Criterion micro-benchmark"
bên dưới.

## Criterion micro-benchmark (tự động, chạy nightly)

Khác các benchmark trên (đo network+DB round-trip qua traffic thật), 2 benchmark này đo phần
**CPU thuần** — sinh SQL (`plan_list`) và so sánh schema (`compile`/`diff`) — không cần Postgres,
đủ nhanh để chạy tự động mỗi đêm (`.github/workflows/nightly-benchmark.yml`, cron 03:00 UTC,
report-only giống `codeql.yml`, không chặn build). `target/criterion/` được cache giữa các lần
chạy nên criterion tự in dòng "Performance has regressed/improved" so với đêm trước.

- `crates/metap-query/benches/plan_list_bench.rs` — `plan_list/simple_filter` (~1.25µs),
  `plan_list/jql_and_or_range_order` (~5.47µs, câu JQL có AND/OR/range/ORDER BY).
- `crates/metap-reconciler/benches/diff_bench.rs` — `reconciler/compile` (~2.43µs),
  `reconciler/diff_cold_create_everything` (~1.63µs), `reconciler/diff_converged_zero_ops`
  (~4.60µs — case chạy nhiều nhất thực tế, sau khi entity đã reconcile hội tụ).

Chạy tay: `bash testing/performance/run-nightly-benchmark.sh`.

Máy đo: 1 dev host, debug/release ghi rõ theo từng dòng, không phải production.

## `sustained_concurrent_list_against_a_real_multi_entity_abac_workflow`

2026-08-22, `helpdesk.tickets` 500K row, `crm-server` debug build, dev Postgres local (config mặc
định `shared_buffers=128MB`), 10 phút liên tục, nhiều role khác `"admin"` qua ABAC thật:

**p50=26ms, p95=31ms, p99=34ms** — ổn định suốt 10 phút, không suy giảm theo thời gian.

## `sustained_concurrent_list_across_many_tenants_at_ten_million_rows`

2026-08-22, 10 tenant × 1M row = 10M row, sau khi tune `shared_buffers=2GB`/
`effective_cache_size=4GB` (`docker-compose.yml`, xem `docs/roadmap.md`'s ghi chú cùng ngày cho
root-cause trước khi tune — p95 từng lên tới 1306ms ở config mặc định):

**p50=66ms, p95=78ms, p99=91ms, max=190ms**.

## `sustained_concurrent_create_update_transition_delete_cycle`

2026-08-23, 60 giây / 10 worker, chu trình create→update→transition→delete đầy đủ (không chỉ
list/read):

**11.350 cycle thành công, 0 lỗi — p50=52ms, p95=65ms, p99=78ms, max=155ms.**
