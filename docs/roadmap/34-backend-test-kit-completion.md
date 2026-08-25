## Phase 34: Backend test kit — đóng nốt 2 việc còn lại của Phase 20 (2026-08-25)

Chủ dự án chọn "làm tất luôn" 3 hạng mục còn treo — đây là hạng mục nhỏ nhất trong 3, làm trước.
Đóng đúng 2 việc `docs/roadmap/20-backend-test-kit.md` ghi "Chưa làm": Semgrep vào CI, và
criterion micro-benchmark/nightly automation cho Trụ Performance.

- **Semgrep vào CI (blocking)** — trước đây chỉ local theo đúng yêu cầu ban đầu. Finding false-
  positive duy nhất (`crates/dev-tools/src/main.rs`'s `std::env::args()`, CLI dispatch không phải
  security-sensitive) suppress bằng `# nosemgrep: rust.lang.security.args.args` **inline cùng
  dòng** (thử đặt comment ở dòng trên trước — không ăn, Semgrep cần trailing comment cùng dòng
  finding). Verify sống: `semgrep scan --config p/rust --config p/secrets --config .semgrep.yml`
  → 0 finding thật. `.github/workflows/ci.yml` thêm job `semgrep` (`--error`, exit 1 nếu có
  finding) — gate thật, không phải report-only.
- **Criterion micro-benchmark cho `plan_list`/`compile`+`diff`** — khác benchmark 10M-row hiện có
  (đo network+DB round-trip qua traffic thật, không tự động hoá được trong ngân sách GitHub-hosted
  runner), 2 benchmark mới đo **CPU thuần** (sinh SQL, so sánh schema) — không cần Postgres, đủ
  nhanh chạy nightly. `crates/metap-query/benches/plan_list_bench.rs` (đo cả filter thường lẫn
  câu JQL AND/OR/range/ORDER BY — dogfood engine JQL mới ở Phase 32), `crates/metap-reconciler/
  benches/diff_bench.rs` (case `diff_converged_zero_ops` quan trọng nhất thực tế — sau khi entity
  đã hội tụ, mọi tick sau đó phải rẻ). `.github/workflows/nightly-benchmark.yml` (cron 03:00 UTC,
  report-only giống CodeQL) — cache `target/criterion/` giữa các lần chạy để criterion tự in
  "Performance has regressed/improved" so đêm trước; **phát hiện + fix bug thật lúc viết
  workflow**: `actions/cache@v4` (dạng gộp restore+save) chỉ save đúng 1 lần cho mỗi key rồi im
  lặng bỏ qua mọi lần sau — dùng `actions/cache/restore@v4` + `actions/cache/save@v4` tách riêng,
  key gắn `github.run_id` (key cố định làm `save` fail thẳng ở lần chạy thứ 2 vì cache key bất
  biến), `restore-keys` prefix để luôn lấy đúng cache gần nhất.
- **`testing/performance/seed_10m.sql`** (mới) — script SQL thủ công tái lập đúng dữ liệu
  `sustained_concurrent_list_across_many_tenants_at_ten_million_rows` cần (10 tenant × 200
  `hr.departments` + 2.000 `hr.employees` + 1.000.000 `helpdesk.tickets`) — trước đây test tự ghi
  "seeded out-of-band" nhưng không có script nào tồn tại. **Verify sống bằng bản thu nhỏ** (1
  tenant, 3/4/5 dòng thay vì 200/2.000/1.000.000) chạy thật qua Postgres dev, xác nhận đúng shape
  + reference field trỏ đúng id thật, dọn sạch ngay sau đó — không chạy bản đầy đủ (quá nặng cho
  phiên làm việc này, đúng tinh thần "seed thủ công" tài liệu đã ghi).
- `testing/performance/baseline.md` cập nhật: thêm mục "Criterion micro-benchmark", ghi số đo
  thật (không phải ước lượng): `plan_list/simple_filter` ~1.25µs, `plan_list/jql_and_or_range_order`
  ~5.47µs, `reconciler/compile` ~2.43µs, `reconciler/diff_cold_create_everything` ~1.63µs,
  `reconciler/diff_converged_zero_ops` ~4.60µs.

**Kiểm chứng đầy đủ**: `cargo bench` chạy thật cả 2 file benchmark, số liệu ghi vào baseline.md là
số đo thật không phải suy đoán. `cargo build/fmt --check/clippy --workspace --all-targets -D
warnings` + `cargo test --workspace` (72 test suite) sạch. Cả 2 workflow YAML mới/sửa validate
qua `python3 -c "import yaml; yaml.safe_load(...)"`.

**Còn lại của Phase 20 sau phase này**: Trụ Regression (`testing/regression/README.md`) vẫn chỉ
là index, chưa có việc cụ thể nào được giao — không phải gap, chỉ là chưa có trigger.

Diff chưa commit.
