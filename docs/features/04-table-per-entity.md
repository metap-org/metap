# Table-per-entity — readiness brief

- **Trạng thái:** in-progress — cả 5/5 bước đã code + kiểm chứng bằng e2e (2026-08-23,
  `crates/metap-reconciler`), nhưng **chưa wire vào bất kỳ boot sequence nào** — không entity
  sản phẩm nào thật sự chạy qua table-per-entity, và chưa có binary orchestrator chạy nền thật
- **Người đề xuất:** chủ dự án (yêu cầu lên plan cùng Organization & Identity), 2026-08-22
- **Track sở hữu:** Backend Core
- **Phase roadmap liên quan:** chưa gắn phase — sẵn sàng gắn khi chủ dự án chốt lịch build đầy đủ

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
xong trước mảnh nào vì lý do phụ thuộc kỹ thuật thật (không phải sở thích).

**Cập nhật 2026-08-23 — trigger không còn thuần lý thuyết.** Benchmark `sustained_concurrent_
list_across_many_tenants_at_ten_million_rows` (`docs/roadmap.md`) seed thật 10 tenant × 1M
`helpdesk.tickets` = 10M row trong 1 entity, đúng ngưỡng `@10M/entity`, đo trên host dev
`~9.7GB RAM`: `shared_buffers=128MB` mặc định khiến p95=1306ms/p99=1479ms (từ p95=31ms/p99=34ms ở
500K row); tune `shared_buffers` lên 2GB + thêm 1 composite index đưa p95/p99 xuống 78ms/91ms
(giảm ~17 lần) — **fix ở tầng tuning thật sự hiệu quả cho MỘT entity ở quy mô này**. Nhưng
`pg_total_relation_size('records')` đã lên **13GB** (bảng + toàn bộ index) chỉ với một entity
10M-row, trên host chỉ 9.7GB RAM — đúng cơ chế §3.1 mô tả ("N entity × 10M trong một bảng chung =
100M+ → index phình") nhưng ở quy mô nhỏ hơn N=1: tune RAM/index cho một entity nặng là khả thi,
không scale tuyến tính khi N entity cùng nặng cùng lúc trên cùng một bảng vật lý. Kết luận thực
tế: chưa có entity nào trong repo *sản phẩm* chạm ngưỡng 10M row hiện tại — chưa có gì thay đổi
về thời điểm bắt đầu *bắt buộc* — nhưng khác với brief version trước, giờ đã có số đo thật (không
phải suy diễn) cho thấy đúng ngưỡng này, đúng cơ chế này. Brief vẫn chuẩn bị sẵn *thứ tự* build,
nhưng độ ưu tiên nên được chủ dự án xem lại — không còn lý do để hoãn tới khi có entity thật chạm
ngưỡng mới bắt đầu suy nghĩ về nó.

## Phạm vi

**Trong phạm vi — build order đề xuất (5 bước, theo phụ thuộc kỹ thuật, không phải song song
tuỳ ý):**

1. **`FieldStorage`/tier suy từ cờ metadata** (§3.2) — `indexed`/`sortable`/`unique`/`searchable`
   trên `EntityField` tự động promote field lên generated column (T2) hay giữ JSONB (T1);
   `FieldStorage { Column, Native }` là override tường minh khi cần. Làm trước vì mọi bước sau
   (reconciler diff, migration transform) đều cần biết "field này nên nằm ở đâu" làm input —
   không có bước này thì reconciler không có gì để diff.
   **done (2026-08-23)**: `crates/metap-metadata/src/entity.rs` — `FieldStorage` (`Native`/
   `Column` override, camelCase JSON `"native"`/`"column"`), `FieldStorageTier` (`Jsonb`/
   `GeneratedColumn`), `resolve_field_storage_tier(&EntityField) -> FieldStorageTier`,
   `field_kind_sql_type(FieldKind) -> &str` (đúng bảng map §3.2: Money→`numeric(18,4)`,
   Reference/Id→`uuid`, ...). `EntityField.storage: Option<FieldStorage>` — optional, mặc định
   `None` (suy từ cờ), round-trip đúng qua JSON/low-code JSONB, không phá vỡ entity cũ chưa có
   field này (`#[serde(default)]`). Metadata-only — chưa có reconciler nào tiêu thụ, `records`
   dùng chung vẫn lưu mọi field trong `data jsonb` bất kể tier. Đã đồng bộ
   `crates/metap-metadata/src/openapi.rs`'s hand-written `EntitySummary` schema (thêm property
   `storage`) và `crates/metap/src/lib.rs`'s facade (`metap::metadata::FieldStorage` etc., qua
   module re-export sẵn có — không cần thêm vào `prelude`, đúng tinh thần "prelude chỉ chứa thứ
   một boot sequence cần"). 11 unit test (`entity.rs`'s `storage_tier_tests`), toàn bộ
   `cargo build/clippy -D warnings/fmt --check`, `cargo test --workspace` sạch.
   **Dọn dẹp + regenerate FE types (2026-08-23, sau khi chủ dự án xác nhận)**: xoá 540 draft +
   608 version rác trong `low_code_entity_drafts`/`low_code_entity_versions` — khớp chính xác
   pattern `test.<kịch bản e2e>_<hex>` (21 kịch bản: `concurrent_publish`, `dangling_ref`,
   `disable_toggle`, `disabled_publish`, `draft_roundtrip`, `export_all_a/b`, `export_disabled`,
   `export_filter_a/b`, `impact`, `list_all_a/b`, `list_versions`, `preview`/
   `preview_dangling_ref`, `reserved`, `rollback_ok`, `rollback_unknown`, `versioning`,
   `workflow_guard`) cộng `teste` — debris từ `crates/metap-lowcode` e2e test suite qua nhiều
   phiên, chưa tự dọn. **Giữ nguyên** 8 entity thật:  `hr.departments`/`hr.employees`
   (Phase 18 P0 verify), `helpdesk.tickets`/`bench.issues`/`demo.projects`/`demo.tickets`/
   `ops.demo_a_fcbfb890`/`ops.demo_b_3ad50e6c`. Sau dọn: `pnpm dev:rs` +
   `pnpm --filter @metap/platform-react generate:types` chạy sạch — diff `generated-types.ts`
   giờ đúng +3 dòng (property `storage`, cộng một `guard?: unknown` không liên quan lộ ra vì có
   entity workflow thật trong registry lúc generate).
