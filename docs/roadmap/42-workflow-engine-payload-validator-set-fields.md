## Phase 42: Workflow engine — transition payload, `validator`, `set_fields` (2026-08-26)

Chủ dự án: "phần statemachine + workflow phải ngon, hiện tại chưa có gì mấy". Trước phase này,
`WorkflowTransition` chỉ có `guard` (`PolicyCondition` chạy trên `existing.data`, *trước* khi
transition) — không có cách nào để (1) transition tự mang theo dữ liệu (`POST
/transitions/{action}` chỉ nhận `{ version }`, không có `data`), (2) validate riêng dữ liệu
*sau khi* payload được merge vào (khác `guard`, vốn chỉ thấy được dữ liệu cũ), hoặc (3) tự động
gán field hệ thống (ai đóng issue, lúc nào) mà không cần FE tự gửi lên. Mô hình lấy cảm hứng từ
Jira thật: **condition / validator / post-function** — 3 khái niệm tách biệt, không gộp thành 1
guard làm tất cả.

### Thiết kế — vẫn giữ nguyên ranh giới "không entity nào biết business logic"

Cả 3 phần mới đều tái dùng đúng cơ chế `PolicyCondition`/`PolicyValue` đã có ở `metap-permission`
(dùng cho ABAC/guard từ trước) — **không phải code tuỳ ý**. Đây là quyết định cố ý: nếu cho phép
`set_fields`/`validator` chạy hàm Rust tuỳ biến, `metap-workflow` (hay `metap-metadata`) sẽ phải
biết business logic của từng entity, vi phạm nguyên tắc "No `metap-*` library crate gets
business-entity knowledge" (CLAUDE.md). Giữ khai báo (declarative) nghĩa là mọi entity — kể cả
entity tạo qua low-code, không có code Rust riêng — đều dùng được.

`crates/metap-metadata/src/entity.rs`'s `WorkflowTransition` thêm 2 field mới:

```rust
pub struct WorkflowTransition {
    pub action: String,
    pub from: String,
    pub to: String,
    pub label: String,
    pub guard: Option<PolicyCondition>,      // đã có — chạy trên existing.data, trước transition
    pub validator: Option<PolicyCondition>,  // mới — chạy trên next_data (đã merge payload), sau validate field
    pub set_fields: Option<HashMap<String, PolicyValue>>, // mới — post-function, chạy sau validator
}
```

- **`guard`** — không đổi ngữ nghĩa. Vẫn là điều kiện "có được phép bấm nút này không", nhìn dữ
  liệu *hiện tại* của record (dùng để tô xám nút trên UI qua `compute_capabilities`, và chặn ở
  `transition()`).
- **`validator`** (mới) — điều kiện "payload gửi lên có hợp lệ để hoàn tất transition này không",
  nhìn dữ liệu *sau khi* merge payload + validate field. Việc `guard` không bao giờ làm được vì nó
  chỉ thấy dữ liệu cũ — ví dụ Jira thật: "phải điền `resolution` mới được bấm Close".
- **`set_fields`** (mới) — post-function thuần khai báo: `{ "closedBy": { "fromContext": "userId" } }`
  hoặc giá trị literal cố định. Chạy *sau* `validator` — giá trị hệ thống tự gán không bị chính
  validator dành cho payload người dùng chặn nhầm.

`metap_permission::resolve_value` (trước đây `fn` riêng trong `policy_condition.rs`, giờ `pub`)
được dùng lại nguyên xi cho `set_fields` — cùng 1 cơ chế `Literal`/`FromContext` guard/validator
đã dùng, không phát minh thêm loại giá trị thứ 2.

### `metap-metadata/src/compiler.rs` — cross-check `set_fields`

Cùng tinh thần AUDIT_2.md's field-name validation (Phase 41): `set_fields` bị validate ở
`compile()`, không phải chờ runtime mới lỗi —
- reject nếu field name không tồn tại trên entity,
- reject nếu field name trùng chính `state_field` (state chỉ được set qua `to`, không cho
  `set_fields` ghi đè ngầm).

