## Phase 49: Nền tảng GraphQL + gRPC cho metap — chuẩn bị cho các microservice WAAP (2026-08-29)

Chủ dự án sắp xây "WAAP" (sản phẩm bảo mật kiểu Cloudflare dashboard) thành một hệ thống thật
sự nhiều microservice — mỗi phân hệ con là 1 binary riêng dựng trên nền `metap` (đúng khuôn
`apps/crm-server`/`apps/jira-server`, sinh từ `templates/metap-app/`). Portal frontend cần 1
GraphQL BFF gateway gộp dữ liệu nhiều microservice cùng lúc; các microservice cần gọi thẳng nhau
qua gRPC — đúng 2 trigger đã ghi từ trước trong `docs/architectures/04-strategy.md`/
`docs/roadmap/09-multi-service-evolution.md` nhưng chưa từng xảy ra thật, giờ xảy ra thật với
WAAP. **Phạm vi phase này (chốt với chủ dự án, dùng `EnterPlanMode` trước khi code)**: chỉ thêm
năng lực GraphQL/gRPC tái sử dụng được vào chính repo `metap` — không làm portal/gateway WAAP
thật, không làm entity WAAP (việc đó ở repo riêng, sau này). Yêu cầu bảo mật: ưu tiên bảo mật cao
+ hiệu năng cho mô hình auth đa-service, chấp nhận làm phức tạp hơn thay vì đi tắt.

**Bước 1 — tách JWT decode ra khỏi `axum::FromRequestParts`:** logic decode+validate JWT trong
`crates/metap-http/src/auth.rs` trước đây nằm chết trong `impl FromRequestParts`, không tái dùng
được cho transport khác. Tách thành `metap_peripherals::{AccessClaims, decode_access_token}` (hàm
thuần, không DB) + `metap_control::{ContextAttributesCache, resolve_request_context}` (nửa chạm-DB
— đặt ở `metap-control` chứ không phải `metap-peripherals` để tránh vòng phụ thuộc, vì
`metap-control` đã phụ thuộc `metap-peripherals`). `AuthContext::from_request_parts` giờ chỉ gọi 2
hàm này. Verify: 11/11 test e2e thật (`jwt_security_postgres`, `http_server`) pass y hệt sau
refactor — hành vi REST không đổi.

**Bước 2 — `metap-jwks` + `metap-jwks-http`:** mô hình tin cậy JWKS thật cho đa-service, thay vì
chia sẻ 1 keypair tĩnh thủ công (kiểu `CRON_SERVICE_JWT` hiện có, quá thô sơ cho WAAP). Dùng
Ed25519 (`EdDSA`) cho token liên-service mới (nhỏ hơn, verify nhanh hơn RS256 hiện tại — RS256 vẫn
giữ nguyên cho login user từng app). `JwksKeyStore` hỗ trợ rotation 3 bước (publish → promote →
remove) — key mới publish trước, verifier tự bắt kịp qua cache-miss-thì-fetch-lại-toàn-bộ-JWKS,
không cần deploy đồng bộ. `metap-jwks-http` expose `GET /.well-known/jwks.json` (chỉ service được
chỉ định làm issuer mới mount). Test: sign/verify roundtrip, cross-key rejection, JWK Set
round-trip JSON, rotation 3 bước, `JwksClient` verify qua mock HTTP server thật (`wiremock`), rotate
key mới rồi verify ngay (cache-miss tự fetch lại) — 8 + 2 test, tất cả pass.

