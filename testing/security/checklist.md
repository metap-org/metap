# Security test checklist

Sống — cập nhật mỗi khi thêm/đổi test bảo mật. Mỗi hàng trỏ đúng file/hàm test tương ứng, không
diễn giải lại logic (đọc code là nguồn thật).

## Đã cover

| Scenario | Test | Ghi chú |
|---|---|---|
| Dependency có CVE đã biết | `.github/workflows/ci.yml`'s job `security` (`cargo audit`) | `.cargo/audit.toml` ignore-list 1 advisory không thể sửa (`rsa` qua `sqlx-mysql`, không compile vào build thật — xem file đó) |
| `SET LOCAL search_path` không rò giữa 2 tenant dùng chung 1 connection | `crates/metap-control/tests/tenant_isolation_postgres.rs`'s `single_connection_pool_never_leaks_search_path_between_two_registered_tenants` | `max_connections(1)`, đúng invariant §7 #4 thiết kế |
| `CrudService::list()` không rò dòng dữ liệu giữa 2 tenant dùng chung pool nhỏ | `crates/metap-crud/tests/crud_service_postgres.rs`'s `concurrent_cross_tenant_list_calls_never_return_another_tenants_records` | Đúng hình dạng bug thật đã fix ở commit `cc5f1ea` (thiếu filter `tenant_id`) |
| JWT thiếu token | `crates/metap-http/tests/jwt_security_postgres.rs`'s `missing_token_is_rejected` | |
| JWT hết hạn | `..jwt_security_postgres.rs`'s `expired_token_is_rejected` | `crates/metap-http/src/auth.rs`'s `validation.leeway = 20` (siết từ mặc định 60s của crate `jsonwebtoken`, quyết định chủ dự án 2026-08-24 — xem `docs/roadmap.md`) |
| JWT chữ ký bị sửa | `..jwt_security_postgres.rs`'s `tampered_signature_is_rejected` | |
| JWT ký bằng key khác (không phải key server tin) | `..jwt_security_postgres.rs`'s `token_signed_by_a_different_key_is_rejected` | |
| JWT hợp lệ nhưng tenant khác không đọc được dữ liệu | `..jwt_security_postgres.rs`'s `a_valid_token_for_one_tenant_cannot_read_another_tenants_record` | |
| Deny-by-default khi không có policy nào khớp (role không phải admin) | `crates/metap-permission/tests/rbac_abac_integration_postgres.rs`'s `non_admin_role_with_no_matching_policy_is_denied_by_default` | Trước đây chỉ có unit test cô lập, không có integration test qua Postgres thật |
| RBAC role-gate cấp/từ chối đúng role | `..rbac_abac_integration_postgres.rs`'s `role_gate_policy_grants_the_named_role_and_denies_others` | |
| ABAC record-condition (`fromContext`) cấp/từ chối đúng theo attribute | `..rbac_abac_integration_postgres.rs`'s `record_condition_allows_matching_department_and_denies_mismatched` | Non-admin role — `is_admin()` mới bypass, test này không dùng admin |
| Deny ghi đè Allow qua round-trip Postgres thật | `..rbac_abac_integration_postgres.rs`'s `explicit_deny_policy_overrides_a_matching_allow_policy` | Logic thuần đã unit-test ở `policy_condition.rs`; đây là bản round-trip DB thật |
| SAST cho logic code tự viết (không phải CVE dependency) | `.github/workflows/codeql.yml` (`analyze` job) | GitHub-native, chạy trên push/PR/cron hằng tuần. Report-only qua tab Security, không phải gate chặn CI — quy ước CodeQL: ruleset mới trên codebase cũ cần một vòng triage trước khi đủ tin để chặn build |
| SAST local + CI (blocking) | `.semgrep.yml` + `semgrep scan --config p/rust --config p/secrets --config .semgrep.yml`, wired vào CI 2026-08-25 (`.github/workflows/ci.yml`'s `semgrep` job, `--error`) | Ban đầu chỉ local (yêu cầu người dùng: "semgrep quét local"); wired vào CI sau khi false positive duy nhất được nosemgrep inline (xem hàng dưới) — gate thật ở 0 finding, không phải report-only |

### Semgrep false positive đã xác nhận (không cần sửa code)

| File | Rule | Vì sao là false positive |
|---|---|---|
| `crates/dev-tools/src/main.rs` (dòng `std::env::args().collect()`) | `rust.lang.security.args.args` | Rule cảnh báo dùng `std::env::args()[0]` (đường dẫn executable) cho mục đích bảo mật — file này chỉ dùng `args.get(1)` để chọn subcommand CLI (`gen-keys`/`mint-token`/...), không đọc `args[0]`, không có logic bảo mật nào phụ thuộc executable path. Suppress bằng `# nosemgrep: rust.lang.security.args.args` inline (2026-08-25) để CI job gate được ở 0 finding thật, không phải bị "biết trước 1 finding luôn đỏ" |

## Chưa cover / cân nhắc thêm (ghi nhận, chưa phải việc cần làm ngay)

- **Fuzz testing** — không nằm trong phạm vi bộ test này.
- **Injection qua tên entity low-code** — đã có `MetadataCompiler` validate theo
  `docs/architectures/09-adr/00-index.md`, không phải gap cần lấp thêm.
- **Rate-limiter bypass** — `security_headers.rs`/rate-limit layer đã có (Phase 8 Hardening), chưa
  có test riêng khai thác bypass (vd multiple IP giả mạo header).
- **Resolve-đúng-transform-chain khi quarantine** (`crates/metap-reconciler/src/quarantine.rs`) —
  đã ghi nhận là giới hạn có chủ đích trong chính doc comment của `resolve()`, không phải gap ẩn.

## Công cụ bổ sung (không phải regression test, không CI)

- **OWASP ZAP (DAST)** — `testing/security/zap/run.sh`, xem `testing/README.md`'s mục "DAST —
  OWASP ZAP". Cover rộng kiểu OWASP Top 10 (injection/header/v.v) bằng cách import
  `/metadata/openapi.json` — không hiểu multi-tenant ABAC/workflow của app này, không thay thế
  các hàng ở trên.

## Không thay thế review thủ công

Skill `/security-review` (chạy trên diff/PR) vẫn là bổ sung định kỳ cho các thay đổi lớn — bộ
test ở đây là regression gate tự động, không phải review toàn diện.
