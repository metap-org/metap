# Phase 50 — BFF gateway thật xuyên microservice (`graphql-gateway`)

## Trigger

Phase 49 xây xong nền tảng GraphQL/gRPC/JWKS làm thư viện (`metap-graphql`, `metap-grpc`,
`metap-jwks`), nhưng chưa wire vào binary nào cả. Ngay sau Phase 49, bước tiếp theo trong phiên
làm việc là gắn `metap-graphql-http::router()` vào `apps/jira-server` — lần đầu tiên một service
thật tự phục vụ GraphQL cho chính entity của nó. Chủ dự án hỏi thẳng: "vậy giờ nó BFF kiểu gì?" —
và đúng: đó **chưa phải BFF thật**. Một service tự expose GraphQL cho chính entity của nó không
khác gì REST tự expose entity của nó qua giao thức khác — không có sự **aggregate xuyên nhiều
service** mà BFF (Backend-for-Frontend) đúng nghĩa phải có, và đúng chỗ
`docs/architectures/04-strategy.md` từng ghi nhận là "cần làm nhưng chưa có trigger": *"lời gọi
entity của module đó từ BFF phải chuyển từ in-process call (`CrudService`) sang remote call...
cùng một call site nhưng hành vi khác theo deployment topology"*.

Chốt phạm vi trước khi code (`EnterPlanMode`), chủ dự án chọn: **"Xây luôn binary gateway BFF
thật"** — không dừng ở việc mô tả khoảng trống, xây hẳn binary gateway đứng trước cả
`jira-server` lẫn `crm-server`.

## Kiến trúc

### 1. `RecordBackend` (`metap-crud`) — điểm neo local-vs-remote dispatch

```rust
#[async_trait::async_trait]
pub trait RecordBackend: Send + Sync {
    async fn list(&self, entity: &str, input: &ListInput, ctx: &RequestContext) -> anyhow::Result<ServiceResult<Vec<RecordDto>>>;
    async fn get(&self, entity: &str, id: Uuid, ctx: &RequestContext) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>>;
    async fn get_many(&self, entity: &str, ids: &[Uuid], ctx: &RequestContext) -> anyhow::Result<ServiceResult<Vec<(Uuid, RecordDto, RecordCapabilities)>>>;
    async fn create(&self, entity: &str, data: &JsonObject, ctx: &RequestContext) -> anyhow::Result<ServiceResult<RecordDto>>;
    async fn update(&self, entity: &str, id: Uuid, version: i32, data: &JsonObject, ctx: &RequestContext) -> anyhow::Result<ServiceResult<RecordDto>>;
    async fn transition(&self, entity: &str, id: Uuid, action: &str, version: i32, data: Option<&JsonObject>, ctx: &RequestContext) -> anyhow::Result<ServiceResult<RecordDto>>;
    async fn delete(&self, entity: &str, id: Uuid, version: i32, ctx: &RequestContext) -> anyhow::Result<ServiceResult<RecordDto>>;
}
```

Đặt ở `metap-crud`, không phải `metap-graphql` hay crate mới: mọi type trong signature đã sống
sẵn ở đây, và cả `metap-graphql` lẫn `metap-grpc` đều đã phụ thuộc `metap-crud` từ trước — dùng
chung 1 trait ở đây không thêm dependency mới cho crate nào, không có vòng phụ thuộc chéo
`metap-graphql`↔`metap-grpc` phải giải quyết. `impl RecordBackend for CrudService` cũng đặt tại
đây — 7 hàm khớp signature sẵn, chỉ là 1 impl block mỏng delegate thẳng.

`metap-graphql`'s `build_schema`/`with_request_data`/`RecordLoader` đổi từ nhận `Arc<CrudService>`
sang `Arc<dyn RecordBackend>` — **hành vi single-service không đổi**: `jira-server`/`crm-server`
vẫn truyền `state.crud.clone()` thẳng (giờ chỉ là 1 kiểu cụ thể của `Arc<dyn RecordBackend>`, nhờ
Rust ưu tiên inherent method trước trait method nên `impl RecordBackend for CrudService`'s
`self.list(...)` gọi đúng `CrudService::list` chứ không đệ quy vô hạn).

### 2. `metap_grpc::GrpcBackend` — implement `RecordBackend` bằng gọi mạng thật

Bọc `RecordServiceClient<Channel>` (đã có từ Phase 49), dùng lại `convert::{struct_to_json,
json_to_struct}`. Mỗi lời gọi gắn `authorization: Bearer {service_jwt}` — **1 JWT tĩnh, mint sẵn
cho từng upstream**, đúng tiền lệ `CRON_SERVICE_JWT` đã có trong repo.

