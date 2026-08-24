## Phase 27: Generalize attachment thành năng lực platform (`metap-http`), không riêng jira (2026-08-24)

Ngay sau khi Bước 4/4 xong, chủ dự án chỉ ra đúng: mọi thứ vừa viết trong
`apps/jira-server/src/attachment_routes.rs` (multipart → `ObjectStore::put` → tạo metadata; `get`
metadata → `ObjectStore::get` → stream) hoàn toàn generic, chỉ có literal `"jira.attachments"`/
URL prefix `/api/jira.issues/...` là hardcode cho jira. Đúng tinh thần
`/api/:entity/:id/transitions/:action` đã generic theo `:entity` — nên đưa lên `metap-http` làm
route chung, không phải mỗi app tự viết lại.

**Quyết định thiết kế chốt cùng chủ dự án**: **2 cơ chế lưu trữ song song**, không phải chọn 1 —
1 bảng SQL chung toàn platform làm mặc định (đủ cho hầu hết entity), và tuỳ chọn bảng riêng cho
entity cần nhiều volume (tối ưu hiệu năng riêng sau này).

- Crate mới `crates/metap-attachments` (plain library, cùng hình dạng `metap-cron`) —
  `AttachmentRecord` + `create`/`list`/`get`/`delete_attachment`, mọi hàm nhận `table_name: &str`
  tường minh (không hardcode `"attachments"`) — đây chính là cơ chế "2 lựa chọn": gọi với tên bảng
  chung hoặc tên bảng riêng. `ensure_dedicated_table` tạo bảng riêng (schema cố định 8 cột, không
  cần máy móc reconcile() động như table-per-entity cho record chính vì shape không đổi theo
  entity). `validate_table_name` chặn injection qua identifier, cùng discipline
  `Router::validate_schema_name`.
- `crates/migrations/0021_attachments.sql` — bảng `attachments` mặc định toàn platform (như
  `policies`/`cron_jobs`, không phải per-entity JSONB — `record_id` trỏ "bất kỳ entity nào" nên
  không gắn FK type-safe được).
- `crates/metap-http/src/routes/attachments.rs` (mới) — 4 route generic
  (`POST`/`GET /api/{entity}/{id}/attachments`, `GET .../download`, `DELETE .../{attachmentId}`),
  merge thẳng vào `build_router` (không qua `extra_routes` nữa — áp dụng mọi app, app không set
  `object_store` chỉ nhận 503). Permission check thẳng qua
  `PermissionService::can_read/update/delete_entity(&context, entity_name)` (đã public, không cần
  entity đăng ký `MetadataRegistry` — xác nhận đọc code). `AppState` thêm
  `attachment_tables: Arc<HashMap<String,String>>` (entity → tên bảng riêng, mặc định rỗng).
  `DELETE` giờ xoá cả blob thật trong `ObjectStore` — đóng luôn gap "blob mồ côi" đã ghi nhận ở
  Phase 26.
- **`apps/jira-server` đơn giản hơn nhiều**: xoá hẳn `entities/attachment_entity.rs` +
  `attachment_routes.rs`, chỉ còn giữ khối wiring `S3ObjectStore` (~15 dòng) — không cần biết route
  ở đâu nữa.
- **Đánh đổi đã lường trước, verify sống đúng như dự đoán**: bản jira-specific cũ chặn xoá issue
  đang có attachment "miễn phí" qua `find_referencing_record` (Reference field thật trong
  metadata). Bảng chung mới không có FK kiểu này (`metap-crud` không được phép biết khái niệm
  "attachments") — **verify sống xác nhận đúng**: tạo issue test có attachment → xoá issue → HTTP
  200 (không còn bị chặn), để lại attachment record mồ côi (blob không mất, chỉ tham chiếu treo).
  Route xoá attachment vẫn hoạt động được cho record mồ côi (verify riêng) — dọn tay vẫn làm được
  khi cần, chỉ là không tự động.