2. **Reconciler level-triggered cho MỘT entity đơn lẻ** (§5.1-§5.2, §5.4 thuật toán diff, §5.6
   executor) — `reconcile = diff(desired, actual) → plan → execute`, tự lành sau crash, không
   rollback (DDL online không rollback được). Chạy cho một entity, chưa multi-tenant fan-out —
   chứng minh cơ chế đúng trước khi nhân rộng ra N tenant. Phụ thuộc bước 1 (cần biết field nào
   ở tier nào để biết DDL nào cần chạy).
   **done (2026-08-23), đầy đủ §5.1-§5.8** — crate mới `crates/metap-reconciler`:
   - `PhysicalSchema`/`ColumnSpec`/`IndexSpec`/`FkSpec`/`UniqueSpec` (§5.2) — desired
     (`compile()`) và actual (`introspect()`, đọc thẳng `pg_catalog`) quy về cùng một type rồi
     mới so sánh, đúng tinh thần thiết kế.
   - `compile()`: field không có cờ nào → giữ JSONB; `indexed`/`sortable`/`unique` → **expression
     index** thẳng trên `data` (không tạo cột — đúng §5.6 "mặc định né `GENERATED ... STORED`");
     `searchable` → GIN (tsvector cho `fts`, trigram cho substring); `storage: column` → cột
     thật + trigger đồng bộ + backfill; **`Reference` field có `ref_entity` luôn cần cột thật**
     bất kể cờ khác (phát hiện qua e2e: FK không thể trỏ vào JSONB path, phải là cột SQL —
     không có trong pseudocode gốc, phải tự suy ra khi implement thật).
   - `diff()`: 5 bước đúng §5.4 (rename trước, column add-only không drop, index
     valid/invalid/lệch → rebuild, FK not-valid/validate resume, unique) + `topo_sort` giữ đúng
     thứ tự drop-trước-create cho từng cặp rebuild cùng tên. `normalize_expr` (§5.3) dò thực
     nghiệm qua Postgres thật (không đoán): phải dò trước cách Postgres viết lại biểu thức khi
     lưu (`::text` tự thêm vào literal, số lớp ngoặc khác nhau) rồi mới viết hàm normalize đúng
     — nếu đoán sai, reconcile sẽ lặp vô hạn "cần rebuild" dù không đổi gì.
   - `executor::execute`: advisory lock **trên một connection ghim riêng** (phát hiện bug thật
     lúc code — `pg_try_advisory_lock` gắn theo session/connection, gọi qua `&PgPool` trần sẽ
     lấy connection khác nhau mỗi lần, lock không thực sự bảo vệ gì); `Migrating` status khi có
     op `Cost::Heavy`; mỗi op chạy theo đúng `ExecutionMode` (transactional/CONCURRENTLY ngoài
     transaction/batched).
   - `backfill::run_heavy_backfill` (§5.7): keyset (không OFFSET), checkpoint cùng transaction
     với update batch, throttle, cờ `completed` trong `reconciler_backfill_progress` để `diff()`
     biết "cột đã tồn tại nhưng chưa backfill xong" (resume) khác "đã backfill xong" (không làm
     lại) — chi tiết không có sẵn trong pseudocode gốc, phải tự thiết kế thêm để giữ `diff()`
     vẫn là hàm thuần so hai `PhysicalSchema`.
   - `watchdog` (§5.8): lease + heartbeat trong `reconciler_entity_status`, requeue hoặc
     chuyển `error` sau 5 lần retry.
   - Migration mới: `crates/migrations/0017_reconciler_tables.sql`
     (`reconciler_entity_status`, `reconciler_backfill_progress`).
   - **Kiểm chứng bằng e2e thật trên Postgres dev** (không chỉ unit test): 3 test
     `crates/metap-reconciler/tests/reconcile_postgres.rs`, quan trọng nhất
     `reconcile_converges_to_zero_ops_on_a_second_pass` — thuộc tính đúng đắn cốt lõi của một
     reconciler level-triggered: reconcile lần 2 với desired state không đổi phải ra 0 op, nếu
     không đúng số lượt trên sẽ không bao giờ hội tụ. Cộng `storage_column_backfills_existing_
     rows_and_syncs_new_ones` (backfill dữ liệu có sẵn + trigger đồng bộ dòng mới) và
     `reference_field_gets_a_real_fk_once_both_entities_are_tables` (FK thật, validated). 3 bug
     thật được e2e test bắt được và sửa trước khi merge (không phải suy đoán): thiếu ngoặc bọc
     ngoài cho biểu thức có cast trong `CREATE INDEX` ("syntax error near ::"), FK field chưa
     được promote thành cột thật, và advisory lock qua pool không ghim connection. 29 unit test
     (`compile`/`diff`/`normalize`) + 3 e2e test, `cargo build/clippy --all-targets -D warnings/
     fmt --check` sạch cho toàn workspace, `cargo test --workspace` không regression.
   - **Chưa làm**: chưa wire vào bất kỳ binary nào (không boot sequence nào gọi `reconcile()`),
     bảng `records` dùng chung vẫn là nơi `CrudService` đọc/ghi thật — đây vẫn là bước 2/5, chưa
     phải "table-per-entity đã chạy thật".
