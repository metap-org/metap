## Phase 25: Tenant auth pluggable — Bearer + Basic + OIDC (2026-08-24, đang làm)

Auth trước đây chỉ có 1 đường cứng: local email+password → mint JWT. Chủ dự án chốt: đây là auth
của **tenant** (khách hàng dùng platform), không phải auth low-code admin — tenant phải tự chọn
được cơ chế auth cho user của mình, **cho phép nhiều kiểu cùng lúc**. Scope chốt: Bearer (JWT, giữ
nguyên), Basic (HTTP Basic, per-request), OIDC (redirect IdP ngoài, JIT provisioning khi user OIDC
login lần đầu) — extensible thêm sau. Plan đầy đủ: xem plan file phiên làm việc lúc chốt
(`/home/minhtuan/.claude/plans/snazzy-squishing-phoenix.md` tại thời điểm viết).

**Bước 1/3 — Foundation, done (2026-08-24):**
- `crates/migrations/0019_tenant_auth_configs.sql` — bảng `tenant_auth_configs` (1 row/tenant/
  provider_kind, **N provider enabled cùng lúc**, không phải 1 strategy loại trừ nhau — đúng
  "tenant có thể chọn nhiều kiểu auth"), backfill `local` cho mọi tenant đã có trong
  `control.tenants` trước migration này.
- Crate mới `crates/metap-auth` (plain library, cùng hình dạng `metap-cache`/`metap-storage`):
  `AuthProviderKind` enum (`Local`/`Basic`/`Oidc`), `LocalPasswordProvider` (bọc
  `metap_peripherals::verify_credentials`, generic `PgExecutor` — không phải `dyn AuthProvider`
  boxed, vì Local/Basic/OIDC nhận input khác hẳn nhau, ép chung 1 trait signature chỉ là
  indirection không cần thiết — chi tiết ghi trong doc comment của crate).
  `crates/metap-control::provisioning`'s `provision_schema_tenant`/`provision_dedicated_db_tenant`
  thêm bước `seed_local_auth_config` — tenant mới luôn có `local` enabled mặc định.
- `crates/metap-http/src/routes/auth.rs`'s `login()` refactor gọi qua
  `metap_auth::LocalPasswordProvider` thay vì gọi thẳng `metap_peripherals::verify_credentials` —
  thuần refactor, hành vi giữ nguyên 100%.
- **Kiểm chứng sống**: apply migration thật, xác nhận 2 tenant đã provision đều backfill đúng
  `local` row. Chạy `crm-server` thật, tạo user thật qua `pnpm create:user`, login qua HTTP với cả
  2 nhánh (`tenantId` có/không) đều trả JWT đúng, sai password → 401 — không regression.
  `cargo build/fmt --check/clippy --workspace --all-targets -D warnings` sạch toàn workspace.

