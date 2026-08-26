## Phase 43: Entity field validation — `min`/`max` (số) và `minLength`/`maxLength` (chuỗi) (2026-08-27)

Gap đã ghi nhận từ lâu trong doc comment của `crates/metap-crud/src/validation.rs`: validator hiện
chỉ check JSON *type* theo `FieldKind` (string/number/boolean/enum-membership), không có khái
niệm giới hạn số hay độ dài chuỗi — `EntityField` metadata trước đây không có field nào cho việc
đó. Chủ dự án chỉ định rõ: thiếu "validation kiểu limit num, string".

### Thiết kế

`crates/metap-metadata/src/entity.rs`'s `EntityField` thêm 4 field mới, tất cả optional (không
phá backward-compat với entity đã khai báo — `#[serde(default)]`):

```rust
pub min: Option<f64>,          // Number/Money — biên dưới (inclusive)
pub max: Option<f64>,          // Number/Money — biên trên (inclusive)
pub min_length: Option<u32>,   // String — độ dài ký tự tối thiểu (inclusive)
pub max_length: Option<u32>,   // String — độ dài ký tự tối đa (inclusive)
```

Không thêm `pattern` (regex) hay bất kỳ ràng buộc format nào khác (email, UUID shape) — ngoài
phạm vi yêu cầu, và mỗi ràng buộc mới cần cân nhắc rủi ro riêng (ReDoS cho regex, ví dụ) nên để
lại cho lúc thật sự cần.

### `metap-metadata/src/compiler.rs` — cross-check ở boot time

Cùng tinh thần các cross-check khác (enum không có `enumValues`, reference không có `refEntity`):
- `min`/`max` khai báo trên field không phải `Number`/`Money` → lỗi.
- `minLength`/`maxLength` khai báo trên field không phải `String` → lỗi.
- `min > max` hoặc `minLength > maxLength` → lỗi (cấu hình vô nghĩa, không field nào pass được).

Lỗi ở boot time, không phải chờ runtime mới phát hiện — cùng nguyên tắc `AUDIT_2.md` đã áp dụng
cho field-name/state-enum cross-check.

### `metap-crud/src/validation.rs` — check thật khi ghi record

Chèn ngay sau type-check hiện có, trước bước canonicalize `Reference`: nếu field là
`Number`/`Money` và có `min`/`max`, so `value.as_f64()` với biên; nếu field là `String` và có
`min_length`/`max_length`, so `value.as_str().chars().count()` (đếm ký tự, không phải byte — đúng
với chuỗi UTF-8 tiếng Việt) với biên. Lỗi trả về qua `FieldErrors` giống các lỗi khác (`"min"`,
`"max"`, `"min_length"`, `"max_length"`), không phải lỗi chung chung — FE phân biệt được lý do
chính xác.

### `metap-metadata/src/openapi.rs` — đồng bộ vào `/metadata/openapi.json`

Cả 2 chỗ generator hand-viết đều cập nhật, để không lệch với `entity.rs`'s struct thật (đúng ghi
chú "must stay in sync" trong CLAUDE.md):
- `entity_field_json_schema()` (mô tả shape của chính `EntityField`) thêm `min`/`max`/
  `minLength`/`maxLength`.
- `field_schema()` (mô tả shape một record's `data` field, dùng cho request/response body trong
  OpenAPI doc) giờ gắn `minimum`/`maximum`/`minLength`/`maxLength` JSON Schema keyword thật khi
  field có khai báo — không chỉ liệt kê type nữa.

`packages/platform-react`'s `generated-types.ts` cần chạy lại `pnpm --filter @metap/platform-react
generate:types` (yêu cầu backend đang chạy) để nhận field mới — **chưa chạy trong phiên này**, để
người dùng tự làm khi cần dùng field mới ở FE (đúng theo quy ước trong CLAUDE.md, không tự browser-
verify FE).

### Cập nhật 100+ literal `EntityField { ... }` trong toàn repo

Vì `EntityField` không dùng `Default`/spread trong hầu hết chỗ khai báo (convention hiện tại: liệt
kê tường minh mọi field), thêm 4 field mới nghĩa là cập nhật mọi struct-literal đang tồn tại — 30
file, ~104 literal (entity definitions ở `apps/*/src/entities/*.rs`, test helper `field()` ở nhiều
crate, benchmark fixtures). Làm bằng script (brace-matching, chèn `min: None, max: None,
min_length: None, max_length: None,` trước dấu `}` đóng mỗi literal), sau đó `cargo fmt` để chuẩn
hoá indentation.

**Bug phát sinh giữa chừng, đã sửa**: lần chạy script đầu tiên match nhầm cả `-> EntityField {`
(chữ ký hàm trả về `EntityField`, dấu `{` đó mở *thân hàm*, không phải literal) — brace-matching
từ vị trí sai khiến chèn nhầm vào giữa thân hàm, vỡ cú pháp ở ~20 file. Do working tree sạch trước
khi bắt đầu, revert bằng `git checkout --` toàn bộ rồi sửa regex (loại trừ cả tiền tố `struct ` lẫn
`-> `) và chạy lại — không mất gì.

**Bug thứ 2, phát hiện thủ công (không nằm trong cargo workspace nên `cargo build` không bắt
được)**: `templates/metap-app/src/example_entity.rs` dùng cú pháp spread `..field(...)` cho 2
trong 3 `EntityField` literal — script chèn field mới *sau* `..spread`, vi phạm luật Rust "spread
phải là item cuối cùng trong struct literal". Sửa tay: bỏ phần chèn thừa ở 2 chỗ đó (spread đã kế
thừa `min`/`max`/`min_length`/`max_length` từ `field()` helper). Nhân tiện phát hiện và sửa luôn 1
gap có sẵn từ trước (không liên quan phase này): `WorkflowTransition` literal trong cùng file
thiếu `validator`/`set_fields` (thêm ở Phase 42) — template này không nằm trong cargo workspace
nên không có CI nào bắt lỗi biên dịch của nó.

### Verify sống

- `crates/metap-crud/src/validation.rs`: 6 test mới (`number_below_min_is_rejected`,
  `number_above_max_is_rejected`, `number_within_min_max_passes`,
  `string_shorter_than_min_length_is_rejected`, `string_longer_than_max_length_is_rejected`,
  `string_within_length_bounds_passes`).
- `crates/metap-metadata/src/compiler.rs`: 5 test mới (`min_max_on_a_non_numeric_field_is_rejected`,
  `min_greater_than_max_is_rejected`, `min_max_on_a_numeric_field_passes`,
  `min_length_max_length_on_a_non_string_field_is_rejected`,
  `min_length_greater_than_max_length_is_rejected`).
- `cargo build/fmt --check/clippy --workspace --all-targets -D warnings` sạch, `cargo test
  --workspace` (unit, không cần DB) sạch toàn bộ.

**Còn lại chưa làm**: không có `pattern` (regex format) hay ràng buộc string-format khác (email,
UUID) — vẫn đúng giới hạn nêu trong doc comment gốc, chỉ thu hẹp phạm vi "không có limit num/
string" mà chủ dự án chỉ ra, chưa mở rộng thêm. FE `generated-types.ts` chưa regenerate — cần chạy
`pnpm dev:rs` + `pnpm --filter @metap/platform-react generate:types` rồi commit lại khi có nhu cầu
dùng field mới ở form/field-renderer. Diff chưa commit.