**Bước 3 — `metap-grpc`:** CRUD-over-gRPC generic theo metadata, 1 proto `RecordService` (đổi tên
từ "CrudService" để tránh đụng `metap_crud::CrudService`) — `list/get/create/update/transition/delete`
dùng `google.protobuf.Struct` cho payload thay vì message riêng từng entity, đúng tinh thần
metadata-driven runtime (giống lý do REST/OpenAPI generic). `build.rs` dùng `protoc-bin-vendored`
(máy dev không có `protoc` cài sẵn) — không cần setup thêm khi checkout. Auth: forward JWT qua gRPC
metadata cho on-behalf-of-user (JWKS hoặc khoá tĩnh); mTLS cho M2M thuần cố tình để ngỏ làm điểm mở
rộng (cần convention cert thật từ hạ tầng triển khai WAAP, không bịa ra ở đây). Triển khai: 1 port
thứ 2, `tonic::transport::Server` chạy trong `tokio::spawn` riêng — đúng khuôn
`NOTIFICATION_WORKER_INLINE` đã có, không gộp vào hyper server của axum. Phát hiện + sửa 1 bug thật
khi viết test: `serde_json::Number::from_f64` làm số nguyên như `version` không đọc được bằng
`.as_i64()` nữa (luôn tạo Number dạng float) — sửa `convert.rs` tự nhận diện số nguyên, có test
regression riêng. Verify: test e2e thật (Postgres + JWT thật + tonic client/server thật qua mạng) —
unauthenticated reject, create, get, transition, stale-version reject (Aborted), update đúng
version, list, delete, get-sau-khi-xoá (NotFound) — pass toàn bộ.

**Bước 4 — `CrudService::get_many`:** batch-get cho DataLoader (GraphQL cần), thêm
`crates/metap-crud/src/crud_service/get_many.rs` + `fetch_existing_batch` trong `helpers.rs` — 1
query `WHERE id = ANY($1)` thay vì N lần `get`, chạy đúng pipeline permission/mask như `get` (1
`load_snapshot` cho cả batch). Thứ tự trả về khớp `ids` truyền vào (không phải thứ tự DB trả về);
id không tồn tại hoặc bị record-level policy deny thì đơn giản vắng mặt trong kết quả, không phải
lỗi — khớp semantics "reference không giải quyết được → null" đã có sẵn ở chỗ khác trong hệ thống.
Test e2e thật: 3 record (amount 10/20/30), record-level policy deny amount=20 cho role "viewer",
truyền `ids` xáo trộn kèm 1 id không tồn tại — xác nhận amount=20 và id-không-tồn-tại đều vắng mặt,
2 record còn lại đúng thứ tự đã truyền vào. Chạy lại 12 test khác (trừ 3 load-test dài) — không phá
gì.

**Bước 5 — `metap-graphql` + `metap-graphql-http`:** schema GraphQL sinh động từ `MetadataRegistry`
lúc runtime bằng `async-graphql`'s module `dynamic` (không phải static/derive-macro — entity không
biết trước lúc compile, giống lý do `openapi.rs` không thể `#[derive(JsonSchema)]`). Mỗi entity → 1
Object type (`Query.{camel}(id)`/`Query.{camel}List(...)`/`Mutation.create{Type}`/`update{Type}`/
`delete{Type}`/`transition{Type}` nếu có workflow) — đặt tên field cố định `{camel}List` thay vì cố
đoán số nhiều tiếng Anh (không đáng tin với tên entity tự do). 3 yêu cầu bắt buộc của GraphQL trên
nền tảng này đều giải quyết được, không cái nào cần code phức tạp riêng:
- **DataLoader (chống N+1)**: `Reference` field resolve qua `RecordLoader` (key `(entity, id)`,
  gộp theo entity trong 1 tick rồi gọi `get_many` — bước 4 tồn tại chính vì việc này), 1 instance
  mới mỗi request (không rò batch cache giữa request/tenant khác nhau).
- **Complexity/depth limit**: có sẵn trong `SchemaBuilder::limit_depth`/`limit_complexity` của
  `async-graphql`, chỉ cần set từ config, không tự viết.