- **Gap migration thật phát hiện lúc verify — đã fix (2026-08-24, sau khi commit).** Migration
  `0019` (Phase 25's `tenant_auth_configs`) có bước backfill query `control.tenants` — chạy lại
  `db-migrate` cho 1 tenant `dedicated_db` **đã provision từ trước** (nên đã tự `DROP SCHEMA
  control`) lỗi ngay ở migration 19, chặn cả 20/21 chạy theo sau (`sqlx::migrate!` áp tuần tự, 1
  cái lỗi chặn hết). Hậu quả thật (không chỉ lý thuyết): `metap_myjira` (DB dedicated của tenant
  jira) bị **kẹt ở migration 18** — `tenant_auth_configs` chưa hề tồn tại, `users` chưa có
  `auth_provider`/`external_subject`, `attachments` (0021) cũng chưa có thật (chỉ có DDL tôi áp
  tay tạm bợ lúc verify Phase 27, không qua `sqlx::migrate!` nên checksum không khớp) — nghĩa là
  `GET /auth/providers` cho tenant này **thực ra đã lỗi ngầm** từ lúc Phase 25 merge tới giờ.
  **Cách sửa đúng** (không sửa nội dung migration 19 — đã chạy thật ở DB platform, sửa sẽ vỡ
  checksum `sqlx::migrate!` lưu cho DB đó): tái tạo tạm bảng `control.tenants` rỗng đúng schema
  gốc migration `0012` trong `metap_myjira`, xoá bảng `attachments` đã tạo tay trước đó, chạy
  `db-migrate` thật (19/20/21 tự áp đúng qua migrator thật, checksum tự sinh chính xác, không
  đoán tay), seed đúng 1 row `local` cho tenant này vào `tenant_auth_configs` (backfill của 19
  đúng ra trả về 0 dòng vì `control.tenants` rỗng — đúng, vì bên trong 1 DB dedicated riêng của 1
  tenant thì backfill từ `control.tenants` — vốn liệt kê **mọi** tenant toàn platform — vốn dĩ sai
  ngữ nghĩa; giống hệt `metap-control::provisioning::seed_local_auth_config` đã làm đúng cho
  tenant mới), rồi `DROP SCHEMA control CASCADE` lại đúng invariant thiết kế (dedicated DB không
  bao giờ giữ `control` schema). Áp lại tương tự cho DB platform (thiếu migration 21 vì
  `sqlx::migrate!` không tự phát hiện file mới thêm — cùng footgun `touch src/main.rs` để ép
  rebuild đã gặp trước đó với `db-migrate`).
  **Verify sống sau fix**: `GET /auth/providers` trả đúng `{"providers":["local"]}` (trước đó lẽ
  ra phải lỗi 500 vì bảng không tồn tại), login thật vẫn hoạt động, route attachment vẫn hoạt
  động đúng (bảng `attachments` giờ được `sqlx::migrate!` quản lý thật, không còn là DDL tay).
  **Bài học chung, áp dụng cho mọi migration tương lai**: migration chạm `control.*` chỉ an toàn
  chạy lúc **provision lần đầu** (khi `control` còn tồn tại tạm thời trên DB dedicated đó) —
  không bao giờ an toàn để backfill dữ liệu **từ** `control.tenants` bên trong 1 DB dedicated
  (ngữ nghĩa sai — DB đó chỉ nên biết về chính nó, không phải toàn bộ tenant khác), và không an
  toàn `db-migrate` lại cho 1 tenant dedicated đã tồn tại nếu có migration mới đụng `control.*`
  ở giữa — nên tránh hẳn kiểu backfill này cho các migration sau, chỉ dùng cho bảng platform
  dùng chung thật sự (`policies`/`cron_jobs`), không phải bảng sống trong từng DB tenant riêng.
- **Kiểm chứng sống đầy đủ qua HTTP thật** (route generic, không phải bespoke cũ): upload →
  list → download byte-for-byte đúng → delete → xác nhận **blob cũng biến mất khỏi SeaweedFS**
  (list bucket qua S3 API trước/sau). Tình cờ thấy luôn 1 file chủ dự án đã tự upload thử qua UI
  trước đó ("Hướng dẫn tải app viettel family.docx") — xác nhận tính năng đã chạy được cho người
  dùng thật, không chỉ qua curl. `cargo build/fmt --check/clippy --workspace --all-targets -D
  warnings` + `cargo test --workspace` sạch. `pnpm --filter @metap/jira-fe build`/`lint` sạch.

Diff chưa commit.