### `metap-workflow/src/lib.rs` — 2 hàm mới cạnh `run_guard`

```rust
pub fn run_validator(transition: &WorkflowTransition, next_data: &JsonObject, context: &RequestContext) -> Result<(), String>
pub fn apply_set_fields(transition: &WorkflowTransition, data: &mut JsonObject, context: &RequestContext)
```

Cùng khuôn `run_guard` — `validator` optional (không khai báo thì luôn pass), lỗi trả về
`String` reason. `apply_set_fields` là no-op nếu transition không khai báo `set_fields`.

### `CrudService::transition()` — wiring thật

Chữ ký thêm 1 tham số: `payload: Option<&JsonObject>` (trước `context`). Luồng mới, chèn giữa
`run_guard` (không đổi) và bước ghi DB:

1. Nếu `payload` có, chạy đúng check `assert_writable_fields` (permission field-level) như
   `update()` đã làm — không cho transition lách qua field-permission bằng cách gửi field cấm ghi
   qua payload thay vì qua `PATCH`.
2. Merge payload vào bản sao `existing.data`, ép `state_field = to` (payload không bao giờ tự đổi
   state field, kể cả cố tình gửi lên).
3. `validate_payload` (field-metadata-driven, giống `update()`) — lỗi trả `400 validation_failed`
   với field errors, không phải lỗi chung chung.
4. `run_validator` — lỗi trả `422 validator_failed` (mã lỗi mới, tách khỏi `422 guard_failed` đã
   có, để FE phân biệt được 2 loại thất bại).
5. `apply_set_fields` — ghi đè field hệ thống vào `next_data` trước khi UPDATE.

Toàn bộ vẫn 1 transaction, không đổi cấu trúc audit (`record_event`)/outbox
(`emit_transitioned`) sau đó.

`metap-http/src/routes/records.rs`'s `TransitionBody` thêm field `data: Option<HashMap<String,
Value>>` (`#[serde(default)]` — request cũ `{ "version": N }` không có `data` vẫn hoạt động y hệt
trước, xác nhận bằng test HTTP e2e cũ `full_http_lifecycle_over_a_real_server_and_a_real_jwt`
pass nguyên không sửa).

### Verify sống

E2e mới `crates/metap-crud/tests/crud_service_postgres.rs`'s
`transition_payload_is_validated_and_set_fields_are_applied` — thêm 1 transition thứ 2 vào
`test_entity()` (`"close"`: `approved` → `closed`, `validator` yêu cầu `resolution != null`,
`set_fields` gán `closedBy` từ `context.userId`):
- gọi `close` không kèm `resolution` trong payload → `422 validator_failed`,
- gọi lại kèm `{ "resolution": "fixed" }` → thành công, record có cả `resolution` (từ payload) và
  `closedBy` (tự động, đúng bằng `userId` của caller, không phải giá trị FE gửi lên).

`cargo build/fmt --check/clippy --workspace --all-targets -D warnings` sạch. `cargo test
--workspace` (unit) sạch. E2e liên quan chạy lại toàn bộ qua Postgres/RabbitMQ thật:
`crud_service_postgres.rs` (12/14 pass — 2 fail còn lại là test cần seed data lớn có sẵn từ
trước, không liên quan phase này), `workflow_engine_postgres.rs` (2/2 pass),
`http_server.rs::full_http_lifecycle_over_a_real_server_and_a_real_jwt` (pass, xác nhận
backward-compat request shape cũ).

**Còn lại chưa làm** (chưa có trigger cụ thể): `set_fields` mới hỗ trợ `Literal`/`FromContext` —
chưa có "tính toán từ field khác" (vd `closedAt = now()`) vì `PolicyValue` chưa có variant thời
gian; JQL/list-view chưa biết hiển thị riêng field do `set_fields` ghi (không cần — nó vẫn là field
thường trong `data`, list-view/filter hiện tại đã tự hoạt động). Diff chưa commit.