3. **Migration declarative-only + preflight/quarantine** (§4.2-§4.5) — rename tường minh (không
   suy đoán từ diff tên cột), preflight quét data bẩn trước khi transform, quarantine record
   không transform được thay vì chặn toàn bộ migration. Phụ thuộc bước 2 (preflight/quarantine
   là một nhánh trong pipeline reconciler, không phải cơ chế độc lập).
   **done (2026-08-23)**: `crates/metap-reconciler/src/migration.rs` — `MigrationOp`
   (`RenameField`/`AddField`/`WidenType`/`DropField`/`RemoveEnum`, đúng tập op §4.2, mỗi op sinh
   một SQL cố định, không có code-hook nào); chỉ `WidenType` có thể fail trên data bẩn nên chỉ
   nó có preflight thật (dùng `pg_input_is_valid` — hàm built-in Postgres 16, kiểm tra cast an
   toàn không cần try/catch, dò thực nghiệm qua Postgres thật trước khi dùng, không đoán).
   `QuarantinePolicy` (`Block` mặc định | `Coerce{fallback}` | `Quarantine`) đúng §4.4.
   `crates/metap-reconciler/src/quarantine.rs`: bảng `{table}_quarantine` (§4.5, đúng schema
   thiết kế), `quarantine_bad_rows` **di chuyển từng dòng một** (không phải 1 câu DELETE hàng
   loạt) để bắt riêng lỗi `foreign_key_violation` (SQLSTATE `23503`) và bỏ qua đúng 1 dòng đang
   bị tham chiếu thay vì huỷ cả batch — đây chính là cách thực hiện luật "không quarantine row
   đang bị ref" (§4.5) mà **không cần** crate này tự dò đồ thị FK liên-entity, để Postgres tự
   chặn bằng `ON DELETE RESTRICT` sẵn có từ bước 2. `resolve()` đơn giản hoá so với thiết kế gốc
   (không "áp lại full transform chain" vì crate chưa có log lịch sử migration để replay — ghi
   rõ giới hạn này trong doc comment, người sửa data chịu trách nhiệm data đã hợp lệ với schema
   hiện tại). Preflight orphan-ref cho FK (`preflight_fk_orphans`) tách riêng khỏi
   `MigrationOp` vì nó gắn với `FkSpec`, không phải một data-evolution op — hành vi mặc định
   (không gọi preflight) vẫn an toàn: `ValidateForeignKey` tự fail sạch với lỗi Postgres thật,
   tương đương Policy::Block. `backfill.rs` tổng quát hoá thành `run_batched_update` dùng chung
   cho cả bước 2 (sync cột) lẫn bước 3 (transform `data`).
   **Bug thật bắt được qua e2e**: CTE `batch` thiếu alias `t` trong khi `where_extra` của
   migration lại dùng `t.data` → lỗi "missing FROM-clause entry for table t" — chỉ lộ ra khi
   chạy transform thật có điều kiện WHERE bổ sung (bước 2 không dùng `where_extra` nên không bắt
   được lỗi này). 5 e2e test (`tests/migration_postgres.rs`): preflight thuần không đổi data,
   Block từ chối, Coerce dùng fallback cho dòng xấu + transform đúng dòng tốt, Quarantine di
   chuyển dòng xấu ra trước + resolve đưa lại, và dòng đang bị FK tham chiếu được bỏ qua đúng
   như thiết kế.