**Bước 2/3 — HTTP Basic auth, done (2026-08-24):**
- `crates/metap-http/src/auth.rs`'s `AuthContext` extractor thêm nhánh `Authorization: Basic
  <base64>` (thử trước Bearer, cheap prefix check) — bắt buộc header `X-Tenant-Id` (Basic không
  có claim nào mang tenant_id như JWT), decode base64 `email:password`, check tenant có bật
  `basic` trong `tenant_auth_configs` không (mặc định **tắt** cho mọi tenant), verify qua
  `LocalPasswordProvider`, build `RequestContext` thẳng — **không mint JWT**, stateless đúng bản
  chất HTTP Basic.
- `TenantAuthCache` mới (`crates/metap-http/src/cache.rs`, cùng hình dạng
  `ContextAttributesCache` đã có) — cache "tenant này bật provider nào" 30s TTL, vì check này
  chạy trên **mọi request** Basic-authed (không có session/JWT để amortize như Bearer).
- `metap-auth::enabled_providers` mới — query `tenant_auth_configs`.
- **Kiểm chứng sống** (crm-server thật, tenant `33333333-...`): (1) Basic chưa bật → 401 đúng
  message; (2) thiếu `X-Tenant-Id` → 401; (3) bật `basic` qua SQL, đợi qua cache TTL (30s) → 200
  đúng identity; (4) sai password → 401 "Invalid email or password."; (5) path Bearer cũ không bị
  ảnh hưởng — `/auth/login` + `/auth/me` qua JWT vẫn 200 y hệt trước. `cargo build/fmt --check/
  clippy --workspace --all-targets -D warnings` sạch toàn workspace.

**Bước 3/3 — OIDC, done (2026-08-24):**
- `crates/migrations/0020_users_oidc_columns.sql` — `password_hash` chuyển nullable, thêm
  `auth_provider`/`external_subject` (+ CHECK constraint, + unique index tra theo
  `(tenant_id, auth_provider, external_subject)`). **Chú ý thao tác**: `sqlx::migrate!` không tự
  phát hiện file migration mới thêm vào nếu binary `db-migrate` đã build trước đó — cần `touch`
  lại `src/main.rs` (hoặc `cargo clean`) để ép rebuild, nếu không sẽ âm thầm bỏ qua migration mới.
- `crates/metap-auth/src/oidc.rs` (mới) — `OidcConfig` (issuer/client_id/client_secret_ref trỏ
  env var, không lưu secret thật/redirect_uri/scopes/post_login_redirect), `oidc_authorize_url`/
  `oidc_verify_callback` dùng crate `openidconnect` v4 (dựng trên `oauth2` + `reqwest` rustls,
  xác nhận qua `cargo tree` không kéo openssl). PKCE + nonce đầy đủ theo chuẩn, không tự làm crypto
  tay. `jit_provision_oidc_user`/`find_oidc_user` — JIT provisioning tra theo `external_subject`
  (ổn định hơn email), không gán role nào (deny-by-default, admin gán sau).
- Routes mới (`crates/metap-http/src/routes/auth.rs`): `GET /auth/oidc/{tenant_id}/login`
  (redirect IdP), `GET /auth/oidc/{tenant_id}/callback` (exchange code → JIT/link user → mint JWT
  y hệt local login → redirect FE kèm `#token=`), `GET /auth/providers?tenantId=` (public, liệt kê
  provider đã bật). `OidcFlowCache` mới (`crates/metap-http/src/cache.rs`) — bookkeeping CSRF/
  nonce/PKCE verifier giữa 2 request redirect+callback, one-time-use (chống replay).
- FE (`packages/platform-react`): `OidcCallbackPage` mới (đọc `#token=` từ URL, gọi `setToken`).
  `LoginForm` thêm prop `tenantId` tuỳ chọn (không đổi hành vi cho mọi caller hiện có không truyền
  prop này) — khi có, gọi `/auth/providers` để hiện nút "Sign in with SSO" nếu tenant bật oidc, và
  truyền `tenantId` vào body `/auth/login` (đóng luôn gap FE chưa từng gửi field này dù backend đã
  hỗ trợ từ Phase 16). Route `/auth/oidc/callback` thêm vào cả `apps/crm-fe`/`apps/jira-fe`.
- **Kiểm chứng sống — nhiều lớp**: (1) unit e2e `crates/metap-auth/tests/oidc_e2e.rs` — mock IdP
  thật qua `wiremock` (discovery/JWKS/token), id_token tự ký RS256 bằng `jsonwebtoken`+keypair
  `rsa` tự sinh (không phải mock đoán mò) — xác nhận round-trip đúng identity + nonce sai bị
  reject; JIT provisioning + link-lần-2-không-trùng verify qua Postgres thật (`--ignored`). (2)
  **Full HTTP round-trip qua `crm-server` thật** với 1 fake IdP Python độc lập (không phải test
  trong repo, verify tay 1 lần): `GET /auth/oidc/{tenant}/login` → parse `nonce`/`state` thật từ
  header `Location` → `GET /auth/oidc/{tenant}/callback` → 303 kèm `#token=` đúng JWT hợp lệ, dùng
  được trên `/auth/me` y hệt token local login. Xác nhận thêm: `users` row JIT-provisioned đúng
  (`auth_provider='oidc'`), replay `state` đã dùng → 401 đúng, tenant chưa cấu hình oidc → 404
  đúng. `cargo build/fmt --check/clippy --workspace --all-targets -D warnings` + `cargo test
  --workspace` sạch toàn bộ. `pnpm typecheck`/`lint` sạch cả 3 package frontend; `pnpm --filter
  @metap/crm-fe build` + `@metap/jira-fe build` (tsc -b + vite build) sạch — theo đúng chính sách
  không tự Playwright-verify FE, hand off cho chủ dự án bấm thử thật trên trình duyệt.

**Tổng Phase 25**: cả 3/3 bước done, verify sống đầy đủ. Diff chưa commit.

