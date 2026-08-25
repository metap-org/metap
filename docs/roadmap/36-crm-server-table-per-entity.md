## Phase 36: `crm.customers` — table-per-entity thật cho crm-server (2026-08-25)

3/3 hạng mục "làm tất luôn" — mục lớn nhất và rủi ro nhất. Table-per-entity đã code-complete từ
Phase 19 nhưng chưa từng wire vào `crm-server` (`apps/jira-server`, Phase 21, là binary duy nhất
dùng thật, và làm từ đầu — không có data cũ). `crm-server` thì có: **1671 record `crm.customers`
thật** đã tồn tại trong bảng `records` chung trước khi đổi — khác hẳn tình huống jira-server, nên
dừng lại hỏi ý chủ dự án trước khi động vào data thật (không tự quyết một chiều dù "làm tất luôn"
đã được duyệt cho phạm vi công việc, không phải cho rủi ro mất data). Chốt: **di chuyển thật**
(không bỏ qua data cũ).

**Phạm vi cố ý hẹp**: chỉ `crm.customers` (1/4 entity code-authored của crm-server), không làm cả
`sales.orders`/`inventory.movements`/`accounting.journal` cùng lúc — đúng tinh thần incremental
`jira-server` đã làm (bắt đầu 2 entity, không phải tất cả cùng lúc).

- `customer_entity.rs`'s `table_name` đổi từ `"records"` sang
  `metap_reconciler::qualified_table_name_for("crm.customers")`.
- `main.rs` thêm 1 lần gọi `metap_reconciler::reconcile(&pool, tenant_id, &entity, &[])` ở boot,
  cùng cơ chế `jira-server` dùng — khác biệt quan trọng: `crm-server` dùng tenant `Schema`
  (pool nền tảng **dùng chung** giữa mọi schema-tenant), không phải `DedicatedDb` riêng — bảng
  `entities.crm_customers` mới cũng dùng chung y hệt `records` đã dùng chung, `tenant_id` truyền
  vào `reconcile()` chỉ là bookkeeping cho advisory-lock/introspection, không phải phạm vi bảng.
  Thêm dependency `metap-reconciler`/`uuid` vào `apps/crm-server/Cargo.toml` (trước đây không cần
  trực tiếp).
- **Migration dữ liệu 1 lần** (không phải cơ chế `metap-reconciler` nào — reconciler chỉ quản lý
  DDL/promote cột, không di chuyển hàng giữa 2 bảng vật lý): `INSERT INTO entities.crm_customers
  (...) SELECT (...) FROM records WHERE entity = 'crm.customers'`. **Test trước bằng
  `BEGIN; ...; ROLLBACK;`** để xác nhận không vỡ FK tự tham chiếu (`referredBy`) trước khi chạy
  thật — xác nhận Postgres check FK ở cuối statement cho 1 câu `INSERT...SELECT`, không phải
  từng dòng, nên thứ tự dòng không quan trọng. Chạy thật: **1672/1672 khớp tuyệt đối** (1671 +
  1 record tenant khác). Trigger đồng bộ cột thật `"referredBy"` (`metap-reconciler` tự sinh lúc
  `compile()`) tự chạy đúng cho toàn bộ 1672 dòng — verify bằng spot-check `data->>'referredBy'`
  so với cột thật, khớp 100%. Sau khi verify sống qua HTTP thành công (xem dưới), xoá 1672 dòng
  cũ khỏi `records` (không phải migration file trong `crates/migrations/` — đây là data fixup
  1 lần cho DB dev hiện có, không phải DDL mọi DB mới cần, cùng tiền lệ dọn 608-entity ở Phase 19
  và fix migration 0019 ở Phase 27).

**Kiểm chứng sống đầy đủ qua HTTP thật trên `crm-server`** (không chỉ đọc code suy luận):
- `list`/filter theo `status` — dữ liệu cũ (trước migration) hiển thị đúng.
- `get` 1 record đã soft-delete từ trước migration → đúng 404 (không phải bug — `deleted=true`
  giữ nguyên qua migration, tự bắt nhầm lúc đầu rồi tự sửa bằng cách chọn lại record chưa xoá).
- `get` 1 record chưa xoá từ trước migration → 200, đúng dữ liệu.
- `create` parent + child (child có `referredBy` trỏ parent, tự tham chiếu) → `relatedDisplay`
  hydrate đúng tên parent.
- Xoá parent khi child còn tham chiếu → `409 record_referenced` (delete-guard hoạt động đúng trên
  bảng riêng).
- `transitions/activate` trên child → chạy đúng, `status` đổi `draft`→`active`.
- Dữ liệu test (`TPE-PARENT`/`TPE-CHILD`) xoá sạch sau verify.

**Phát hiện phụ, không phải bug của phase này**: `?referredBy=` filter không lọc gì — field này
chưa từng có trong `list_views.filters` của `crm.customers` (giới hạn có từ trước, độc lập với
việc chuyển bảng, không sửa trong phase này).

`cargo build/fmt --check/clippy --workspace --all-targets -D warnings` + `cargo test --workspace`
(72 test suite) sạch — không regression cho 3 entity `crm-server` còn lại (vẫn dùng `records`
chung, không đổi).

**Còn lại**: 3 entity code-authored còn lại (`sales.orders`/`inventory.movements`/
`accounting.journal`) và mọi entity low-code (`hr.departments`/`hr.employees`/`helpdesk.tickets`/
...) vẫn dùng `records` chung — cố ý chưa làm, mỗi entity chuyển cần lặp lại đúng quy trình này
(test FK bằng rollback trước, verify sống, dọn `records` sau) chứ không có cách làm hàng loạt an
toàn hơn.

Diff chưa commit.