4. **Orchestrator fan-out multi-tenant** (§6.1-§6.4) — pull-based + `SKIP LOCKED`, concurrency
   theo resource pool, xử lý version skew giữa các tenant đang ở version metadata khác nhau khi
   rollout. Chỉ có ý nghĩa sau khi bước 2-3 chứng minh chạy đúng cho một entity/tenant — fan-out
   một cơ chế chưa chứng minh ra N tenant chỉ nhân rủi ro lên N lần.
   **done (2026-08-23)**: `crates/metap-reconciler/src/orchestrator.rs` + migration
   `crates/migrations/0018_reconciler_orchestrator.sql` (bảng `reconciler_entity_deployments`,
   tương đương `control.entity_deployments` trong thiết kế — đặt ở schema `public` cho nhất
   quán với 2 bảng reconciler khác, không dùng schema riêng). `claim_due` đúng câu SQL
   `FOR UPDATE SKIP LOCKED` §6.2 (pending trước, failed sau, giới hạn `max_attempts`), thêm
   tham số `entity_name_filter` **so với thiết kế gốc** — hợp lý cho cả production (sharding
   worker theo entity) lẫn cần thiết cho test (phát hiện qua e2e: nhiều test chạy song song
   trong cùng process tranh giành chung một hàng đợi toàn cục, cướp dòng của nhau — không phải
   lỗi SKIP LOCKED mà là thiếu scope, phải sửa mới cô lập được test đúng). `classify_error`
   (§6.4) đọc SQLSTATE thật từ `sqlx::Error` trong error chain của `anyhow::Error` (không phải
   chỉ lớp lỗi ngoài cùng, vì `executor`/`backfill` bọc lỗi qua nhiều tầng) → 3 nhóm Transient/
   DataError/Fatal; chỉ Transient được tự động retry (`status='failed'`, còn nằm trong hàng đợi
   claim), DataError/Fatal chuyển `status='blocked'` (ngoài phạm vi claim, cần người can thiệp).
   `run_claimed_batch` bound concurrency qua `futures::stream::buffer_unordered` (không dùng
   semaphore thủ công), cô lập lỗi từng entity đúng §6.4 ("một tenant fail không chặn tenant
   khác"). `wave_size`/`advance_wave` (canary 1-2 → 5% → 25% → 100%, halt nếu wave trước vượt
   ngưỡng error rate). **Chưa làm**: không phải một service chạy nền — giống `metap-cron`
   (thư viện) so với `cron-scheduler` (binary tick theo giờ), một binary orchestrator thật là
   phần việc riêng, ngoài phạm vi crate này. Kiểm chứng: 6 e2e test
   (`tests/orchestrator_postgres.rs`), quan trọng nhất là 8 worker gọi `claim_due` đồng thời
   thật (không mock) trên 40 dòng — xác nhận không dòng nào bị claim 2 lần, không dòng nào bị bỏ
   sót.
