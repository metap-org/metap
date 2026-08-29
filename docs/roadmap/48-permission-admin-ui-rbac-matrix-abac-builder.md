## Phase 48: Redesign UI quản lý phân quyền — ma trận RBAC (cơ bản) + condition builder ABAC (nâng cao) (2026-08-29)

Trang `/admin/policies` (`platform-ui/src/admin/PoliciesAdminPage.tsx`) trước đây chỉ có 1 form
tạo-policy thô (entity/action/roles-dạng-text/field/subject + **textarea JSON thô** cho
`condition`) và 1 list phẳng chỉ xoá được. Không có type `PolicyCondition` nào ở FE (`condition:
unknown` xuyên suốt), danh sách action hardcode sai/cũ (thiếu `transition`, thừa `"write"`), và
`effect` (Allow/Deny) không hề hiển thị — policy Deny vô hình trên UI. Chủ dự án muốn tách theo
đúng cách backend đã model: RBAC (`policies.roles`) = "phân quyền cơ bản" → ma trận role×action;
ABAC (`policies.condition`) = "phân quyền nâng cao" → condition builder trực quan thay vì JSON
tay. Yêu cầu rõ: nghĩ/plan/thiết kế trước khi code (dùng `EnterPlanMode`, xem
`/home/minhtuan/.claude/plans/cuddly-bouncing-graham.md`).

**Bước 1 — nghiên cứu + xác nhận model backend đã đủ dùng, không cần đổi data model:**
`PolicyRow` (`crates/metap-permission/src/policy_store.rs`) đã có sẵn `roles: Option<Vec<String>>`
(RBAC) và `condition: Option<PolicyCondition>` (ABAC, cây đệ quy `Attribute{attribute,op,value} |
All{[...]} | Any{[...]}`) trên **cùng 1 row** — 1 policy có thể thuần RBAC, thuần ABAC, hoặc cả
hai (AND). Không cần migration/đổi schema.

**Bước 2 — type + hàm thuần cho `PolicyCondition` ở FE:** file mới
`platform-ui/src/admin/policyCondition.ts` mirror đúng shape Rust (untagged union), cộng
`isBasicShapedRow(policy)` — luật duy nhất phân định 1 policy là "khớp được vào ma trận" hay phải
rơi qua tab Advanced (không condition, không field, subject=context, effect=allow).

**Bước 3 — Ma trận RBAC (Basic tab):** `PermissionMatrix.tsx` — hàng = role (tự gom từ
`useAdminUsers()`+`useAdminPolicies()`+role gõ tay chưa lưu, không có bảng role catalog nào cả —
roles vốn free-text), cột = action (từ `GET /metadata/actions` mới, xem bước 5), ô = checkbox.
Hàng "Everyone" cố định cho policy `roles: null/[]`.

**Bước 4 — Condition builder ABAC (trong tab Advanced):** `ConditionBuilder`/`ConditionNodeEditor`/
`AttributePicker`/`ValueEditor` — cây AND/OR đệ quy tự dựng bằng indent + border trái (`@metap/ui`
không có Tree component). `AttributePicker` implement đúng bất đối xứng record-vs-context trong
`evaluate.rs` (`subject=record` → chọn field entity; `subject=context` → free-text vì
`AUTH_CONTEXT_ENTITY`'s dynamic attribute không enumerate được từ FE — phát hiện thêm:
`Autocomplete` chỉ commit khi chọn đúng 1 option có sẵn, gõ tự do bị bỏ qua âm thầm, nên dùng
`Input`+`Chip` gợi ý thay vì `Autocomplete`).

**Bước 5 — backend: `GET /metadata/actions` (quyết định cùng chủ dự án):** ban đầu định hardcode
5 action ở FE cho đơn giản, nhưng chủ dự án chọn hướng BE làm nguồn chân lý duy nhất —
`EntityAction::ALL` (`crates/metap-permission/src/context.rs`) + route mới, sửa luôn
`metap-http::routes::admin::KNOWN_ACTIONS` (mirror tay trùng lặp trước đó) để derive từ đây thay
vì tự khai báo lại.

