## Phase 23: `metap-cache` — caching layer swappable, policy-row caching là consumer đầu tiên (2026-08-23)

Bắt nguồn từ đánh giá kiến trúc bên ngoài (`REVIEWED.md`) và khảo sát lại caching hiện có trong
repo: `RegistryCache`/`Router::dedicated_pools`/`ContextAttributesCache` đều đã dùng `moka`
in-process riêng lẻ, nhưng `PermissionSnapshot::load` — đường nóng nhất, gọi lại `policies` table
từ Postgres trên **mọi** lệnh gọi `CrudService` (list/get/create/update/transition/delete) — doc
comment cũ của chính nó ghi rõ "deliberately not a cross-request/TTL cache", tức là chưa từng được
cache thật. Đây là mục tiêu cache đầu tiên có ý nghĩa hiệu năng thật.

**Thiết kế**: `crates/metap-cache` — trait `Cache` (`get`/`set`/`invalidate`, tenant-scoped bắt
buộc ở chữ ký như `metap-storage::ObjectStore` đã làm, `set` không có tham số `ttl` per-call — 1
`Cache` instance = 1 TTL cố định lúc khởi tạo, đúng quy ước `RegistryCache`/`ContextAttributesCache`
đã dùng) + 2 impl:
- **`MokaCache`** — in-process, cùng thư viện `moka` đã dùng khắp nơi, không thêm dependency mới.
  Rẻ nhất nhưng mỗi instance server tự warm cache riêng — đúng cho topology single-instance hiện
  tại, sai một khi có nhiều instance.
- **`RedisCache`** — distributed, qua bất kỳ server nói RESP protocol nào (Redis/DragonflyDB/
  Valkey/KeyDB tương thích client như nhau — đổi backend chỉ là đổi connection string, không đổi
  code, đúng tinh thần "interface, không khoá công nghệ" đã áp dụng cho `ObjectStore`). **Chủ dự
  án yêu cầu nghiên cứu Redis so với DragonflyDB** ("đang hot trend") trước khi chọn: benchmark
  công khai cho thấy DragonflyDB đạt throughput single-node cao hơn Redis đáng kể (~5-10x thực tế,
  dù số 25x hay được PR là đo trên máy nhiều core), tiết kiệm RAM hơn ~30% với tập key nhỏ, nhờ
  kiến trúc multi-threaded/shared-nothing khác Redis (đơn luồng theo thiết kế gốc). Về license:
  Redis đã rời BSD sang SSPL/RSALv2 (3/2024), thêm lại AGPLv3 (5/2025); DragonflyDB dùng BSL
  (Business Source License, không hoàn toàn mã nguồn mở nhưng chấp nhận được cho self-host một
  node như dev stack ở đây) — không bên nào còn là Redis-BSD-cũ nữa. **Chọn DragonflyDB làm
  backend chạy trong `docker-compose.yml`'s dev stack**, nhưng code hoàn toàn không biết/quan tâm
  đó là DragonflyDB cụ thể — production có thể trỏ `POLICY_CACHE_REDIS_URL` vào Redis/Valkey thật
  mà không sửa dòng code nào.

**`docker-compose.yml`**: service `dragonfly` mới (opt-in, không nằm trong `up -d postgres
rabbitmq` mặc định, cùng quy ước `seaweedfs`/`vault`) — `dragonflydb/dragonfly:v1.40.1`, cổng 6379
(giống Redis mặc định).

**Wire vào `PermissionService`**: constructor mới `with_cache(store, cache)` cạnh `new()` hiện có
(không đổi hành vi bất kỳ call site nào không opt-in — mọi test hiện có, và `crm-server`/
`jira-server` khi `POLICY_CACHE_REDIS_URL` không set, vẫn query `PolicyStore` tươi trên mọi lệnh
gọi, y hệt trước khi cache tồn tại). `load_snapshot` kiểm tra cache trước (key
`policies:{entity}`, scope theo `tenant_id`), miss thì query `PolicyStore` rồi ghi lại cache; lỗi
đọc/ghi cache không làm fail request — log warn rồi rơi về store, không bao giờ để một cache bug
chặn đứng luồng permission. `create_policy`/`delete_policy` chủ động `invalidate()` entry liên
quan ngay khi ghi (không chỉ dựa vào hết TTL) — `delete_policy` không biết trước entity nào bị ảnh
hưởng nên tra `list_policies` trước khi xoá để tìm entity, chỉ chạy khi cache thật sự được cấu
hình (chi phí thêm bằng 0 cho deployment không dùng cache). `PolicyRow`/`PolicyEffect` thêm
`Serialize`/`Deserialize` để phục vụ serialize sang cache.

**Ranh giới bảo mật giữ nguyên, không nới**: role/`user_roles` **không** đi qua cache này —
`crates/metap-http/src/auth.rs` vẫn tra `user_roles` tươi trên mọi request, không đổi. Chỉ policy
*rule* (dữ liệu admin chỉnh sửa hoạ hoằn) được cache, không phải role *assignment* — cùng ranh
giới `ContextAttributesCache` đã vạch giữa "ordinary business record" và "security-critical role
assignment".

**Bug thật tìm được lúc verify sống**: DragonflyDB không pin `--proactor_threads`/`--maxmemory` tự
tính dung lượng theo **RAM khả dụng hiện tại** (không phải RAM tổng) nhân với số IO thread phát
hiện từ host (~256MB/thread) — container khởi động bình thường lúc host còn nhiều RAM trống, sau
đó **tự thoát** (`There are 16 threads, so 4.00GiB are required. Exiting...`) khi `cargo build`
song song đã chiếm phần lớn RAM trống, khiến e2e test đang chạy nền báo nhầm "Connection refused"
(trông như bug ở `RedisCache`, thực ra là container đã chết từ trước khi test bắt đầu). Sửa bằng
cách pin cứng `--proactor_threads=2 --maxmemory=512mb` trong `docker-compose.yml` — dung lượng cố
định phù hợp cho 1 cache dev, không phụ thuộc RAM khả dụng tại thời điểm container khởi động, cùng
tinh thần "sized for this dev host, not a production box" đã ghi ở comment `shared_buffers` của
`postgres` trong cùng file.

**Kiểm chứng sống**: `MokaCache` — 5 unit test (get/set, miss, tenant-isolation, invalidate,
TTL-expiry thật qua `tokio::time::sleep`). `RedisCache` — 3 test e2e `#[ignore]`d chạy thật qua
DragonflyDB (`docker compose up -d dragonfly`): round-trip put/get/delete, tenant-isolation, TTL
expiry thật. `PermissionService` — test e2e mới
(`crates/metap-permission/tests/rbac_abac_integration_postgres.rs`,
`load_snapshot_is_cached_and_invalidated_on_policy_write`) dùng `CountingPolicyStore` (wrap
`PolicyStore` thật, đếm số lần `load_all_policies` bị gọi) để chứng minh — không chỉ tin — rằng:
lần `load_snapshot` thứ 2 cho cùng tenant/entity **không** gọi lại store (được phục vụ từ cache),
và `create_policy` sau đó khiến lần `load_snapshot` kế tiếp **phải** gọi lại store (cache đã bị
invalidate đúng, không trả dữ liệu cũ). `cargo fmt --check`/`clippy --workspace --all-targets -D
warnings`/`build --workspace` sạch toàn bộ; unit test toàn workspace (`cargo test --workspace
--lib --bins`) 0 failed.

