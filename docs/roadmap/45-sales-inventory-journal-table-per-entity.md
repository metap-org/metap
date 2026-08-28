## Phase 45: `sales.orders`/`inventory.movements`/`accounting.journal` — table-per-entity thật cho 3 entity còn lại của crm-server (2026-08-28)

Tiếp nối Phase 36 (`crm.customers`) — đúng như phase đó ghi nhận, "mỗi entity chuyển cần lặp lại
đúng quy trình này, chứ không có cách làm hàng loạt an toàn hơn". Cả 4 entity code-authored của
`crm-server` giờ đều dùng bảng riêng.

**Chuỗi phụ thuộc FK quyết định thứ tự**: `sales.orders.customer` → `crm.customers`,
`inventory.movements.referenceOrder` → `sales.orders`, `accounting.journal.referenceMovement` →
`inventory.movements` — đúng thứ tự Phase 7 giới thiệu 4 module. `metap_reconciler::compile()`
luôn build FK constraint nhắm vào `qualified_table_name_for(ref_entity)` bất kể entity đích đã
thật sự được reconcile hay chưa (cùng ràng buộc `apps/jira-server`'s `project_entity.rs` đã ghi) —
nên `main.rs` phải gọi `reconcile()` theo đúng thứ tự phụ thuộc (`sales.orders` sau
`crm.customers`, `inventory.movements` sau `sales.orders`, `accounting.journal` sau
`inventory.movements`), nếu không FK sẽ trỏ vào một bảng chưa tồn tại và `CREATE TABLE`/
`ADD CONSTRAINT` fail ngay khi boot.

- `table_name` của cả 3 entity đổi từ `"records"` sang `metap_reconciler::qualified_table_name_for(...)`.
- `main.rs` thêm 3 lệnh gọi `reconcile()` nối tiếp sau `customer_reconcile` sẵn có, đúng thứ tự
  phụ thuộc ở trên — cùng cơ chế Phase 36 đã dùng (tenant `Schema`, pool nền tảng dùng chung, không
  phải `DedicatedDb`).
- Chỉ `Reference` field (`customer`/`referenceOrder`/`referenceMovement`) có cột vật lý thật
  (`field_has_real_column` — không entity nào trong 3 entity này khai `storage: Column` cho field
  khác); mọi field còn lại vẫn ở JSONB `data`.
- **Không có migration dữ liệu 1 lần nào cần chạy** — khác Phase 36 (1671 record thật): DB dev
  hiện tại có 0 row cho cả 3 entity (`sales.orders`/`inventory.movements`/`accounting.journal`)
  lẫn `crm.customers`. Nếu chạy phase này trên một DB có data thật, cần lặp lại đúng quy trình
  Phase 36 (`INSERT ... SELECT` từ `records`, test bằng `BEGIN; ...; ROLLBACK;` trước, verify sống
  qua HTTP, rồi mới xoá row cũ khỏi `records`) cho từng entity theo đúng thứ tự trên.

**Kiểm chứng sống đầy đủ qua HTTP thật trên `crm-server`** (không chỉ đọc code suy luận):
- Chuỗi FK xuyên 4 bảng riêng: tạo `crm.customers` → `sales.orders` (trỏ customer) →
  `inventory.movements` (trỏ order) → `accounting.journal` (trỏ movement) — cả 4 tạo thành công,
  FK constraint hoạt động đúng.
- `list` mỗi entity: `relatedDisplay` hydrate đúng tên/mã bản ghi liên quan xuyên bảng riêng
  (`sales.orders.customer` → tên customer, `inventory.movements.referenceOrder` → code order,
  `accounting.journal.referenceMovement` → code movement).
- Delete-guard (`409 record_referenced`) đúng ở cả 2 điểm trong chuỗi: xoá customer khi order còn
  tham chiếu, xoá order khi movement còn tham chiếu — cả hai đều đúng lỗi, không phải 500/lỗi FK
  thô từ Postgres.
- Workflow transition qua HTTP cho cả 3 entity (`confirm`/`submit`→`approve`→`post`/`post`), guard
  (`Neq`/`Any`) vẫn chạy đúng trên bảng riêng.
- Dữ liệu test xoá sạch sau verify (leaf-first: journal → movement → order → customer).
- Chạy lại 2 script smoke có sẵn của repo (không phải test mới viết) trên server thật:
  `apps/crm-server/scripts/smoke.sh` — **pass toàn bộ**, phủ cả 4 entity (create/transition/guard/
  filter-by-reference-field, gồm cả nhánh `reject`/`reverse` của `inventory.movements` và guard
  `Any` của `accounting.journal`).
- Reconcile idempotent: boot lại lần 2 (không đổi entity definition) → `ops_applied=0` cho cả 4
  entity, khớp bảo đảm "reconcile hội tụ về 0 việc ở lần chạy 2" đã verify từ Phase 44/19.

**Phát hiện phụ, không phải bug của phase này**: chạy luôn
`apps/crm-server/scripts/permission-smoke.sh` để đối chiếu regression — 4/6 section fail
(context-level role policy không nới quyền sau khi grant role; field-level read/write policy
không gỡ mask/chặn sau khi xoá policy; record-level policy filter trả `null` thay vì mảng lọc
đúng). **Xác nhận đây là bug có sẵn từ trước, không phải regression của phase này**: `git stash`
về code chưa đổi (table_name vẫn `"records"` cho cả 3 entity), build lại, chạy lại
`permission-smoke.sh` — **fail y hệt**, cả về nội dung lỗi lẫn traceId pattern. Không sửa trong
phase này (ngoài phạm vi "table-per-entity", cần điều tra riêng ở tầng `metap-permission`/
`metap-http`'s policy re-evaluation) — ghi nhận lại đây, cần một phase riêng.

`cargo build/fmt --check/clippy -p crm-server --all-targets -D warnings` sạch; `cargo build
--workspace` (toàn bộ, sau khi dọn `target/` bị đầy đĩa giữa phiên — `rm -rf target`, dựng lại
xuống 6.5G) sạch. Không chạy `cargo test --workspace` lần cuối do giới hạn thời gian sau sự cố đầy
đĩa — build sạch + verify sống qua HTTP thật (smoke.sh + kiểm tra FK/hydrate/guard/transition thủ
công) là bằng chứng chính cho phase này, cùng tinh thần Phase 36.

**Còn lại**: mọi entity low-code (`hr.departments`/`hr.employees`/`helpdesk.tickets`/...) vẫn dùng
`records` chung — cố ý chưa làm, ngoài phạm vi phase này (những entity đó không code-authored, quy
trình chuyển cần đi qua `reconciler-orchestrator`, không phải một lệnh `reconcile()` tại boot).
