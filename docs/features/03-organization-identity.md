# Organization & Identity Layer (org structure, role scope)

- **Trạng thái:** proposed
- **Người đề xuất:** chủ dự án, 2026-08-22
- **Track sở hữu:** Backend Core (phần entity mẫu liên quan track App/Entity)
- **Phase roadmap liên quan:** chưa gắn — nếu duyệt, đề xuất là Phase 18

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

Hướng 1 nhất quán nhất với triết lý hiện tại (role tra mới, không cache) nhưng cần đo chi phí
perf trước khi chốt — **không quyết ở đây**, để lại cho lúc brief này được duyệt.

## Phạm vi (nếu duyệt)

**P0 — làm được ngay, không cần trigger, không đụng code core:**
- Entity mẫu `hr.departments`/`hr.teams`/`hr.employees` (field, list view) — chứng minh
  Organization structure là business entity bình thường, verify qua HTTP thật.
- Ví dụ policy "role scoped by department" bằng `PolicyCondition` + `fromContext` có sẵn — chỉ
  chạy được sau khi P0 tiếp theo (enrich context) xong.

**P0 — cần thiết kế + code:**
- Chọn 1 trong 3 hướng enrich `RequestContext` ở trên, implement, verify sống qua HTTP.

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

- Nếu chọn hướng 1 (convention-based fetch): `AuthContext` (`crates/metap-http/src/auth.rs`)
  thêm một lệnh gọi `CrudService` mới vào đường auth của mọi request — cần ADR
  (`docs/architectures/09-adr.md`) vì đây là thay đổi hiệu năng + hành vi ở request path chung,
  không phải một call site đơn lẻ.
- Không đụng `metap-metadata`/`metap-crud` nếu Organization ở dạng entity thường — đúng ranh giới
  "core không biết business entity" đã giữ từ đầu.

## Rủi ro / phụ thuộc

- Chi phí perf của việc enrich context mỗi request (hướng 1) chưa đo — cần benchmark trước khi
  chốt, không suy đoán.
- Chưa có UI quản lý Organization/Employee — FE track khác lo (theo phân công hiện tại: backend
  ưu tiên, FE có người khác làm).
- Phụ thuộc gián tiếp `docs/features/02-workflow-engine.md` Increment 2 nếu muốn approval routing
  dùng org data — chưa phải trigger để bắt đầu brief này ngay.