- **Field-level permission masking**: **không cần code thêm** — mọi resolver đọc từ
  `RecordDto.data` đã được `filter_readable_fields` mask sẵn bên trong `CrudService` (y hệt REST),
  field bị cấm chỉ đơn giản vắng mặt trong JSON nên tự động trả `null`. Vì field Reference resolve
  qua lại đúng `CrudService::get`/`get_many`, việc mask tự đệ quy xuyên các tầng nested — không có
  đường mask riêng thứ 2 nào phải viết hay dễ quên viết.

Verify bằng 4 test e2e thật (Postgres + policy thật): vòng đời tạo/lấy/transition qua workflow thật,
mở rộng Reference field qua DataLoader (2 con trỏ tới cùng 1 cha), field-level deny trả `null` đúng
qua GraphQL, depth-limit chặn đúng query lồng sâu, và 1 test riêng cho `metap-graphql-http` (server
axum thật, gộp REST + `/graphql`, JWT thật) — 401 khi chưa auth, mutation/query thật qua Postgres
thật. Bug thật phát hiện khi viết test: quên khai báo field "status" trùng tên envelope (mirror
cột `RecordDto.status`) → panic "Field already exists" — sửa bằng danh sách `ENVELOPE_FIELD_NAMES`
loại trừ field trùng tên khi sinh Object type.

**Bước 6 — cập nhật `templates/metap-app/`:** chỉ thêm doc-comment hướng dẫn opt-in GraphQL/gRPC
trong `src/main.rs` (đúng kiểu hướng dẫn `metap-lowcode-http` đã có sẵn) — không thêm dependency
mặc định, không tạo template thứ 2 riêng cho WAAP (tránh 2 template lệch nhau theo thời gian).

**Kiểm chứng cuối cùng**: `cargo build/clippy -D warnings/fmt --check` sạch toàn workspace (bao
gồm `apps/crm-server`/`apps/jira-server` — hoàn toàn không bị ảnh hưởng, không phụ thuộc bất kỳ
crate mới nào). Toàn bộ crate mới (`metap-jwks`, `metap-jwks-http`, `metap-grpc`, `metap-graphql`,
`metap-graphql-http`) đã thêm vào `Cargo.toml` workspace và facade `crates/metap/src/lib.rs`
(`metap::jwks`/`jwks_http`/`grpc`/`graphql`/`graphql_http`).

**File chính đã thêm/sửa** — sửa: `crates/metap-http/src/{auth,cache}.rs`,
`crates/metap-peripherals/src/{auth,lib}.rs`, `crates/metap-crud/src/crud_service.rs` (mod mới),
`crates/metap-crud/src/crud_service/helpers.rs` (`fetch_existing_batch`),
`crates/metap-control/src/lib.rs` (mod mới `auth_context`), `crates/metap/{Cargo.toml,src/lib.rs}`,
`templates/metap-app/src/main.rs`. Crate mới: `crates/metap-jwks`, `crates/metap-jwks-http`,
`crates/metap-grpc` (+ `proto/metap_crud.proto`, `build.rs`), `crates/metap-graphql`,
`crates/metap-graphql-http`, cùng `crates/metap-crud/src/crud_service/get_many.rs`.

**Còn lại, cố ý chưa làm** (đúng ranh giới đã chốt): repo/binary WAAP thật (portal gateway, entity
WAAP) — việc sau, ở repo riêng; cấp phát/rotate mTLS certificate cho gRPC M2M — hạ tầng triển khai
của WAAP, không phải việc của crate `metap-grpc` (chỉ nhận `ServerTlsConfig` optional); GraphQL
Enum field hiện là `String` thô thay vì GraphQL enum type thật (đơn giản hoá v1, không mất thông
tin — giá trị enum vẫn đúng, chỉ là kiểu SDL kém chặt hơn); numbers đi qua `google.protobuf.Struct`
(gRPC) mất phân biệt int/float ở tầng transport (giới hạn thiết kế của `Struct`, không phải bug —
`CrudService` validate/coerce lại đúng kiểu ngay sau đó).
