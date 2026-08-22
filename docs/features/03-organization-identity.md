# Organization & Identity Layer (org structure, role scope)

- **Trạng thái:** P0 done (2026-08-22); P1/P2 vẫn proposed, chưa code
- **Người đề xuất:** chủ dự án, 2026-08-22
- **Track sở hữu:** Backend Core (phần entity mẫu liên quan track App/Entity)
- **Phase roadmap liên quan:** Phase 18 (`docs/roadmap.md`)

## Vấn đề / động lực

Metap xác định mỗi tenant là một business/company (`docs/multi-tenant-platform-design.md`), nhưng
mô hình identity hôm nay chỉ có hai tầng phẳng:

```
Tenant
 └── users (danh tính)
      └── user_roles (tenant_id, user_id, role) — một chuỗi role phẳng, không có scope
```

Không có gì diễn tả được "người này thuộc phòng ban nào", "giữ chức vụ gì", "report cho ai", hay
"role Sales Manager của người này chỉ áp dụng trong phạm vi phòng Sales, không phải toàn tenant".
Đây là gap thật — nếu Metap muốn là nền tảng low-code cho business nói chung (không chỉ CRM đơn
giản), sớm muộn một entity nào đó (approval workflow, báo cáo theo phòng ban, phân quyền theo chi
nhánh) sẽ cần tới organization structure.

## Rà soát hạ tầng đã có (trước khi thiết kế cái mới — không suy đoán)

- **`user_roles`** (`crates/migrations/0002_sticky_drax.sql`) — đúng như mô tả ở trên: phẳng,
  không có cột scope nào.
