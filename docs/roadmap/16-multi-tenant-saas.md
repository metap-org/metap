## Phase 16: Multi-tenant SaaS Control Plane & Data Plane

**Trạng thái: Giai đoạn 1 (control-plane skeleton) đã triển khai (2026-08-16)** — crate mới
`crates/metap-control` (`Router`, `control.tenants` registry, `RegistryCache`) và `CrudService`
(`crates/metap-crud`) đã refactor để mọi method (list/get/create/update/transition/delete) đi
qua `Router::begin(tenant)` thay vì `&PgPool` trực tiếp — đúng seam đã chốt ở §2.2. **Không đổi
hành vi runtime nào** ở giai đoạn này: chưa có tenant nào được provision qua `control.tenants`
(bảng mới toanh, `crates/migrations/0012_control_tenants.sql`), nên `Router::begin` áp dụng
fallback tương thích ngược có chủ đích — tenant chưa có row → coi như
`{status: Active, strategy: Schema("public")}`, đúng hành vi trước khi có Router (mọi thứ vẫn nằm
`public` schema, isolation vẫn là cột `tenant_id` như cũ). Đã verify: 5 kịch bản e2e Router
(`cargo test -p metap-control -- --ignored`, gồm kịch bản chứng minh `SET LOCAL search_path`
không rò qua pool tái dùng — bẫy #1 nghiêm trọng nhất của thiết kế) + 4 test e2e `CrudService`
cũ pass y hệt không đổi + smoke thủ công qua HTTP (create/get/update/transition/delete) đều 200.

**Giai đoạn 2 (tenant provisioning + `DedicatedDb`) đã triển khai (2026-08-16).** `dev-tools
provision-tenant` (`pnpm provision:tenant`) là cách duy nhất ghi row `control.tenants` hôm nay
(không có HTTP `POST /admin/tenants` — `AdminContext` chỉ ủy quyền trong tenant của chính người
gọi, chưa có khái niệm "platform superadmin" xuyên tenant). Hai nhánh:
- `schema` (trial): luôn ghim `schema_name='public'` — **chưa có isolation thật**, vì bảng
  `records`/`users`/... chỉ tồn tại ở `public` cho tới khi data-plane evolution (§3,
  table-per-entity) triển khai. Route một tenant sang schema khác hôm nay sẽ vỡ hết query.
- `dedicated_db` (paid): **có isolation thật** — chạy migration lên một DB Postgres riêng, ghi
  `control.tenants.dsn_secret_ref`, tạo admin user trên DB đó. `crates/metap-control::SecretStore`
  (trait) + `EnvStore` (impl duy nhất — đọc DSN từ biến env tên đúng bằng `dsn_secret_ref`, chưa
  có Vault) + `Router`'s `dedicated_pools` cache (moka, idle TTL 10 phút) làm cho
  `Router::begin` mở transaction đúng trên DB riêng. Đã verify end-to-end qua HTTP thật: record
  tạo qua tenant `dedicated_db` chỉ nằm trong DB riêng, không xuất hiện ở DB chính.

**Đã fix 2026-08-21** (xem Phase 3): `check_action` giờ deny-by-default cho non-admin khi
entity/action chưa có policy — admin vẫn luôn bypass, và `POST /admin/policies/seed-defaults`
là công cụ nhanh để gán quyền cho role của tenant mới, tránh phải gọi `create_policy` từng cái.

**Giai đoạn 3 (HTTP tenant provisioning + platform-superadmin) đã triển khai (2026-08-17)** —
trigger đi theo hướng B đã chốt. Mô hình "platform superadmin" tái dùng 100% hạ tầng JWT/role
sẵn có, không thêm bảng/loại claim mới:
- `metap_control::PLATFORM_TENANT_ID` (`Uuid::nil()`, all-zero) — một tenant sentinel, **không
  bao giờ** có row `control.tenants`, không bao giờ được `Router` route tới. Chỉ tồn tại để
  `users`/`user_roles` (luôn ở `public`, không qua Router) có chỗ giữ danh tính platform-admin.
- Role `"platform_admin"` (một role name như bất kỳ role nào khác, gán qua
  `metap_peripherals::assign_role` sẵn có) cho user trong tenant sentinel đó.
- `PlatformAdminContext` (`crates/metap-http/src/auth.rs`, cạnh `AdminContext`) — extractor mới
  check `tenantId == PLATFORM_TENANT_ID && roles chứa "platform_admin"`, khác `AdminContext`
  (chỉ ủy quyền trong tenant của chính người gọi).
- `dev-tools bootstrap-platform-admin <email> <password>` (`pnpm bootstrap:platform-admin`) —
  bootstrap con-gà-quả-trứng đầu tiên, cùng kiểu `seed-admin` đã giải quyết cho tenant admin.
- Logic provisioning (trước đây inline trong `dev-tools`) được kéo ra
  `metap_control::provision_schema_tenant`/`provision_dedicated_db_tenant` — CLI và HTTP giờ
  gọi chung 2 hàm này, không thể lệch nhau (cùng lý do `mint_jwt`/`create_user` đã dùng chung
  trước đó). `PostgresTenantRegistry::list()` (mới) hỗ trợ `GET /platform/tenants`.
- Crate mới `crates/metap-control-http` (cùng lý do tách riêng `metap-lowcode-http` — khả năng
  optional, `metap-http` không phụ thuộc nó): `POST /platform/tenants` (body có `strategy:
  "schema"|"dedicated_db"`, 409 nếu `tenantId` trùng, 400 nếu thiếu field theo strategy),
  `GET /platform/tenants`, `GET /platform/tenants/{id}`, `PATCH /platform/tenants/{id}/status`
  (suspend/resume — thêm ngay sau đó cùng ngày 2026-08-17). **Chưa làm** (out of scope đợt
  này): delete/deprovision — cần thiết kế riêng cho việc dọn dữ liệu tenant.
- **Suspend/resume hoá ra là một việc rất nhỏ**: enforcement đã tồn tại sẵn từ Giai đoạn 1 —
  `Router::begin` đã reject `TenantStatus::Suspended` với 403 (`RouterError`, xem
  `crud_service.rs`'s `router_unavailable`) từ trước, việc còn thiếu chỉ là hành động admin để
  đổi cột `status`. `PostgresTenantRegistry::set_status(id, status)` (mới, chỉ nhận
  `"active"`/`"suspended"` qua route — các status khác do flow chưa xây quản lý) +
  `PATCH /platform/tenants/{id}/status`. Chịu ảnh hưởng của `RegistryCache`'s TTL 30s có sẵn
  (đã document từ trước là tradeoff chấp nhận được cho "provisioning, suspend/promote") — một
  suspend/resume có thể mất tới 30s mới có hiệu lực trên route đã cache, không phải bug mới.
- Test mới: 5 test e2e `metap-control` (provisioning + `list()` + trùng `tenantId` → lỗi
  downcast được thành unique-violation + `set_status` nối trực tiếp với `Router::begin` reject
  thật), 1 test e2e `metap-http` (`PlatformAdminContext` gate qua route giả lập).
  `metap-control-http` không có test tự động (đúng tiền lệ `metap-lowcode-http`) — verify live
  qua HTTP thật: bootstrap platform-admin → mint token → provision tenant `schema` → 409 khi
  trùng id → `GET /platform/tenants`/`{id}` (kể cả 404) → admin user tenant mới login được qua
  `POST /auth/login` → provision tenant `dedicated_db` → thiếu field bắt buộc → 400 → strategy
  sai → 400 → suspend → tenant đó bị 403 trên `/api/*` → status không hợp lệ → 400 → id không
  tồn tại → 404 → resume → hoạt động lại → token không phải platform-admin → 403 trên mọi route
  `/platform/*`, kể cả một admin thường của tenant khác.

**Giai đoạn 4 (Vault) — bắt đầu, `VaultStore` đã xong (2026-08-17).** `crates/metap-control::VaultStore`
— second `SecretStore` impl cạnh `EnvStore` — static KV v2 secret qua HTTP API của Vault (crate
`vaultrs`), token auth (`VAULT_TOKEN`), không phải AppRole, không phải dynamic database-credentials
engine của Vault (cả hai đều là gap thật, cố tình để lại tới khi có một production deployment
target thật sự cần — `DbCreds::expires_at` vẫn luôn `None`, giống `EnvStore`). Lựa chọn store nào
(`EnvStore` hay `VaultStore`) giờ chuyển lên composition root (`apps/crm-server/src/main.rs`,
`AppState::new` nhận `secret_store: Arc<dyn SecretStore>` thay vì tự build `EnvStore` bên trong) —
hành vi mặc định không đổi: vẫn `EnvStore` trừ khi `VAULT_ADDR`/`VAULT_TOKEN` (`metap-infra`'s
`AppConfig`) được set, nên không downstream project nào bị ép chạy Vault container để dev bình
thường. `dev-tools vault-put-dsn <dsnSecretRef> <dsn>` (`pnpm`-equivalent chưa thêm) ghi DSN vào
Vault cho một tenant `dedicated_db` — đối trọng Vault-backed của bước "set env var" mà
`provision-tenant` vẫn in ra khi dùng `EnvStore`. `docker-compose.yml` có thêm service `vault` (dev
mode, fixed root token) — opt-in, không nằm trong stack mặc định `docker compose up -d postgres
rabbitmq`. Test: 3 e2e (`crates/metap-control/tests/vault_store.rs`, `--ignored`, cần một dev Vault
sống). Đóng lại luôn phần "Design-only, chưa code" mà Phase 8's bullet secret manager từng ghi.

**Role lookup + RBAC/policy qua Router — Đã xong (2026-08-20), đóng một bug thật, không chỉ một
gap kiến trúc.** Rà soát lại roadmap phát hiện dòng "role lookup và `PostgresPolicyStore` vẫn
dùng `AppState.pool` trực tiếp" phía trên **sai lý do**: đây không phải RBAC/policy là bảng
control-plane dùng chung an toàn để bỏ qua Router — `provision_dedicated_db_tenant` chạy toàn bộ
`crates/migrations/*.sql` (gồm `users`/`user_roles`/`policies`) lên DB riêng của tenant, nên với
một tenant `dedicated_db` các bảng này **chỉ tồn tại trong DB riêng đó**, không bao giờ có trong
pool control-plane dùng chung. Verify trực tiếp (không chỉ đọc code): provision một tenant
`dedicated_db` thật qua `POST /platform/tenants` → admin user được tạo đúng trong DB riêng
(query xác nhận) → `POST /auth/login` với đúng email/password đó → **`401 invalid_credentials`**,
vì `verify_credentials` query nhầm pool chung. Kết luận: **toàn bộ tier `dedicated_db`** (Phase 16
Giai đoạn 2, 2026-08-16) **không ai login được** kể từ khi ship — không phải RBAC lỏng, mà auth
hỏng hoàn toàn; không bị phát hiện trước đó vì narrative verify của Giai đoạn 3 chỉ test login
cho tenant `schema`.

Đã fix toàn bộ, không phải patch một phần:
- `metap_peripherals::role_assignment` (`get_roles_for_user`/`assign_role`/`revoke_role`/
  `list_users`) và `metap_peripherals::auth` (`verify_credentials`/`create_user`) đổi từ
  `pool: &PgPool` sang generic `impl PgExecutor<'e>` (cùng pattern `metap-crud::crud_service`'s
  `fetch_existing` đã dùng) — vừa chạy được với một `&PgPool` trần (provisioning, trước khi
  `control.tenants` row tồn tại nên Router chưa route được), vừa chạy được với một
  `Router::begin`-transaction (mọi call site còn lại).
- `PostgresPolicyStore` **chuyển từ `metap-permission` sang sống trong `metap-control`**
  (`crates/metap-control/src/policy_store.rs`) — lý do thuần dependency-cycle, không phải
  ranh giới thiết kế mới: `metap-metadata -> metap-permission`, `metap-peripherals ->
  metap-metadata`, `metap-control -> metap-peripherals`; `metap-permission -> metap-control`
  (để với tới `Router`) sẽ khép vòng lặp đó. Trait `PolicyStore` vẫn ở `metap-permission`
  (`row_from_sql` được đổi `pub` để impl bên `metap-control` tái dùng); mọi method của trait đã
  sẵn nhận `tenant_id: Uuid` nên không cần đổi signature, chỉ đổi phần lưu trữ — mỗi method giờ
  tự `router.begin(tenant_id.into())` rồi commit.
- `AppState` (`metap-http`) có thêm field `router: Router` public; `AppState::new` nhận
  `router: Router` thay vì tự build từ `secret_store` — `Router` giờ được build một lần ở
  composition root (`apps/crm-server/src/main.rs`) và chia sẻ cho cả `PostgresPolicyStore::new`
  lẫn `AppState`/`CrudService`, thay vì hai `Router`/`RegistryCache` độc lập.
- `AuthContext` (`crate::auth`, mọi request đã auth) route role lookup qua
  `state.router.begin(tenant_id)` — `PLATFORM_TENANT_ID` (sentinel, không bao giờ có
  `control.tenants` row) tự động rơi vào fallback "unregistered tenant → public schema" sẵn có
  của `Router::begin`, đúng nơi `users`/`user_roles` của nó thật sự nằm, không cần
  special-case.
- `POST /auth/login` thêm field **tuỳ chọn** `tenantId` vào body. Có `tenantId` → route qua
  `Router::begin(tenantId)` (bắt buộc với `dedicated_db`, vì `users` không nằm ở pool chung).
  Không có `tenantId` → giữ nguyên hành vi cũ (query pool chung theo email global) — đúng mặc
  định cho tenant `schema` (hiện vẫn dùng chung `public`, chưa có isolation thật, nên email vẫn
  là khoá tra cứu duy nhất khả dụng cho nhóm này). Không phải breaking change cho flow hiện có,
  chỉ thêm khả năng mới.
- `/admin/users`, `/admin/users/{id}/roles[/{role}]` (`routes/admin.rs`) route qua Router;
  `create_user` giờ chạy insert user + mọi role assignment trong **một** transaction thay vì một
  connection mỗi lệnh gọi — tiện thể đóng luôn một gap atomicity có sẵn từ trước (một role
  assignment fail giữa chừng từng để lại user đã tạo nhưng chỉ có một phần role, không cách nào
  biết role nào fail).
- `templates/metap-app` (main.rs + tests/http_server.rs) cập nhật theo cùng shape — verify bằng
  `cargo generate` một project thật (không nằm trong workspace nên `cargo check` gốc không tự
  bắt được) rồi trỏ dependency `metap` sang path local, `cargo check --tests` sạch.

Verify: toàn bộ test suite hiện có (`cargo test --workspace` + `-- --ignored` trên Postgres/
RabbitMQ/Vault thật) pass không đổi, cộng test mới cho template. Verify live riêng cho đúng bug
gốc: provision lại tenant `dedicated_db` → `POST /auth/login` **kèm** `tenantId` → 200, JWT hợp
lệ → `GET /auth/me` trả đúng `roles: ["admin"]` (role lookup qua Router hoạt động) →
`GET /admin/users` liệt kê đúng user của tenant đó → `POST /admin/policies` tạo policy thành
công — cả bốn đều chạm đúng DB riêng của tenant, không phải pool chung.

**Vault AppRole auth — Đã xong (2026-08-20).** `metap_control::VaultStore::new_with_approle`
(cạnh `new` token-based có sẵn) — login một lần lúc construct qua
`vaultrs::auth::approle::login`, `client.set_token(...)` với client token trả về. Lý do tồn tại
song song với token-based: `VAULT_TOKEN` nghĩa là phải phân phối tay một credential dùng-được-
ngay, sống lâu dài; AppRole's `role_id` không nhạy cảm (bake thẳng vào deploy manifest được),
`secret_id` mới là phần nhạy cảm và có thể để pipeline secret-injection (Vault Agent, một bước
CI, K8s injector) cấp ngắn hạn thay vì hand-carry một token thô. **Chưa làm, có chủ đích**: auto-
renew trước khi token hết hạn — token AppRole hết hạn thì mọi call Vault sau đó fail cho tới khi
restart process hoặc gọi lại constructor; cùng mức "không tự rotate" như token tĩnh vốn có, không
phải regression mới, nhưng vẫn là gap thật cần một background task hoặc retry-on-fail để đóng.
`AppConfig` thêm `vault_role_id`/`vault_secret_id`/`vault_approle_mount`
(`VAULT_ROLE_ID`/`VAULT_SECRET_ID`/`VAULT_APPROLE_MOUNT`, mount mặc định `"approle"`);
`apps/crm-server/src/main.rs` ưu tiên AppRole nếu cả `vault_role_id`+`vault_secret_id` đều có,
rồi mới tới token, rồi mới `EnvStore`. Test mới: `approle_login_can_read_a_dsn_written_by_a_token_authed_store`
(`crates/metap-control/tests/vault_store.rs`, doc comment của file có sẵn các bước `vault` CLI để
tự setup AppRole role trên dev Vault). Verify live, không chỉ test: enable `approle` + tạo role
qua `vault` CLI trên dev Vault container → boot `crm-server` thật chỉ với
`VAULT_ROLE_ID`/`VAULT_SECRET_ID` (không có `VAULT_TOKEN` trong env của chính nó) → provision một
tenant `dedicated_db` mới, DSN ghi vào Vault qua `dev-tools vault-put-dsn` (dùng root token,
việc của operator, tách biệt với credential read-only mà server tự dùng) → login vào tenant đó
→ 200, JWT hợp lệ — xác nhận `Router` resolve đúng DSN qua Vault bằng AppRole token, không phải
token tĩnh.

**Delete/deprovision tenant — Đã xong (2026-08-21).** Hai quyết định thiết kế chốt trước khi code
(không có sẵn trong `docs/architectures/09-adr.md`, thao tác phá huỷ nên hỏi trực tiếp thay vì tự
suy đoán): (1) `dedicated_db` — **không** tự động `DROP DATABASE` vật lý; (2) `schema` (hiện vẫn
chung `public`, chưa có isolation thật) — **không** tự động xoá record theo `tenant_id` trong
`records`/`users`/... dùng chung. Cả hai lý do giống nhau: một API call lỡ tay không nên xoá dữ
liệu vĩnh viễn, và với `schema` việc xoá theo `tenant_id` trên bảng dùng chung còn rủi ro hơn
(một bug trong `WHERE` ảnh hưởng tenant khác) khi chưa có data-plane isolation thật (§3).

Implement: `TenantStatus::Deleted` (mới, `metap-control::tenant`) — terminal, không có đường quay
lại như `Suspended`→`resume`. `Router::begin` reject với `RouterError::TenantDeleted`, map 404
(`metap-crud`'s `router_unavailable`, `metap-http`'s `router_unavailable_response`) — 404 chứ
không phải 403 như Suspended/Expired, vì tenant coi như không còn tồn tại nữa, không phải "tồn
tại nhưng tạm bị cấm". `PostgresTenantRegistry::deprovision(id)` (chỉ update cột `status`,
idempotent — gọi lại trên tenant đã xoá vẫn `true`, không lỗi). Route mới
`DELETE /platform/tenants/{id}` (`metap-control-http`, gate `PlatformAdminContext`, 204 thành
công / 404 nếu `id` không tồn tại) — tách riêng khỏi `PATCH .../status` (chỉ nhận
`active`/`suspended`) vì xoá là một chiều, không phải một giá trị status như hai cái kia.

Test mới: `deprovisioning_is_immediately_enforced_by_router_with_a_404_not_a_403`
(`crates/metap-control/tests/provisioning_postgres.rs`, e2e Postgres thật — cùng pattern test
suspend có sẵn). Verify live qua HTTP thật: provision tenant `schema` → `GET` thấy `status:
"active"` → `DELETE` → 204 → `GET` lại vẫn 200 nhưng `status: "deleted"` (row vẫn còn, chỉ đổi
status) → `DELETE` lần 2 vẫn 204 (idempotent) → `DELETE` một id không tồn tại → 404 → mint token
cho tenant đã xoá, gọi `/api/crm.customers` → bị chặn thật (không phải giả lập) — dù response
thực tế caller thấy là **401** "failed to resolve roles" từ `AuthContext`, không phải 404 từ
`CrudService`: role lookup cũng route qua `Router::begin` (fix RBAC 2026-08-20) và chạy trước
handler, nên với `Suspended`/`Expired`/`Deleted` một client luôn gặp 401 ở tầng auth trước khi
tới được tầng CrudService nơi 404/403 mới thực sự map — hành vi đã có sẵn cho
`Suspended`/`Expired` từ trước, không phải điểm không nhất quán mới do `Deleted` gây ra. `404
tenant_not_found` từ `CrudService`'s mapping vẫn đúng và có test riêng (Router-level), chỉ là
không phải status code một client thật nhìn thấy qua route đã-auth thông thường.

**AppRole auto-renewal — Đã xong (2026-08-21).** Đóng gap đã ghi nhận từ lúc ship AppRole
(2026-08-20): trước đây token AppRole hết hạn là mọi call Vault sau đó fail cho tới khi restart
process. `VaultStore` giờ giữ `client`/`expires_at` sau một `tokio::sync::Mutex` chung, và mọi
method của `SecretStore` (`db_credentials`, cộng `put_dsn`) tự kiểm tra + re-login (cùng
`role_id`/`secret_id`, một `vaultrs::auth::approle::login` mới) trước khi thực sự gọi Vault, nếu
còn dưới `RENEW_BUFFER` (60s) là hết hạn — không cần background task/renewal loop riêng, vì
`db_credentials` vốn đã không phải hot path (`Router` cache pool của tenant `dedicated_db` 10
phút). `lease_duration == 0` (quy ước "không hết hạn" của Vault) được xử lý riêng, không bị hiểu
nhầm thành "đã hết hạn ngay" (sẽ ép re-login ở mọi call nếu tính sai). Instance dùng token tĩnh
(`VaultStore::new`) không bị ảnh hưởng — vẫn không có khái niệm renew, đúng như trước.

Verify live, không chỉ đọc code: tạo một AppRole role riêng cho test với `token_ttl=5s` (ngắn hơn
nhiều `RENEW_BUFFER`) → login → **đợi thật 6 giây** (token gốc chắc chắn đã hết hạn phía Vault) →
gọi `db_credentials` → vẫn thành công, chỉ có thể vì code đã tự renew trước đó. Test mới
`approle_token_auto_renews_before_a_real_expiry` (`crates/metap-control/tests/vault_store.rs`,
~6s runtime, doc comment của file có sẵn lệnh `vault` CLI để tự tạo role ngắn hạn này). Toàn
workspace `build`/`clippy -D warnings`/`fmt --check`/`test` sạch, không regression trên 2 test
AppRole cũ.

**`/code-review` trên commit trên tìm ra 2 finding thật, cả hai đã fix cùng ngày (2026-08-21):**

1. **Renewal tiêu mất `secret_id` một-lần-dùng** — bản đầu tiên luôn re-login bằng
   `vaultrs::auth::approle::login` (cùng `secret_id` đã lưu) mỗi lần renew, tự phá chính nó ngay
   lần renew đầu tiên nếu role được cấu hình `secret_id_num_uses=1` — đúng khuyến nghị chính
   module doc comment đưa ra ("issued short-lived/one-time"). Fix: `ensure_fresh_token` giờ thử
   `vaultrs::token::renew_self` trước (gia hạn token hiện có, không cần `secret_id`), chỉ fallback
   về login lại khi `renew_self` fail (token vượt `max_ttl`). Verify live: tạo role với
   `secret_id_num_uses=1`, `token_ttl=65s` (dài hơn `RENEW_BUFFER` để `renew_self` chạy trong lúc
   token còn sống thật, không phải sau khi đã chết hẳn) → login (tiêu hết `secret_id`) → renew
   2 lần liên tiếp (mỗi lần cách nhau 6s) → cả 2 đều thành công, không đụng lại `secret_id` đã
   dùng. Test mới `renewal_survives_past_expiry_twice_without_reusing_a_single_use_secret_id`.
2. **Giữ lock qua trọn network call, serialize hết mọi tenant** — `Mutex` bọc `client` bị giữ
   suốt cả round-trip HTTP tới Vault, trong khi `VaultStore` là một `Arc<dyn SecretStore>` dùng
   chung cho `Router` của mọi tenant `dedicated_db` — N tenant cold-start/idle-evict cùng lúc sẽ
   xếp hàng qua đúng 1 lock thay vì chạy song song. Fix: `fresh_client()` chỉ giữ lock cho phần
   kiểm tra/renew (rẻ, hiếm khi cần I/O), rồi build một `VaultClient` mới (cùng `addr`/token hiện
   tại, qua `build_client` có sẵn) để gọi Vault **ngoài** lock — đánh đổi chấp nhận được (mất
   connection-pool reuse giữa các lần gọi, đổi lấy không serialize hoá cross-tenant) vì Vault
   không phải hot path ở đây.

Toàn workspace `build`/`clippy -D warnings`/`fmt --check`/`test` sạch lại sau cả 2 fix, 6/6 test
`vault_store.rs` pass trên dev Vault thật.

Còn lại cho Giai đoạn 4+: dynamic database-credentials engine thật của Vault (rotating creds,
không phải static DSN); template pack; data-plane evolution (§3-§7); capabilities (§8); FE
onboarding (§9); deployment SaaS specifics (§11).

Toàn bộ thiết kế nằm ở `docs/multi-tenant-platform-design.md` (hợp nhất từ hai bản nháp brainstorm
`adr.md`/`adr2.md` ngày 2026-08-15, đã xóa sau khi hợp nhất); các quyết định cốt lõi rút gọn dạng
bullet nằm ở [09. Architecture Decisions](architectures/09-adr.md). Tóm tắt phạm vi:

- **Tenant isolation**: tiered tenancy — schema-per-tenant cho trial (1 DB chung, N schema),
  DB-per-tenant cho paid (isolation vật lý). Thay cho đề xuất RLS-only ban đầu. §2.1.
- **Control plane**: `control.tenants` registry + `Router.begin(tenant)` (mọi query qua
  transaction, `SET LOCAL search_path` — không session-level, tránh rò tenant qua pool tái dùng),
  Vault cho secret + config 4 tầng kế thừa, tenant provisioning tự phục vụ + template pack YAML.
  §2.2-§2.5.
- **Data plane**: table-per-entity thay `records` JSONB dùng chung (khi @ ~10M row/entity), 3
  tier storage suy từ cờ metadata (`indexed/unique/searchable`), reconciler DDL level-triggered
  (idempotent, tự lành sau crash — DDL online không rollback được), migration declarative-only
  (eager cho field indexed, lazy cho field display-only), quarantine cho data bẩn, orchestrator
  fan-out multi-tenant (pull-based, canary→wave rollout). §3-§7.
- **Capabilities phái sinh**: audit/history (diff mode, opt-in per-entity), aggregation/rollup
  (permission pushdown vào WHERE), inbound integration (idempotency gate + raw store trước khi
  xử). §8.
- **FE onboarding**: `<MetapApp>` shell đọc `AppManifest` từ pack, tự dựng nav/routes — thay cho
  việc ráp tay từng dự án như `apps/crm-fe/App.tsx` hiện nay. §9.
- **Deployment SaaS**: PgBouncer transaction-mode bắt buộc khi scale ngang (connection budget
  nhân theo số instance), reconcile-orchestrator + cron tách khỏi request-serving instance
  (singleton/worker riêng), HA cho control-plane + Vault (SPOF). §11.

**Đừng over-build trước khi có trigger** (nguyên văn từ thiết kế gốc): three-way merge pack,
Kafka, pack registry/CDN, scripting/plugin runtime per-tenant, zero-downtime cutover phức tạp hơn
expand-contract 2 tầng đã mô tả.

Findings nhỏ từ cùng đợt review đã tách sang mục tiêu Phase 8 (TS strict, `opt-level`, clippy/
rustfmt gate, JWT `aud`/`iss`, `.gitignore` cho `settings.local.json`) vì không phụ thuộc trigger
SaaS — làm được ngay, không cần đợi Phase 16 bắt đầu.

