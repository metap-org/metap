## Phase 22: `metap-storage` — object storage interface swappable, SeaweedFS làm backend đầu tiên (2026-08-23)

Nghiên cứu trước khi làm (không suy đoán): claim "MinIO đã EOL" của chủ dự án **đúng và nghiêm
trọng hơn tưởng** — không chỉ đổi license, mà 3/2025 gỡ Web Console khỏi bản Community, 12/2025
chuyển "maintenance mode", 4/2026 **repo chính thức bị archive** (read-only, hết patch bảo mật).
Khảo sát alternative tự host: SeaweedFS (Apache 2.0, 12+ năm, rất active), Garage (AGPLv3, Rust,
1 binary nhưng thiếu ACL/versioning), RustFS (Apache 2.0, cùng stack Rust, nhưng còn `rc.3`, tự
nhận Distributed Mode/Lifecycle "Under Testing"), Ceph RGW (feature-complete nhất nhưng quá nặng
cho docker-compose nhỏ). Quyết định: **SeaweedFS** làm backend đầu tiên (trưởng thành nhất, tương
thích S3 API tốt với `aws-sdk-s3` không cần workaround) — nhưng **interface phải tách khỏi
backend cụ thể** để sau này đổi sang RustFS/Garage/AWS S3 thật dễ dàng, đúng yêu cầu chủ dự án.

**Thiết kế**: `crates/metap-storage` — `ObjectStore` trait (`put`/`get`/`delete`/`exists`, key +
bytes thuần, cố tình không lộ khái niệm S3 nào ra trait — bucket/ETag/versioning/multipart đều
không xuất hiện trong signature) + `S3ObjectStore` (impl duy nhất hiện tại, dựng trên `aws-sdk-s3`
với `endpoint_url` cấu hình được) — cùng hình dạng `SecretStore` (`EnvStore`/`VaultStore`) và
`EventBus` (`RabbitEventBus`) đã có sẵn trong repo, không phát minh pattern mới. Đổi sang RustFS/
Garage/AWS S3 thật chỉ cần đổi `S3ObjectStoreConfig` (endpoint/credentials) — cùng 1 impl, vì mọi
backend đang xét đều nói S3 HTTP API; đổi sang thứ không nói S3 (disk cục bộ, API cloud khác) mới
cần impl `ObjectStore` mới, không phải sửa lại mọi call site.

**`docker-compose.yml`**: service `seaweedfs` mới (opt-in, không nằm trong `up -d postgres
rabbitmq` mặc định, cùng quy ước với `vault`) — `chrislusf/seaweedfs:3.97`, chạy `server -s3` (1
container chứa cả master+volume+filer+S3 gateway, đúng quy mô cho stack dev nhỏ, không cần tách
nhiều node như topology production của SeaweedFS).

**Chưa wire vào route/feature nào** — thêm trước khi có consumer cụ thể (vd file đính kèm trên
entity), đúng thứ tự `metap-reconciler` từng được xây trước khi `apps/jira-server` cần tới.

**Kiểm chứng sống**: `docker compose up -d seaweedfs` — chờ ~15-20s cho chuỗi khởi động nội bộ
(master → volume → filer → S3 gateway) hoàn tất, S3 API trả `200` ở cổng 8333. Test e2e thật
(`crates/metap-storage/tests/s3_seaweedfs.rs`, `#[ignore]`d theo đúng quy ước `*_postgres.rs` đã
có) — vòng đời đầy đủ: `exists`/`get` trên key chưa tồn tại đúng `false`/`None` (không phải lỗi),
`put`, `exists`/`get` đúng, ghi đè (overwrite) thay hoàn toàn nội dung cũ, `delete`, `exists`/`get`
sau xoá đúng `false`/`None`, xoá lần 2 trên key đã xoá không lỗi (đúng ngữ nghĩa idempotent của S3
`DELETE`) — pass thật qua SeaweedFS đang chạy, không mock. `cargo fmt --check`/`clippy --workspace
--all-targets -D warnings`/`build --workspace` sạch. Tắt lại container sau khi verify xong (opt-in,
không để chạy ngầm ngoài ý muốn).

**Sửa security-first sau review** (chủ dự án: "thiết kế phải đảm bảo security first nữa nhé") —
bản đầu chỉ có `put(key, ...)` trần, tin tưởng caller tự prefix key theo tenant, đúng loại lỗi
boundary đã gặp lặp lại trong phiên này (crud_service thiếu filter tenant_id, jira-server dùng
chung DB platform). Sửa:
- **Tenant scoping bắt buộc ở chữ ký trait**, không phải quy ước caller: mọi method
  (`put`/`get`/`delete`/`exists`) nhận `tenant_id: Uuid` làm tham số đầu tiên — cùng nguyên tắc
  `Router::begin(tenant: TenantId)`/`CrudService`'s `WHERE tenant_id = $1` đã áp dụng: chặn ở
  choke point, không dựa vào caller nhớ đúng. `S3ObjectStore` tự build key thật
  (`{tenant_id}/{key}`) nội bộ qua `scoped_key()` — không call site nào tự nối chuỗi.
- **`validate_key()`** chặn key dạng path-traversal (`..`/`.` segment), key rỗng, key bắt đầu
  bằng `/`, dài quá 1024 byte (giới hạn thật của S3), chứa control character — chạy trước khi
  request chạm backend, cùng kỷ luật `Router::validate_schema_name`/`table_name` validate đã có.
- **`access_key`/`secret_key` đổi từ `String` sang `secrecy::SecretString`** — cùng convention
  `metap-control::DbCreds.dsn` đã dùng, tránh lộ qua `{:?}`/log vô tình.
- **Verify sống**: thêm 2 test e2e mới — `same_key_different_tenants_never_collide` (2 tenant ghi
  cùng 1 key logic, đọc lại đúng nội dung riêng, xoá của tenant A không đụng tenant B) và
  `traversal_shaped_key_is_rejected_before_it_reaches_the_backend`. Soi trực tiếp qua SeaweedFS
  filer API (`GET /buckets/.../`) xác nhận key vật lý thật sự có prefix UUID tenant, không chỉ
  tin vào assertion Rust. `cargo fmt`/`clippy -D warnings`/test đều sạch.

