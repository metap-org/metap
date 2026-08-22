# Table-per-entity — readiness brief

- **Trạng thái:** proposed
- **Người đề xuất:** chủ dự án (yêu cầu lên plan cùng Organization & Identity), 2026-08-22
- **Track sở hữu:** Backend Core
- **Phase roadmap liên quan:** không thuộc phase nào — chưa có trigger

## Đây KHÔNG phải một thiết kế mới

Toàn bộ thiết kế đã có, đầy đủ, ở `docs/multi-tenant-platform-design.md`:
- §3 Data Plane — Storage & Model (table-per-entity, 3-tier storage, relations/FK thật)
- §4 Data Plane — Data Evolution (migration declarative-only, preflight/quarantine)
- §5 Reconciler — trái tim data-plane (level-triggered, pipeline, diff, executor)
- §6 Orchestrator (fan-out multi-tenant)
- §7 Testing data-plane

Brief này **chỉ** làm một việc: format hoá phần đã thiết kế thành một build order tường minh
theo phụ thuộc kỹ thuật, để lúc trigger thật xảy ra thì có sẵn thứ tự thi công thay vì phải đọc
lại toàn bộ §3-§7 rồi tự suy luận thứ tự lúc đó. Không viết code, không viết migration, không đổi
bất kỳ quyết định thiết kế nào đã ghim.

## Vấn đề / động lực

`docs/architectures/09-adr.md` đã ghim: "Bảng `records` JSONB dùng chung sẽ được thay bằng
table-per-entity khi có tín hiệu scale (@ ~10M row/entity), không phải ngay bây giờ." Đây là một
thiết kế trigger-based đã duyệt (§3.1) — vấn đề không phải "có nên làm" mà là "khi trigger xảy ra
thì bắt đầu từ đâu". §3-§7 mô tả từng mảnh (storage tier, migration, reconciler, orchestrator,
testing) khá đầy đủ nhưng **không mô tả thứ tự build** — một reader mới không biết mảnh nào phải
xong trước mảnh nào vì lý do phụ thuộc kỹ thuật thật (không phải sở thích). Nghiên cứu
"Organization & Identity × table-per-entity" (2026-08-22,
`docs/features/03-organization-identity.md`'s mục "Quan hệ với table-per-entity") xác nhận lại:
chưa có entity nào trong repo chạm ngưỡng 10M row — không có gì thay đổi về *thời điểm* bắt đầu,
brief này chỉ chuẩn bị sẵn *thứ tự* cho lúc đó.

## Phạm vi

**Trong phạm vi — build order đề xuất (5 bước, theo phụ thuộc kỹ thuật, không phải song song
tuỳ ý):**

1. **`FieldStorage`/tier suy từ cờ metadata** (§3.2) — `indexed`/`sortable`/`unique`/`searchable`
   trên `EntityField` tự động promote field lên generated column (T2) hay giữ JSONB (T1);
   `FieldStorage { Column, Native }` là override tường minh khi cần. Làm trước vì mọi bước sau
   (reconciler diff, migration transform) đều cần biết "field này nên nằm ở đâu" làm input —
   không có bước này thì reconciler không có gì để diff.
2. **Reconciler level-triggered cho MỘT entity đơn lẻ** (§5.1-§5.2, §5.4 thuật toán diff, §5.6
   executor) — `reconcile = diff(desired, actual) → plan → execute`, tự lành sau crash, không
   rollback (DDL online không rollback được). Chạy cho một entity, chưa multi-tenant fan-out —
   chứng minh cơ chế đúng trước khi nhân rộng ra N tenant. Phụ thuộc bước 1 (cần biết field nào
   ở tier nào để biết DDL nào cần chạy).
3. **Migration declarative-only + preflight/quarantine** (§4.2-§4.5) — rename tường minh (không
   suy đoán từ diff tên cột), preflight quét data bẩn trước khi transform, quarantine record
   không transform được thay vì chặn toàn bộ migration. Phụ thuộc bước 2 (preflight/quarantine
   là một nhánh trong pipeline reconciler, không phải cơ chế độc lập).
4. **Orchestrator fan-out multi-tenant** (§6.1-§6.4) — pull-based + `SKIP LOCKED`, concurrency
   theo resource pool, xử lý version skew giữa các tenant đang ở version metadata khác nhau khi
   rollout. Chỉ có ý nghĩa sau khi bước 2-3 chứng minh chạy đúng cho một entity/tenant — fan-out
   một cơ chế chưa chứng minh ra N tenant chỉ nhân rủi ro lên N lần.
5. **Relations + FK thật** (§3.3) — sau khi entity đã tách bảng riêng (bước 1-4 xong cho entity
   đó), field `Reference` mới gắn được FK cấp DB thật (`on_delete: Restrict` mặc định) thay cho
   fallback check ở `CrudService` (bảng `records` chung dùng cơ chế nào, xem
   `crates/metap-crud/src/crud_service.rs`'s reference-integrity guard, đóng 2026-08-22 — vẫn là
   fallback đúng cho entity **chưa** tách bảng, kể cả sau khi bước 1-4 xong cho entity khác).

**Ngoài phạm vi:**
- Mọi implementation thật (code, migration, reconciler binary) — dừng ở brief.
- Đổi trigger (`@10M/entity`, §3.1) — giữ nguyên như đã ghim, brief này không đề xuất đổi.
- §8 (Audit/Aggregation/Inbound Integration), §9 (FE Onboarding), §10 (GraphQL/Microservice),
  §11 (Deployment Strategy) — nằm ngoài "Data Plane — Storage & Model" mà brief này sequencing,
  dù cùng nằm trong `multi-tenant-platform-design.md`.

## Tiêu chí chấp nhận

Brief này không có tiêu chí "chấp nhận xong" theo nghĩa thường — nó không code gì. Coi là đúng
mục đích nếu: một người đọc brief này trước, rồi đọc §3-§7, hiểu được ngay bước nào làm trước bước
nào và vì sao, không cần tự suy luận lại từ đầu.

## Ranh giới kiến trúc bị đụng tới

Không đụng gì — đây là tài liệu, không phải code. Khi thật sự bắt đầu build (có trigger), từng
bước trong 5 bước trên sẽ cần brief/ADR riêng của chính nó, theo đúng quy trình
`docs/features/README.md`.

## Rủi ro / phụ thuộc

- Thứ tự 5 bước ở trên là suy luận từ phụ thuộc kỹ thuật đọc được trong §3-§7 hôm nay — nếu
  thiết kế đó thay đổi trước khi trigger xảy ra, thứ tự này cần rà lại, không tự động đúng.
- Fallback reference-integrity ở bảng `records` chung (`crates/metap-crud/src/crud_service.rs`'s
  `referencing_fields` check trong `delete()`, đóng 2026-08-22, verify sống qua
  `crm.customers.referredBy`) **độc lập với brief này** — vẫn là cách chặn đúng cho mọi entity
  còn ở bảng chung, vô thời hạn, kể cả sau khi bước 1-4 xong cho một entity khác. Bước 5 (FK
  thật) chỉ thay thế fallback đó cho riêng entity đã tách bảng, không xoá bỏ fallback cho phần
  còn lại.