- **`RequestContext`** (`crates/metap-permission/src/context.rs`) — chỉ mang
  `tenant_id`/`user_id`/`roles`/`function_id`. `PolicyCondition::FromContext`
  (`policy_condition.rs`'s `resolve_value`) chỉ resolve được những field này qua
  `context.to_value()` — **không có chỗ nào để đặt "phòng ban của người gọi"** dù cơ chế
  `fromContext` bản thân đã tổng quát.
- **RBAC + ABAC đã tồn tại đầy đủ, không cần xây lại** — `PolicyRow` đã có cả role gate
  (`roles: Vec<String>`) lẫn attribute condition (`PolicyCondition`, hỗ trợ `fromContext`), đánh
  giá qua `evaluate_policies` (deny-overrides-allow, xong 2026-08-21). Một policy như
  `{roles: ["sales_manager"], condition: {attribute: "departmentId", op: "eq", value:
  {fromContext: "departmentId"}}}` **đã chạy được ngay hôm nay về mặt cơ chế** — chỉ thiếu đúng
  một thứ: `context.departmentId` không tồn tại. Đây là phát hiện quan trọng nhất của lần rà soát
  này: **"role có scope" không phải một subsystem Role/Permission/Scope mới cần xây (như đề xuất
  gốc), mà là một gap hẹp — RequestContext thiếu attribute của caller.**
- **Cross-record condition** (`docs/roadmap.md` Phase 3, xong 2026-08-21) — một policy record-level
  đã resolve được dotted attribute path 1-hop qua field `Reference` (vd `"referredBy.status"`,
  verify sống bằng `crm.customers.referredBy` tự tham chiếu). Manager hierarchy
  (`Employee.managerId`) là **đúng cùng một pattern**, đã chứng minh hoạt động — không cần code
  core mới.
- **Low-code entity builder** (`metap-lowcode`/`metap-lowcode-http`, Phase 11) — một admin đã định
  nghĩa được entity mới (field, list view, workflow) qua API, không cần deploy code. Department/
  Team/Position/Employee **định nghĩa được ngay hôm nay** qua đường này, không chờ trigger hay
  phase nào.

## Đề xuất kiến trúc — lệch một phần có chủ đích so với đề xuất gốc

Đề xuất gốc coi Organization (Department/Team/Position/Employee) là một layer core platform mới,
song song với Access Control. Sau khi rà code thật, đề xuất đổi hướng:

**Organization structure = business entity thường, không phải core platform table.**
Department/Team/Position/Employee/Location có field, có list view, có thể có workflow riêng (vd
approve onboarding) — đúng định nghĩa "business entity" mà `EntityDefinition` đã được thiết kế
cho. Biến chúng thành bảng/struct cứng trong `metap-metadata`/`metap-crud` sẽ vi phạm chính nguyên
tắc `CLAUDE.md` đang giữ: "Không có `metap-*` library crate nào được biết về business entity."
Thay vào đó: ship một bộ `EntityDefinition` mẫu (`hr.departments`, `hr.teams`, `hr.positions`,
`hr.employees`) như ví dụ/template — qua entity module code (như `customer_entity.rs`) hoặc qua
chính low-code builder — không hardcode vào core.

**Access Control (Role/Permission/Policy) hầu như đã đủ, không cần xây lại từ đầu.** Gap thật duy
nhất, hẹp: **RequestContext cần một cách mang caller attribute ngoài role** để `PolicyCondition`'s
`fromContext` có gì để đọc. Đây là phần duy nhất thực sự cần thiết kế mới.

**Manager hierarchy không cần gì mới** — `Employee.managerId` (field `Reference`, self-tham chiếu
tới `hr.employees`) dùng đúng cơ chế cross-record condition đã build. Ví dụ policy "chỉ quản lý
trực tiếp mới sửa được review của nhân viên" viết được ngay bằng
`{attribute: "employee.managerId", op: "eq", value: {fromContext: "userId"}}` mà không cần dòng
code core nào mới.

### Điểm khó thật, chưa có câu trả lời chắc chắn — cần quyết định trước khi code

Làm sao enrich `RequestContext` với attribute của caller (vd `departmentId`) mà không vi phạm
nguyên tắc "`metap-http` không được biết business entity"? Ba hướng đã nghĩ tới, **chưa hướng nào
được chọn**:

1. **Convention-based, entity-agnostic** — nếu tenant có một entity theo tên quy ước (vd
   `"employees"`) với field trỏ tới user hiện tại, `AuthContext` extractor tự gọi `CrudService`
   (generic, không cần biết field shape) fetch record đó rồi merge `data` (hoặc whitelist field)
   vào context. Giữ được tính entity-agnostic (chỉ cần biết *tên* entity theo convention, không
   biết field bên trong), nhưng thêm một query DB vào **mọi** request có auth — cần cache như
   `PermissionSnapshot` đã làm, hoặc lazy-fetch chỉ khi policy thật sự tham chiếu `fromContext`
   tới field lạ (giống cách cross-record condition #3 chỉ fetch khi policy cần).
2. **Khai báo trong JWT lúc mint token** — không tốn query, nhưng stale ngay khi người dùng đổi
   phòng ban mà chưa đăng nhập lại — đối lập với nguyên tắc hiện tại "role luôn tra mới từ DB mỗi
   request, không bao giờ cache trên JWT" (`docs/architectures/06-runtime.md`).
3. **Cột generic JSONB riêng** (vd `user_context_attributes`, sync bởi nghiệp vụ khi Employee
   record đổi) — thêm state phải giữ đồng bộ, rủi ro lệch dữ liệu.

**Đã chốt (2026-08-22, xem ADR `docs/architectures/09-adr.md`): Hướng 1, opt-in qua config, có
cache.** Một biến env `AUTH_CONTEXT_ENTITY` đặt tên đúng một entity quy ước có field `userId`
trỏ tới user hiện tại — `AuthContext` (`crates/metap-http/src/auth.rs`) tự tra record đó sau khi
role đã resolve, flatten kết quả vào `RequestContext.context_attributes`
(`crates/metap-permission/src/context.rs`, `#[serde(flatten)]` nên `fromContext` đọc được ngay
không cần sửa `PolicyCondition`). `None` (mặc định) là no-op tuyệt đối. Khác role (không bao giờ
cache) — kết quả **được cache** (`metap_http::cache::ContextAttributesCache`, cùng mẫu
`metap-control::RegistryCache`, TTL cấu hình qua `AUTH_CONTEXT_CACHE_TTL_SECONDS`, mặc định 30s)
vì đây là một round-trip DB thêm trên *mọi* request khi bật tính năng, không phải một security
check. Hai đường invalidate: đợi TTL, hoặc gọi tường minh
`POST /admin/users/{userId}/context/invalidate` (gate `AdminContext`) ngay sau khi sửa
`departmentId` của một user để có hiệu lực tức thì. Chi phí perf: một `SELECT ... LIMIT 1` có
index (`userId` nên đặt `indexed: true`) chỉ chạy lúc cache-miss, không phải mọi request — không
cần benchmark riêng, cache đã loại bỏ chính nỗi lo ban đầu (rủi ro perf mỗi request).

## Phạm vi

**P0 — done (2026-08-22), verify sống qua HTTP thật:**
- Enrich `RequestContext` (Hướng 1 + cache, mô tả ở trên) — implement, unit test
  (`context_attributes` flatten đúng), e2e test tự động
  (`crates/metap-http/tests/http_server.rs`'s
  `auth_context_entity_enriches_org_scoped_policies_and_supports_explicit_cache_invalidation`,
  bao phủ cả cache-stale-then-invalidate) và verify sống thủ công qua `curl` trên `crm-server`
  đang chạy thật.
- Entity mẫu `hr.departments`/`hr.employees` — tạo qua chính low-code builder
  (`PUT /admin/lowcode/entities/{name}/draft` + `publish`), không phải Rust module mới —
  chứng minh đúng thesis "Organization structure = business entity thường". `hr.employees` có
  field `userId` (`string`) + `departmentId` (`reference` → `hr.departments`).
- Ví dụ policy "role scoped by department" bằng `PolicyCondition` + `fromContext`: verify sống
  user A (department Engineering) đọc record `hr.employees` cùng phòng ban → 200, khác phòng ban
  → 403 (deny-by-default, không cấu hình gì thêm).
- Deployment không set `AUTH_CONTEXT_ENTITY` (toàn bộ e2e suite hiện có, chạy lại nguyên vẹn) —
  hành vi y hệt trước thay đổi này, xác nhận qua `cargo test --workspace -- --ignored`.

**P1:**
- `hr.positions`, `hr.locations` (field thêm trên entity mẫu, không cần core mới).
- `Employee.managerId` self-reference + ví dụ policy "chỉ manager trực tiếp mới sửa được" — verify
  bằng cross-record condition đã có.
- Docs pattern cho FE/entity author: cách viết một "org-scoped policy" đúng chuẩn.

**P2 — chưa cần, đúng như đề xuất gốc:**
- Legal Entity, Business Unit, Cost Center, Job Level, Employment Type, Org Chart visualize,
  Delegation, Temporary Role, Approval Authority — chưa entity/nhu cầu thật nào trong repo cần.
- Approval routing dùng Organization data trong Workflow ("approver: department_manager") — kết
  nối trực tiếp `docs/features/02-workflow-engine.md` Increment 2 (chuỗi activity), chỉ làm sau
  khi Increment 2 có target type đọc được org data.

**Ngoài phạm vi, khác đề xuất gốc:** một subsystem Role/Permission/Scope mới tách biệt khỏi
`metap-permission` hiện tại — RBAC+ABAC đã có đủ biểu đạt lực (role gate + condition), không cần
khái niệm Scope riêng nếu context được enrich đúng.

## Ranh giới kiến trúc bị đụng tới

- Hướng 1 (convention-based fetch, đã chọn): `AuthContext` (`crates/metap-http/src/auth.rs`)
  thêm một lookup (cache-trước) vào đường auth của mọi request khi opt-in bật — ghi nhận ở ADR
  (`docs/architectures/09-adr.md`) vì đây là thay đổi hiệu năng + hành vi ở request path chung,
  không phải một call site đơn lẻ.
- Không đụng `metap-metadata`/`metap-crud` — Organization vẫn ở dạng entity thường qua low-code,
  đúng ranh giới "core không biết business entity" đã giữ từ đầu.

## Quan hệ với table-per-entity (Data Model Strategy Step 3) — nghiên cứu 2026-08-22

Câu hỏi đặt ra: Organization data (Department/Team/Employee) có phải trigger tự nhiên cho việc
tách bảng `records` chung sang table-per-entity không? Rà kỹ hai thiết kế đã có
(`docs/multi-tenant-platform-design.md` §3, `docs/architectures/05-building-blocks.md`'s Data
Model Strategy) trước khi trả lời — **câu trả lời là không, ở hai trục đầu, nhưng có một trục thứ
ba lộ ra một gap thật, độc lập với table-per-entity:**

**1. Trigger theo volume (@10M/entity) — Organization data không chạm tới.** §3.1 của
`multi-tenant-platform-design.md` ghim rõ ràng: table-per-entity là bắt buộc ở mốc **10 triệu
row/entity** (lý do: N entity × 10M trong một bảng chung → index phình, autovacuum ác mộng).
Employee/Department của một tenant thực tế hiếm khi vượt vài trăm nghìn row, kể cả doanh nghiệp
rất lớn — thấp hơn ngưỡng trigger 1-2 bậc độ lớn. Organization & Identity không kéo table-per-
entity tới gần hơn.

**2. Trigger theo độ nhạy latency (lookup caller mỗi request) — đã được giải bởi cơ chế có sẵn,
không cần table-per-entity.** Thiết kế "enrich `RequestContext`" (hướng 1 ở trên) cần tra caller's
Employee record theo `userId` trên **mỗi** request có auth — nghe có vẻ cần một bảng riêng, tối ưu
cho lookup nóng. Nhưng cơ chế **đã tồn tại** giải đúng bài toán này ở tier T1 (JSONB + partial
expression index): `IndexReconciler` (`crates/metap-peripherals`) đã tự động build partial index
cho field `indexed: true` của một entity, đúng shape `WHERE entity = 'hr.employees' AND ...`
(`docs/architectures/05-building-blocks.md`'s "Hot field indexes"). Đặt `userId` field trên
`hr.employees` là `indexed: true` cho tra cứu O(log n) ngay trong bảng `records` chung, không cần
tier T2/T3 hay tách bảng gì cả. Latency-sensitivity không phải lý do hợp lệ để đẩy sớm
table-per-entity ở đây.

**3. Reference integrity — gap thật, đã xác nhận bằng code, tồn tại NGAY CẢ SAU khi
table-per-entity xong (cho bảng chung), và Organization data (Department bị xoá trong khi
Employee vẫn tham chiếu) làm nó lộ rõ hơn hẳn so với các entity khác trong repo hôm nay.**
`multi-tenant-platform-design.md` §3.3 giả định "Ref tới bảng chung → fallback check ở
`CrudService` lúc write" đã tồn tại cho thế giới bảng-chung hiện tại — **kiểm tra trực tiếp
`CrudService::delete()` (`crates/metap-crud/src/crud_service.rs`) xác nhận điều này SAI**: `delete`
chỉ là một `UPDATE records SET deleted = true ...` thuần tuý, không quét bất kỳ record nào khác
đang tham chiếu tới nó qua field `Reference`. Xoá một `hr.departments` record trong khi nhiều
`hr.employees` vẫn có `departmentId` trỏ tới nó **không báo lỗi, không chặn** — để lại tham chiếu
treo (orphan reference) âm thầm. `crm.customers.referredBy` (self-reference đã dùng để verify
cross-record condition #3) hiếm khi lộ bug này vì xoá một customer bị referral tới không phải
thao tác thường gặp; Department/Employee thì khác — xoá một phòng ban đang có nhân sự là thao tác
sẽ xảy ra thật. **Gap này độc lập với table-per-entity**: kể cả khi Step 3 triển khai xong, nó chỉ
cho FK thật với entity **đã tách bảng** — Organization data (dưới ngưỡng 10M) nhiều khả năng vẫn
ở bảng `records` chung vô thời hạn, nên "chờ table-per-entity" không phải câu trả lời. Cần một
câu trả lời riêng cho thế giới bảng-chung: `CrudService::delete()` (và có thể `update` khi
`departmentId` bị đổi) quét `Reference` field nào trỏ tới entity/id đang bị xoá (đọc metadata,
biết field nào là `Reference` + `refEntity` trỏ tới entity này) — chặn (`Restrict`, mặc định an
toàn) hoặc cho phép tuỳ chọn theo field (tương lai, không phải bây giờ).

**Kết luận**: Organization & Identity không phải trigger cho table-per-entity — nên tiếp tục coi
table-per-entity là trigger-based, chưa kích hoạt (đúng kỷ luật hiện tại). Nhưng nó lộ ra một gap
reference-integrity có thật, đang tồn tại, không phụ thuộc bất kỳ phase nào khác — nên được ghi
nhận độc lập (xem `docs/architectures/11-risks.md`, hàng mới thêm cùng ngày) và cân nhắc đóng
**trước** khi P1 (`Employee.managerId`/`departmentId`) đi vào sản xuất thật, nếu không một thao
tác xoá phòng ban bình thường sẽ để lại dữ liệu hỏng âm thầm.

Điểm phụ, đáng ghi vì liên quan trực tiếp thiết kế entity mẫu ở P0/P1: §3.3 của
`multi-tenant-platform-design.md` phân biệt `relationMode: referenced` (record riêng, query/
report được — mặc định) và `relationMode: owned` (nested JSONB, chỉ khi con không cần query độc
lập). `Employee → Department`/`Employee → Employee (manager)` rõ ràng cần `referenced` (org
chart, headcount theo phòng ban là truy vấn thật) — entity mẫu ở P0/P1 nên dùng field `Reference`
thường (đã là mặc định hôm nay, `owned` chưa được implement), không phải lựa chọn cần cân nhắc.

## Rủi ro / phụ thuộc

- Chưa có UI quản lý Organization/Employee — FE track khác lo (theo phân công hiện tại: backend
  ưu tiên, FE có người khác làm).
- Phụ thuộc gián tiếp `docs/features/02-workflow-engine.md` Increment 2 nếu muốn approval routing
  dùng org data — chưa phải trigger để bắt đầu brief này ngay.