**Giới hạn cố ý, ghi rõ trong code (doc comment) lẫn README**: identity người gọi gateway **không**
propagate xuống downstream — mọi request qua gateway tới 1 upstream dùng chung 1 service identity
cố định của upstream đó. Muốn propagate identity thật cần mọi upstream cùng verify qua 1 JWKS
trust root chung (`metap-jwks` đã có làm thư viện từ Phase 49, nhưng `GrpcRecordService::auth`
của từng service hôm nay tự chọn `TokenVerifier::Static` hay `TokenVerifier::Jwks` riêng, không
có hạ tầng "mọi service cùng verify chung 1 JWKS" nào lắp sẵn) — ngoài phạm vi lần này.

Không có RPC batch thật trên dây (`RecordService`'s proto không có `GetMany`) — `get_many` hiện
implement bằng N lời gọi `get` tuần tự. DataLoader vẫn được lợi (N field Reference trong 1 query
gộp thành 1 lời gọi `get_many` ở tầng resolver, không phải N lời gọi resolver riêng), chỉ là
chưa N-thành-1 ở tầng network.

### 3. `CompositeBackend` (`metap-graphql`) — route theo entity name

```rust
pub struct CompositeBackend { by_entity: HashMap<String, Arc<dyn RecordBackend>> }
```

Đặt ở `metap-graphql`, không phải `metap-crud`: logic route theo entity name là đặc thù cho ca
gateway, không phải thứ `metap-crud` cần biết. `jira-server`/`crm-server` (single-service) vẫn
truyền thẳng `Arc<CrudService>` (đã impl sẵn `RecordBackend`), không cần composite.

### 4. `crates/graphql-gateway` (binary `graphql-gateway`, package `metap-graphql-gateway`)

Không sở hữu entity nào — thuần aggregator, không Postgres, không `CrudService`. Boot sequence:

1. Đọc `UPSTREAM_<N>_{NAME,GRPC_ADDR,METADATA_URL,SERVICE_JWT}`, `N = 1, 2, ...` tới khi thiếu
   `_NAME` — không thêm dependency parse config mới.
2. Với mỗi upstream: `GET {METADATA_URL}` thật (chính là `GET /metadata/entities` đã có sẵn từ
   trước, cần auth) kèm bearer `SERVICE_JWT`, parse response thành `EntityDefinition` (dùng thẳng
   `EntityField`/`EntityWorkflow` — cả hai đã `Deserialize` sẵn), và connect 1 `GrpcBackend` thật
   tới `GRPC_ADDR`.
3. `registry.register(entity)` cho từng entity vào 1 `MetadataRegistry` dùng chung — `register`
   tự reject tên trùng, nên 2 upstream cùng khai 1 tên entity fail ngay lúc boot, không cần check
   thêm.
4. Build `CompositeBackend` map mỗi entity name tới đúng `GrpcBackend` của upstream nó thuộc về,
   rồi `metap_graphql::build_schema(&registry, Arc::new(composite), SchemaLimits::default())`.
5. Serve `axum` app riêng (`GET /health`, `POST /graphql`, `GET /graphql/playground` non-prod) —
   không dùng `metap_http::build_router`/`AppState` (gắn chặt Postgres/`CrudService` gateway
   không có). Reuse 2 mảnh an toàn: `metap_http::security_headers::security_headers` (middleware
   thuần, không phụ thuộc `AppState`) và `metap_graphql_http::playground_router` (generic hoá
   `Router<AppState>` → `Router<S>` để dùng lại được ở đây). Auth vào `/graphql` chỉ decode token
   bằng khoá tĩnh riêng của gateway (`metap_peripherals::decode_access_token`) — gate "có token
   hợp lệ hay không", không phải nguồn identity cho downstream (xem mục 2).

### 5. `crm-server` có gRPC lần đầu

`apps/crm-server/src/main.rs` trước đây không có gRPC (khác `jira-server`, đã có từ Phase 49).
Thêm đúng khối `GRPC_ENABLED`/`GRPC_PORT` + `AuthConfig::Static` + `tokio::spawn(metap::grpc::serve(...))`
mirror y hệt `jira-server` — để gateway có **2 service thật** aggregate, không phải demo giả với
1 service.

### 6. Bug thật tìm được: CSP chặn GraphiQL

Chủ dự án báo lỗi qua console trình duyệt khi mở `GraphiQL` của `jira-server`:
`Content-Security-Policy` chặn script/style từ `unpkg.com` và ảnh favicon từ `graphql.org`. Gốc:
`crates/metap-http/src/security_headers.rs` ghi đè CSP header vô điều kiện
(`headers.insert(...)`) trên mọi response, kể cả response `playground_router` đã tự set CSP nới
hơn cho chính nó. Sửa bằng `headers.entry(CONTENT_SECURITY_POLICY).or_insert_with(...)` — route
nào tự set CSP trước thì giữ nguyên, mọi route khác vẫn dùng CSP mặc định như cũ.

## Kiểm chứng

- Bước 1-2 (refactor `RecordBackend`): chạy lại nguyên bộ test `metap-graphql`/`metap-graphql-http`
  hiện có — pass y hệt kết quả trước refactor (hành vi single-service không đổi).
- Bước 3 (`GrpcBackend`): 1 test e2e thật (`metap-grpc/tests/grpc_backend_client_postgres.rs`) —
  gRPC server thật + `GrpcBackend` client thật (không qua GraphQL), full lifecycle
  create/get/get_many/transition/update (bao gồm version-conflict 409)/list/delete, so khớp trực
  tiếp với gọi `CrudService` thẳng.
- Bước 5 (`crm-server` gRPC): verify thủ công — chạy thật `crm-server` với `GRPC_ENABLED=true`,
  gọi `GrpcBackend::connect` thật tới `crm.customers`, create+get thành công qua Postgres dev
  thật.
- **Bước 4 (quan trọng nhất) — bằng chứng "BFF thật"**:
  `crates/graphql-gateway/tests/gateway_e2e_postgres.rs` dựng **2 harness độc lập thật** (mỗi cái
  tenant riêng, keypair RSA riêng, `CrudService` Postgres thật riêng, gRPC listener thật riêng,
  REST listener thật riêng phục vụ `GET /metadata/entities`) — gọi đúng
  `graphql_gateway::schema_builder::build` (cùng đường boot sequence binary thật dùng), rồi chạy
  **1 GraphQL query duy nhất** lấy field từ **2 entity thuộc 2 service khác nhau**, assert cả 2
  phần dữ liệu có trong 1 response. Đây là bằng chứng thật, không phải suy luận từ kiến trúc.
- Cuối cùng: `cargo build/clippy -D warnings/fmt --check` sạch toàn workspace (kể cả crate mới
  `metap-graphql-gateway`), `cargo test --workspace` (unit) sạch, mọi e2e test liên quan pass qua
  Postgres dev thật.

## File chính đã thêm/sửa

- Mới: `crates/metap-crud/src/record_backend.rs` (`RecordBackend` + `impl` cho `CrudService`)
- Mới: `crates/metap-graphql/src/composite_backend.rs` (`CompositeBackend`)
- Mới: `crates/metap-grpc/src/client.rs` (`GrpcBackend`), test
  `crates/metap-grpc/tests/grpc_backend_client_postgres.rs`
- Mới: crate `crates/graphql-gateway` (package `metap-graphql-gateway`, binary `graphql-gateway`)
  — `src/{lib,main,config,schema_builder,server}.rs`, `tests/gateway_e2e_postgres.rs`,
  `.env.example`, `README.md`
- Sửa: `crates/metap-crud/src/{lib.rs,dto.rs}` (`RecordDto`/`RecordCapabilities`/
  `TransitionAvailability` thêm `Deserialize` — cần cho `GrpcBackend` dựng lại từ JSON trên dây)
- Sửa: `crates/metap-graphql/src/{schema.rs,loader.rs,lib.rs}` (nhận `Arc<dyn RecordBackend>`
  thay `Arc<CrudService>`)
- Sửa: `crates/metap-graphql-http/src/lib.rs` (generic hoá `playground_router<S>`, `router()`
  coerce `state.crud` sang `Arc<dyn RecordBackend>`)
- Sửa: `crates/metap-http/src/security_headers.rs` (CSP `insert` → `entry().or_insert_with()`)
- Sửa: `apps/crm-server/src/main.rs`, `apps/crm-server/.env.example` (thêm gRPC, mirror
  `jira-server`)
- Sửa: `Cargo.toml` (workspace members + `crates/graphql-gateway`)

## Còn lại, cố ý chưa làm

- Identity người gọi gateway propagate xuống downstream (cần JWKS trust root chung mọi upstream
  cùng verify — `metap-jwks` có sẵn làm thư viện, chưa có hạ tầng lắp sẵn cho ca này).
- Batch RPC thật cho `get_many` (`RecordService` proto chưa có `GetMany`, hiện N lời gọi `get`
  tuần tự — đáng làm nếu trở thành nghẽn cổ chai thật).
- Rate-limit ở tầng gateway (chỉ có `security_headers`+CORS cơ bản, không có `tower_governor` như
  `metap_http::build_router`).
- mTLS cấp phát/rotate cho gRPC M2M — hạ tầng triển khai của WAAP, không phải việc của
  `metap-grpc`/gateway (chỉ nhận `ServerTlsConfig` optional, đã ghi từ Phase 49).
- Binary/repo WAAP thật (portal gateway thật, entity WAAP thật) — việc sau, ở repo riêng.
