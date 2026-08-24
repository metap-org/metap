## Phase 12: Rust Core Migration

**Trạng thái: Đã quyết định, Migration Order đã hoàn tất, chưa deploy.** `packages/core`
chuyển hoàn toàn sang Rust cho mọi deployment profile — xem
[09. Architecture Decisions](architectures/09-adr.md). Không phải một
sub-item của phase nào trước đó: nó tái định hình *ngôn ngữ implementation* của execution
engine mà mọi phase khác ở trên được xây dựng dựa vào, mà không thay đổi những gì các phase
đó thực sự deliver (metadata compiler, permission engine, query planner, workflow engine,
CRUD, HTTP layer, peripherals — tất cả được re-implement 1:1, không redesign).

Mục tiêu:

- ~~Quyết định có chuyển `packages/core` sang Rust hay không~~ — **Đã xong (2026-08-07)**,
  Option B (mọi profile), sau khi một spike đo được lợi ích footprint/throughput thật.
- ~~Port execution engine (Migration Order bước 1-9)~~ — **Đã xong (2026-08-07)**:
  `crates/` là một Cargo workspace 9 crate (`metap-infra`, `metap-metadata`,
  `metap-permission`, `metap-query`, `metap-workflow`, `metap-crud`, `metap-http`,
  `metap-peripherals`, cộng binary `outbox-publisher`) — 51 unit test (không cần DB) + 19
  e2e test (Postgres/RabbitMQ thật, một HTTP server thật với một JWT RS256 thật) đều pass,
  `cargo build --release --workspace` sạch. Hai bug thật chỉ được bắt bởi e2e/live
  verification (một gap defaulting `data`/`status` trong `CrudService`, một panic ở
  CORS-config chỉ tái hiện được với một origin list không rỗng) — cả hai đã fix, cả hai giờ
  đều có test bao phủ.
- ~~Chứng minh việc port trên business entity thật, không chỉ fixture~~ — **Đã xong
  (2026-08-07)**: `apps/crm-server` (ban đầu là `crates/crm-server`, chuyển đi khi `crates/`
  được scope lại chỉ còn library crate + ops binary — xem ghi chú Repo Structure bên dưới),
  một binary tương đương `apps/crm` thật, chạy đúng entity `crm.customers` (port từ
  `customer.entity.ts`), đã verify live qua HTTP — chạy bằng `pnpm dev:rs`.
- ~~Xóa `apps/crm`/`packages/core` một khi phần port không còn cần chúng nữa~~ — **Đã xong
  (2026-08-07)**. Đóng ba gap trước để không có gì bị bỏ rơi âm thầm: JWT key chuyển sang
  `crates/crm-server/keys/`, ba dev script `packages/core/scripts/*.mjs` trở thành các
  subcommand của `crates/dev-tools`, và SQL migration của Drizzle được copy sang
  `crates/migrations/` cùng `crates/db-migrate` (`sqlx::migrate!`) được thêm vào để apply
  chúng — đã verify bằng cách chạy toàn bộ e2e suite trên một database được migrate từ đầu
  chỉ bằng công cụ đó, *trước khi* xóa bất cứ thứ gì. `packages/platform-react`/`apps/crm-fe` không bị đụng đến
  (frontend vốn luôn chỉ giao tiếp qua HTTP). Gap đã biết được phát hiện lúc đó: các admin
  HTTP route (policy CRUD, role grant/revoke) chưa tồn tại qua HTTP, chỉ tồn tại như các hàm
  có e2e coverage — đã đóng 2026-08-08, xem `crates/metap-http/src/routes/admin.rs`
  (extractor `AdminContext` yêu cầu role `admin`; `/admin/users`,
  `/admin/users/{userId}/roles[/{role}]`, `/admin/policies[/{id}]`,
  `/admin/policies/explain`), đã verify live trên một dev stack Postgres/RabbitMQ thật
  (assign/revoke/list role, create/list/delete/explain policy, 401 khi chưa auth, 403 khi
  không phải admin).
- Cut stack Rust sang thực sự phục vụ traffic. — **Chưa bắt đầu.** Chưa tồn tại production
  deployment topology cho việc này (cùng gap mà Phase 8 Hardening đã track cho stack TS);
  đây là một quyết định riêng, sau này, không mặc nhiên kéo theo khi việc port hoàn tất.
- Retarget các spec đang author bằng TS còn dang dở của Phase 11 (bắt đầu từ
  `docs/low-code-metadata-storage-design.md`) sang Rust trước khi implement chúng. — Chưa
  bắt đầu.