5. **Relations + FK thật** (§3.3) — sau khi entity đã tách bảng riêng (bước 1-4 xong cho entity
   đó), field `Reference` mới gắn được FK cấp DB thật (`on_delete: Restrict` mặc định) thay cho
   fallback check ở `CrudService` (bảng `records` chung dùng cơ chế nào, xem
   `crates/metap-crud/src/crud_service.rs`'s reference-integrity guard, đóng 2026-08-22 — vẫn là
   fallback đúng cho entity **chưa** tách bảng, kể cả sau khi bước 1-4 xong cho entity khác).
   **Thực chất đã done từ bước 2** — phát hiện lúc code bước 2 (không phải kế hoạch ban đầu):
   FK cấp DB **cần một cột vật lý thật** để gắn vào, JSONB path không đủ; nên `compile()`
   (bước 2) đã phải tự suy ra rằng field `Reference` có `ref_entity` **luôn** cần cột thật, độc
   lập với `indexed`/`storage`, và `diff()`/`executor` đã tạo FK (`NOT VALID` rồi `VALIDATE`)
   đầy đủ, kiểm chứng bằng e2e `reference_field_gets_a_real_fk_once_both_entities_are_tables`
   (bước 2). **Phần còn lại, chưa làm**: tắt fallback check ở `CrudService` cho entity đã tách
   bảng — cần `CrudService` biết "entity này đang ở bảng riêng hay bảng `records` chung", một
   khái niệm "vị trí lưu trữ entity" chưa tồn tại ở đâu cả (không nằm trong `MetadataRegistry`).
   Vô nghĩa để làm ngay bây giờ vì chưa có entity nào thật sự được migrate sang bảng riêng
   (không boot sequence nào gọi `reconcile()`) — gắn lại việc này khi có entity đầu tiên chạy
   thật qua table-per-entity.

**Ngoài phạm vi (vẫn đúng sau khi bước 1-5 done):**
- Một binary/service orchestrator chạy nền thật (xem bước 4's "Chưa làm").
- Wire `reconcile()` vào bất kỳ boot sequence nào — chưa có entity sản phẩm nào thật sự chuyển
  sang table-per-entity, toàn bộ `crates/metap-reconciler` vẫn là thư viện chưa được gọi từ
  `apps/crm-server` hay bất kỳ binary nào khác.
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
