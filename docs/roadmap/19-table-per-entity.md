## Phase 19: Table-per-entity — Bước 1/5 (2026-08-23)

Trigger: benchmark 10M row/10 tenant (xem phần "Verify fix thật" ở trên) đo được
`pg_total_relation_size('records')` = 13GB chỉ với **một** entity 10M-row trên host 9.7GB RAM —
đúng cơ chế `docs/multi-tenant-platform-design.md` §3.1 cảnh báo ("N entity × 10M trong một bảng
chung = 100M+ → index phình"), dù tune Postgres giải quyết được cho quy mô một-entity hiện tại.
Chủ dự án chốt: bắt đầu implement thật thay vì tiếp tục chỉ giữ ở readiness brief
(`docs/features/04-table-per-entity.md`) — bước 1 trong 5 bước đã sequencing ở đó.

**Bước 1 — `FieldStorage`/tier suy từ cờ metadata (§3.2), done:**

- `crates/metap-metadata/src/entity.rs`: `FieldStorage` enum (`Native`/`Column`, override tường
  minh), `FieldStorageTier` enum (`Jsonb`/`GeneratedColumn`), `resolve_field_storage_tier` (suy
  tier từ `indexed`/`sortable`/`unique`/`searchable`, override thắng khi có), `field_kind_sql_type`
  (map `FieldKind` → SQL type đúng bảng §3.2: `Money`→`numeric(18,4)`, `Reference`/`Id`→`uuid`,
  ...). `EntityField` thêm field `storage: Option<FieldStorage>`, optional + `#[serde(default)]`
  — entity cũ (kể cả low-code đã lưu trong `low_code_entity_versions`) deserialize bình thường,
  không cần backfill.
- Metadata-only, đúng scope bước 1: chưa có reconciler nào tiêu thụ `FieldStorageTier`, bảng
  `records` dùng chung vẫn lưu mọi field trong `data jsonb` bất kể tier — bước này chỉ chuẩn bị
  input cho bước 2 (reconciler diff).
- Đồng bộ chỗ cần: `crates/metap-metadata/src/openapi.rs`'s hand-written `EntitySummary` schema
  (thêm property `storage`), `crates/metap-metadata/src/lib.rs`'s re-export. `metap::metadata::*`
  (facade) đã tự động thấy qua module re-export sẵn có, không cần sửa `metap::prelude` (tier
  resolution không phải thứ một boot sequence cần).
- 56 chỗ dựng `EntityField { .. }` bằng struct-literal trên toàn repo (entity demo, test) đều
  cần thêm `storage: None,` — sửa bằng script tự động rồi build lặp lại tới khi sạch lỗi biên
  dịch, xác nhận không có literal nào bị bỏ sót.
- Kiểm chứng: 11 unit test mới (`entity.rs`'s `storage_tier_tests` — derive từ từng cờ, override
  cả hai chiều, mapping SQL type, round-trip JSON camelCase, entity cũ thiếu key `storage`
  deserialize ra `None`). `cargo build/clippy --all-targets -D warnings/fmt --check` sạch,
  `cargo test --workspace` (unit, không cần DB) sạch, không regression.

**Dọn DB dev + regenerate FE types (2026-08-23, sau khi chủ dự án xác nhận)**: quy trình chuẩn
"sau khi đổi meta-model, chạy `pnpm dev:rs` + `pnpm --filter @metap/platform-react generate:types`"
(CLAUDE.md) từng bị chặn vì DB dev tồn đọng **608 entity rác** (540 draft + 608 version, khớp
đúng pattern `test.<kịch bản e2e>_<hex>` của 21 kịch bản trong `crates/metap-lowcode`'s test
suite + `teste`) trong `low_code_entity_versions`/`low_code_entity_drafts`. Đã xoá đúng phạm vi
đó, giữ nguyên 8 entity thật (`hr.departments`/`hr.employees` — Phase 18 P0,
`helpdesk.tickets`/`bench.issues`/`demo.projects`/`demo.tickets`/`ops.demo_a_fcbfb890`/
`ops.demo_b_3ad50e6c`). Regenerate lại sạch: `generated-types.ts` giờ chỉ +3 dòng (property
`storage`, xem trên).

**Bước 2/5 — Reconciler cho một entity, đầy đủ §5.1-§5.8 (2026-08-23), done.** Crate mới
`crates/metap-reconciler`: `reconcile(desired) = introspect(actual) → diff → plan → execute`.
`compile()` field không cờ → giữ JSONB; `indexed`/`sortable`/`unique` → expression index thẳng
trên `data` (không tạo cột, đúng §5.6); `searchable` → GIN (tsvector/trigram); `storage: column`
→ cột thật + trigger đồng bộ + backfill; **`Reference` field luôn cần cột thật khi có
`ref_entity`** bất kể cờ khác — phát hiện qua e2e, không có trong pseudocode gốc (FK không thể
trỏ vào JSONB path). `diff()` đúng 5 bước §5.4 + `topo_sort`; `normalize_expr` (§5.3) dò thực
nghiệm qua Postgres thật trước khi viết (không đoán) — Postgres tự thêm `::text` vào literal và
đổi số lớp ngoặc khi lưu index definition, đoán sai sẽ khiến reconcile lặp vô hạn không hội tụ.
`executor::execute` dùng advisory lock **ghim trên một connection riêng** — phát hiện bug thật
lúc code: `pg_try_advisory_lock` gắn theo connection/session, gọi qua `&PgPool` trần sẽ đổi
connection mỗi lần, lock không bảo vệ được gì. `backfill` (§5.7) checkpoint cùng transaction với
update, cờ `completed` để `diff()` phân biệt "đang backfill dở" (resume) với "đã xong" (không
làm lại) — chi tiết phải tự thiết kế thêm để `diff()` vẫn là hàm thuần so hai `PhysicalSchema`.
`watchdog` (§5.8) lease+heartbeat, requeue hoặc `error` sau 5 lần retry. Migration mới:
`crates/migrations/0017_reconciler_tables.sql`.

**Kiểm chứng bằng e2e thật trên Postgres dev**, không chỉ unit test:
`crates/metap-reconciler/tests/reconcile_postgres.rs`, quan trọng nhất
`reconcile_converges_to_zero_ops_on_a_second_pass` — thuộc tính đúng đắn cốt lõi của reconciler
level-triggered (reconcile lần 2 với desired không đổi phải ra 0 op). E2e bắt được và sửa 3 bug
thật trước khi merge: (1) thiếu một lớp ngoặc bọc ngoài cho biểu thức có cast trong
`CREATE INDEX` (`"syntax error near ::"`), (2) FK field chưa được promote thành cột thật trước
khi gắn constraint, (3) advisory lock không ghim connection. 29 unit test + 3 e2e test,
`cargo build/clippy --all-targets -D warnings/fmt --check` sạch toàn workspace, `cargo test
--workspace` không regression. **Chưa wire vào binary nào** — không boot sequence nào gọi
`reconcile()`, bảng `records` chung vẫn là nơi `CrudService` đọc/ghi thật; vẫn là bước 2/5, chưa
phải "table-per-entity chạy thật". Chi tiết đầy đủ:
`docs/features/04-table-per-entity.md`.

**Bước 3/5 — Migration declarative-only + preflight/quarantine (2026-08-23), done.**
`crates/metap-reconciler/src/migration.rs`: `MigrationOp` (`RenameField`/`AddField`/
`WidenType`/`DropField`/`RemoveEnum`, đúng tập op §4.2, mỗi op sinh một SQL cố định, không code
hook). Chỉ `WidenType` cần preflight thật (chỉ nó có thể fail trên data bẩn) — dùng
`pg_input_is_valid` (hàm built-in Postgres 16, kiểm tra cast an toàn không cần try/catch), xác
nhận qua Postgres thật trước khi dùng chứ không đoán. `QuarantinePolicy` (`Block` mặc định |
`Coerce{fallback}` | `Quarantine`, đúng §4.4). `quarantine.rs`: bảng `{table}_quarantine` đúng
schema §4.5; `quarantine_bad_rows` di chuyển **từng dòng một** để bắt riêng lỗi
`foreign_key_violation` và bỏ qua đúng dòng đang bị tham chiếu — nhờ vậy không cần tự dò đồ thị
FK liên-entity, để Postgres (`ON DELETE RESTRICT` từ bước 2) tự chặn. Bug thật bắt được qua e2e:
CTE `batch` trong `backfill.rs` thiếu alias `t` trong khi `where_extra` của migration lại dùng
`t.data` → lỗi "missing FROM-clause entry" — chỉ lộ ra khi test transform thật có điều kiện WHERE
bổ sung. 5 e2e test (`tests/migration_postgres.rs`).

**Bước 4/5 — Orchestrator fan-out multi-tenant (2026-08-23), done.**
`crates/metap-reconciler/src/orchestrator.rs` + `crates/migrations/
0018_reconciler_orchestrator.sql` (`reconciler_entity_deployments`). `claim_due` đúng SQL
`FOR UPDATE SKIP LOCKED` §6.2; thêm `entity_name_filter` **so với thiết kế gốc** (hợp lý cho
production — sharding worker theo entity — và phát hiện cần thiết qua e2e: nhiều test chạy song
song trong cùng process tranh nhau một hàng đợi toàn cục, không phải lỗi SKIP LOCKED mà là thiếu
scope). `classify_error` (§6.4) đọc SQLSTATE thật từ error chain (không chỉ lớp lỗi ngoài cùng,
vì `executor`/`backfill` bọc lỗi qua nhiều tầng) → Transient (tự retry) / DataError / Fatal
(chuyển `blocked`, cần người). `run_claimed_batch` bound concurrency qua
`futures::stream::buffer_unordered`, cô lập lỗi từng entity đúng §6.4. `wave_size`/
`advance_wave` (canary 1-2 → 5% → 25% → 100%, halt nếu wave trước vượt ngưỡng error rate). Kiểm
chứng quan trọng nhất: 8 worker gọi `claim_due` đồng thời **thật** trên 40 dòng — không dòng nào
bị claim 2 lần, không dòng nào bị bỏ sót. 6 e2e test (`tests/orchestrator_postgres.rs`). Không
phải service chạy nền — giống `metap-cron` (thư viện) so với `cron-scheduler` (binary), một
binary orchestrator thật là việc riêng, ngoài phạm vi crate này.

**Bước 5/5 — Relations + FK thật (§3.3): thực chất đã done từ bước 2.** FK cấp DB cần một cột
vật lý thật để gắn vào — phát hiện lúc code bước 2 (không nằm trong kế hoạch ban đầu), nên
`compile()` đã phải tự suy ra field `Reference` có `ref_entity` luôn cần cột thật, độc lập với
`indexed`/`storage`. Phần còn lại (tắt fallback check ở `CrudService` cho entity đã tách bảng)
chưa làm — cần khái niệm "vị trí lưu trữ entity" chưa tồn tại, và vô nghĩa trước khi có entity
đầu tiên chạy thật qua table-per-entity.

**Trạng thái tổng**: cả 5/5 bước đã code + kiểm chứng bằng e2e trên Postgres dev thật (không
mock) — 31 unit test + 14 e2e test, `cargo build/clippy --all-targets -D warnings/fmt --check`
sạch toàn workspace, `cargo test --workspace` không regression. **Chưa wire vào bất kỳ binary
nào** — không entity sản phẩm nào thật sự chuyển sang table-per-entity, chưa có service
orchestrator chạy nền thật. Chi tiết đầy đủ: `docs/features/04-table-per-entity.md`.