**Bước 6 — gộp thao tác ma trận thành 1 API duy nhất:** phát hiện live khi demo — mỗi lần tích 1
ô đang gọi 1 POST/DELETE + 1 lần invalidate-refetch (2 round-trip/click, bulk theo hàng/cột thì đỡ
hơn nhưng vẫn N lệnh). Chủ dự án chọn hướng: sửa tự do trên UI, bấm Save mới gọi 1 API. Thêm
`PolicyStore::sync_basic_policies` (trait method mới, `crates/metap-permission`) — 1 transaction
Postgres duy nhất: xoá hết row basic-shaped hiện có của entity, insert lại đúng theo `grants` gửi
lên (luôn 1-role-1-row, nên **case "policy dùng chung nhiều role" tự nhiên biến mất luôn**, không
cần logic tách-row phức tạp nữa). Route mới `PUT /admin/policies/matrix`. FE: `PermissionMatrix`
sửa hoàn toàn trên `Set` cục bộ (`desired`/`baseline`), chỉ re-seed từ server 1 lần mỗi khi đổi
entity (không re-seed mỗi background refetch — tránh mất thay đổi chưa lưu nếu tab bị refetch nền
lúc đang sửa dở).

**Bước 7 — 3 bổ sung sau khi demo, hỏi rõ scope từng cái trước khi làm:**
- **Grant user ngay trong ma trận**: bấm tên role → xổ ra danh sách user đang có role đó (Badge +
  nút xoá) + ô nhập User ID + nút Assign — dùng thẳng `assignRole`/`revokeRole` có sẵn, không cần
  API mới.
- **Search permission xuyên entity**: `PermissionSearch.tsx`, ô tìm kiếm độc lập với entity đang
  chọn, query `useAdminPolicies()` không lọc entity (chỉ fetch khi có từ khoá), khớp theo
  role/entity/action/label.
- **Permission "name"**: chủ dự án gợi ý dạng `worklog_read`/`worklog_write` — xác nhận chỉ là
  label hiển thị/tìm kiếm (`permissionLabel(entity, action) = "{entity}_{action}"`), không đổi
  model lưu trữ. Dùng chung ở cả cột ma trận lẫn kết quả search (tách hàm vào
  `policyMatrixHelpers.ts` để 2 nơi không lệch nhau).

**Kiểm chứng**: `cargo build/test/fmt --check/clippy -D warnings` sạch cho
`metap-permission`/`metap-http`/`metap-control`/`metap-query`/`metap-metadata` (bao gồm mọi fake
`PolicyStore` trong test/bench phải cập nhật theo trait method mới). `pnpm typecheck`/`lint`/
`format:check` sạch trong `platform-ui`. `vite build` thật của cả `apps/crm-fe`/`apps/jira-fe`
(nơi UI này thực sự chạy qua `link:`) pass — không browser-test theo policy.

**File chính đã thêm/sửa** — `metap`: `crates/metap-permission/src/{context,policy_store,
permission_service}.rs`, `crates/metap-control/src/policy_store.rs`,
`crates/metap-http/src/routes/{admin,metadata}.rs`, `crates/metap-http/src/openapi_paths/admin.rs`,
`crates/metap-metadata/src/openapi.rs`, 3 file test/bench có fake `PolicyStore`. `platform-ui`:
`src/admin/policyCondition.ts` (mới), `src/admin/adminApi.ts`, `src/admin/PoliciesAdminPage.tsx`
(viết lại), `src/admin/policies/*.tsx` (7 file mới: `PermissionMatrix`, `AdvancedPoliciesPanel`,
`ConditionBuilder`, `ConditionNodeEditor`, `AttributePicker`, `ValueEditor`, `PermissionSearch`,
`policyMatrixHelpers.ts`), `src/shared/TagsField.tsx` (tách ra từ `LowCodeEntitiesAdminPage.tsx`
khi có caller thứ 2), `src/i18n/resources.ts`, `README.md`.

**Còn lại, cố ý chưa làm**: role vẫn free-text không có catalog (theo đúng thiết kế hiện tại của
hệ thống, không phải gap của phase này); chưa có UI audit "mọi Deny/condition rule trong tenant"
kiểu bảng tổng hợp (search hiện tại đã đủ dùng, cross-entity nhưng không có view liệt kê riêng);
chưa có endpoint enumerate "toàn bộ role đang tồn tại trong tenant" (FE tự gom từ users+policies).
