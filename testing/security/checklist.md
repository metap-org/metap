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

### Bổ sung 2026-09-07 — rà soát lại sau ~2 tuần không động tới, tìm ra 17 file test mới chưa ghi

Bảng gốc phía trên viết 2026-08-23/25, trước khi gRPC/GraphQL/JWKS/config-tiers/cookie-session/S3
ship — rà soát lại tìm thấy các test bảo mật thật đã tồn tại nhưng chưa từng vào bảng này (không
phải test mới viết, trừ hàng JWKS cuối cùng):

| Scenario | Test | Ghi chú |
|---|---|---|
| 2 tenant ghi cùng key logic vào object storage không đụng nhau | `crates/metap-storage/tests/s3_seaweedfs.rs`'s `same_key_different_tenants_never_collide` | Tên trùng (cùng invariant) với `metap-cache::redis_cache`'s test cùng tên — 2 test độc lập, khác backend |
| Key dạng path-traversal (`../`) bị chặn trước khi chạm backend | `..s3_seaweedfs.rs`'s `traversal_shaped_key_is_rejected_before_it_reaches_the_backend` | `S3ObjectStore::validate_key`, cùng kỷ luật `Router::validate_schema_name` |
| Webhook SSRF guard (audit 04 A#1) — chặn cloud-metadata/private-range/scheme lạ/header cấm | `crates/metap-cron-scheduler/src/executor/ssrf_guard.rs`'s `#[cfg(test)] mod tests`, 12 test (`blocks_cloud_metadata_and_private_ranges`, `ipv4_mapped_ipv6_does_not_bypass_the_v4_rules`, `check_rejects_a_literal_metadata_address_end_to_end`, `forbidden_headers_are_rejected_regardless_of_case`, `a_literal_authorization_header_stays_refused_after_the_secret_path_was_added`, ...) | Unit test thuần (không cần Postgres), nằm trong `src/`, không phải `tests/*.rs` — dễ bị bỏ sót khi rà theo convention cũ, xem `testing/regression/README.md`'s mục riêng cho việc này |
| CSRF double-submit: request mutating thiếu header bị từ chối | `crates/metap-http/tests/cookie_session_postgres.rs`'s `a_mutating_request_without_the_csrf_header_is_rejected` | |
| CSRF double-submit: header không khớp cookie bị từ chối | `..cookie_session_postgres.rs`'s `a_mutating_request_with_a_mismatched_csrf_header_is_rejected` | |
| Bearer token thắng cookie, không bao giờ bị CSRF-gate | `..cookie_session_postgres.rs`'s `an_authorization_header_wins_over_a_cookie_and_is_never_csrf_gated` | Bearer là cơ chế cũ, không đổi hành vi bởi migration cookie |
| Token hết hạn trong cookie bị từ chối | `..cookie_session_postgres.rs`'s `an_expired_token_in_a_cookie_is_rejected` | |
| Credential `secret`-tier không bao giờ trả về qua bất kỳ read nào | `crates/metap-http/tests/tenant_secret_postgres.rs`'s `a_stored_credential_is_never_returned_by_any_read` | Đúng invariant "write-only" `docs/features/18-config-tiers-db-backed.md` slice 3 yêu cầu |
| Reference secret do caller tự đặt bị bỏ qua hoàn toàn (server luôn tự suy ra) | `..tenant_secret_postgres.rs`'s `a_caller_supplied_secret_reference_is_ignored_entirely` | |
| Credential chứa newline bị từ chối, không lưu | `..tenant_secret_postgres.rs`'s `a_credential_containing_a_newline_is_refused_and_not_stored` | Chặn header/value injection |
| Platform admin không thể set credential cho 1 tenant cụ thể qua bề mặt fleet-wide | `..tenant_secret_postgres.rs`'s `a_platform_admin_cannot_set_a_tenant_credential_fleet_wide` | Ranh giới `PlatformAdminContext` vs `AdminContext` |
| `SecretString` không bao giờ in nội dung qua `Debug`/`Display` | `..tenant_secret_postgres.rs`'s `a_secret_string_never_prints_its_contents` | |
| Key tier `Operator` không thể ghi qua bất kỳ API nào, kể cả platform admin | `crates/metap-http/tests/platform_config_postgres.rs`'s `an_operator_key_is_refused_even_for_a_platform_admin` | Đúng invariant tiering-là-security-boundary `metap-config` — xem `CLAUDE.md`'s mục đó |
| Tenant admin không chạm được bề mặt `/platform/config` | `..platform_config_postgres.rs`'s `a_tenant_admin_cannot_reach_the_platform_surface_at_all` | |
| Tenant admin không ghi được key tier Operator/PlatformGlobal qua `/admin/config` | `crates/metap-http/tests/tenant_config_postgres.rs`'s `a_tenant_admin_cannot_write_an_operator_or_fleet_key` | |
| Override config của tenant này không lộ sang tenant khác | `..tenant_config_postgres.rs`'s `one_tenants_overrides_never_leak_into_another` | |
| `GET /public/config` chỉ trả branding, không trả gì khác | `..tenant_config_postgres.rs`'s `the_public_surface_serves_branding_and_nothing_else` | Route công khai, không auth — chặn rò thông tin |
| Giá trị theme dạng injection (`javascript:`/`data:`/...) bị từ chối trước khi lưu | `..tenant_config_postgres.rs`'s `injection_shaped_theme_values_are_refused_before_storage` | Chặn XSS qua giá trị render vào trang public |
| OIDC nonce sai bị từ chối (chặn replay) | `crates/metap-auth/tests/oidc_e2e.rs`'s `wrong_nonce_is_rejected` | |
| `WaitEvent` chain không resume nhầm theo event của tenant khác | `crates/metap-cron/tests/wait_event_postgres.rs`'s `a_waiting_chain_never_resumes_for_a_matching_event_in_another_tenant` | |
| GraphQL query quá sâu bị chặn bởi depth limit | `crates/metap-graphql/tests/graphql_schema_postgres.rs`'s `overly_deep_query_is_rejected_by_the_depth_limit` | Audit 04 A#7 |
| Token ký bởi key JWKS đã bị retire (không còn publish) bị từ chối | `crates/metap-jwks/src/lib.rs`'s `jwks_client_rejects_a_token_signed_by_a_key_no_longer_published_in_the_jwks` | **Test mới, viết 2026-09-07** — trước đó JWKS hoàn toàn không xuất hiện trong file này dù đã có rotation/`JwksClient`. Không cover nhánh "verifier đã cache key trước khi bị retire, chỉ hết hạn theo TTL của chính cache đó" — đây là grace-window có chủ đích của thiết kế 3-bước, không phải gap, xem test's doc comment |

### Semgrep false positive đã xác nhận (không cần sửa code)

| File | Rule | Vì sao là false positive |
|---|---|---|
| `crates/metap-dev-tools/src/main.rs` (dòng `std::env::args().collect()`) | `rust.lang.security.args.args` | Rule cảnh báo dùng `std::env::args()[0]` (đường dẫn executable) cho mục đích bảo mật — file này chỉ dùng `args.get(1)` để chọn subcommand CLI (`gen-keys`/`mint-token`/...), không đọc `args[0]`, không có logic bảo mật nào phụ thuộc executable path. Suppress bằng `# nosemgrep: rust.lang.security.args.args` inline (2026-08-25) để CI job gate được ở 0 finding thật, không phải bị "biết trước 1 finding luôn đỏ" |

## Chưa cover / cân nhắc thêm (ghi nhận, chưa phải việc cần làm ngay)

- **Fuzz testing** — không nằm trong phạm vi bộ test này.
- **Injection qua tên entity low-code** — đã có `MetadataCompiler` validate theo
  `docs/architectures/09-adr/00-index.md`, không phải gap cần lấp thêm.
- **Rate-limiter bypass** — `security_headers.rs`/rate-limit layer đã có (Phase 8 Hardening), chưa
  có test riêng khai thác bypass (vd multiple IP giả mạo header).
- **Resolve-đúng-transform-chain khi quarantine** (`crates/metap-reconciler/src/quarantine.rs`) —
  đã ghi nhận là giới hạn có chủ đích trong chính doc comment của `resolve()`, không phải gap ẩn.
- **gRPC không có test tenant-isolation/RBAC-deny riêng** (2026-09-07) — `grpc_backend_client_postgres.rs`/
  `grpc_crud_postgres.rs` chạy CRUD thật qua 1 JWT thật (đúng đường auth), nhưng không có test nào
  hình dạng giống `jwt_security_postgres.rs`/`rbac_abac_integration_postgres.rs` cho riêng đường
  gRPC — permission/validation đi qua cùng `CrudService` với REST nên rủi ro thấp, nhưng chưa có
  test khẳng định trực tiếp.

## Công cụ bổ sung (không phải regression test, không CI)

- **OWASP ZAP (DAST)** — `testing/security/zap/run.sh`, xem `testing/README.md`'s mục "DAST —
  OWASP ZAP". Cover rộng kiểu OWASP Top 10 (injection/header/v.v) bằng cách import
  `/metadata/openapi.json` — không hiểu multi-tenant ABAC/workflow của app này, không thay thế
  các hàng ở trên.

## Không thay thế review thủ công

Skill `/security-review` (chạy trên diff/PR) vẫn là bổ sung định kỳ cho các thay đổi lớn — bộ
test ở đây là regression gate tự động, không phải review toàn diện.
