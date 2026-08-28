## Phase 44: `reconciler-orchestrator` — chạy orchestrator thật lần đầu (2026-08-27)

Gap đã ghi nhận rõ trong `CLAUDE.md`'s bullet về `metap-reconciler` và trong doc comment của
`crates/metap-reconciler/src/orchestrator.rs` chính nó: `docs/multi-tenant-platform-design.md`
§6 (fan-out multi-tenant) đã có đủ primitive — `claim_due` (pull-based `FOR UPDATE SKIP LOCKED`),
phân loại lỗi theo SQLSTATE, `advance_wave`/`wave_size` (canary → wave rollout) — nhưng **chưa
từng chạy như một service thật**, giống hệt cách `metap-cron` (thư viện) khác `cron-scheduler`
(binary tick nó theo giờ). `apps/jira-server`/`apps/crm-server` chỉ gọi `reconcile()` trực tiếp,
một lần lúc boot, cho entity code-authored của riêng chúng — không đi qua hàng đợi
`reconciler_entity_deployments` này.

### Quyết định phạm vi

Trước khi code, có một câu hỏi kiến trúc chưa có lời giải sẵn: **orchestrator sẽ reconcile entity
nào?** `reconcile()` cần một `EntityDefinition` thật, nhưng theo đúng nguyên tắc "không `metap-*`
crate nào biết về business entity", một ops binary chung chung (như binary này) không thể biết về
entity code-authored (`crm.customers`, `jira.issues`...) — những entity đó chỉ tồn tại bên trong
binary đã đăng ký chúng lúc biên dịch. Chỉ có **entity DB-authored (low-code) đã publish** là
nguồn metadata mọi process đều đọc được (`metap_lowcode::get_published`, quyết định "global theo
deployment" đã chốt từ Phase A) — nên đây là phạm vi thực tế đầu tiên orchestrator này phục vụ:
fan-out một entity low-code cho nhiều tenant. Entity code-authored vẫn đi đường cũ (gọi
`reconcile()` trực tiếp lúc boot), không bị thay thế.

Một giới hạn khác cũng ghi nhận rõ chứ không giấu: `reconciler_entity_deployments`
(`crates/migrations/0018_...`) áp dụng cho **mọi** database tenant (kể cả `DedicatedDb` — mỗi
tenant loại này có bản sao bảng này riêng, không thấy tenant khác). Orchestrator này chỉ poll pool
chung của platform (tenant `Schema`-strategy) — đúng kịch bản wave-rollout nhiều-tenant §6.4 mô
tả. Một tenant `DedicatedDb` muốn dùng orchestrator sẽ cần poll riêng, chưa xây (chưa có nhu cầu
thật — chưa `DedicatedDb` tenant nào chạy entity low-code qua table-per-entity).

### Thiết kế

Crate mới `crates/reconciler-orchestrator` (package `metap-reconciler-orchestrator`, lib
`reconciler_orchestrator`, bin `reconciler-orchestrator`), đúng khuôn `cron-scheduler` đã lập:

- `run_once(control_pool, router, config)` — một chu kỳ claim + reconcile, tách riêng khỏi vòng
  lặp để test gọi trực tiếp (xác định, không phải đua với sleep):
  1. `claim_due` (không filter mặc định — một hàng đợi toàn cục, đúng thiết kế `claim_due`'s doc
     comment; `OrchestratorConfig.entity_name_filter` là tuỳ chọn shard-theo-entity, cũng là cách
     e2e test của chính crate này cô lập nhau khi Rust chạy test song song).
  2. Với mỗi entity claim được: `reconcile_one` — `router.pool_for(tenant_id)` →
     `metap_lowcode::get_published(pool, entity_name)` → build `EntityDefinition` từ
     `LowCodeEntityDefinition::to_entity_definition()` rồi **ghi đè `table_name`** thành
     `metap_reconciler::qualified_table_name_for(entity_name)` (mặc định của hàm gốc luôn là
     `"records"` — orchestrator này tồn tại chính để đưa entity vào bảng riêng, nên luôn ép
     table-per-entity ở đây bất kể default đó) → `metap_reconciler::reconcile()`.
  3. `run_claimed_batch` (đã có sẵn) ghi `record_success`/`record_failure` cho từng entity —
     một entity fail không chặn entity khác, đúng §6.4.
- `run(...)` — vòng lặp `tokio::select!` biased chống shutdown, y hệt hình dạng
  `cron_scheduler::ticker::run_ticker`.
- `src/main.rs` — wiring giống `apps/crm-server/src/main.rs`'s đoạn build `Router` (nhánh
  Vault AppRole/token/EnvStore y hệt, chép lại vì không có crate tầng thấp hơn nào cả hai bên
  có thể dùng chung mà không kéo thêm dependency không cần), đọc `RECONCILER_POLL_MS`/
  `_BATCH_LIMIT`/`_MAX_ATTEMPTS`/`_CONCURRENCY` (mặc định concurrency=2, đúng khuyến nghị §6.3
  "trial/schema chung → concurrency THẤP")/`_ENTITY_FILTER`/`_WORKER_ID` từ env, tắt sạch qua
  SIGINT/SIGTERM.

`metap_reconciler::orchestrator::enqueue_deployment(pool, tenant_id, entity_name, desired_version)`
(hàm mới) — bản single-tenant của `advance_wave` (không cần cohort/canary), UPSERT trực tiếp vào
`reconciler_entity_deployments`, no-op nếu version không thực sự mới hơn (cùng guard
`WHERE ... < EXCLUDED.desired_version` `advance_wave` dùng). Đây là "ai điền vào hàng đợi" —
mảnh còn thiếu duy nhất §6.1 nhắc tới nhưng chưa ai viết. `dev-tools enqueue-reconcile <tenantId>
<entityName> <desiredVersion>` gọi thẳng hàm này — cách kích hoạt thủ công, không xây API HTTP
publish/rollout riêng (chưa có nhu cầu, sẽ là việc lớn hơn nhiều: ai được publish, "pack" nghĩa là
gì, HTTP contract ra sao — để lại cho lúc có trigger thật).

### Verify sống

- 3 e2e test mới (`crates/reconciler-orchestrator/tests/e2e_postgres.rs`, `#[ignore]`d, chạy
  qua `--ignored`): publish 1 entity low-code thật → `enqueue_deployment` → `run_once` → đúng 1
  claim, bảng `entities.*` thật tồn tại (`information_schema.tables`), row `done` với
  `applied_version` đúng, chạy `run_once` lần 2 claim đúng 0 (level-triggered hội tụ); entity
  chưa publish → claim vẫn diễn ra nhưng `reconcile_one` fail → row `blocked`/`fatal` (đúng cô
  lập §6.4, không panic cả batch); hàng đợi rỗng → `run_once` trả về 0, không lỗi.
- 1 e2e test mới cho `enqueue_deployment`
  (`crates/metap-reconciler/tests/orchestrator_postgres.rs`): seed → bump version thật → re-enqueue
  cùng version trên row đã `blocked` không đổi gì → version mới thật sự reset về `pending` và
  claim được.
- Chạy **binary thật**, không chỉ test: `dev-tools enqueue-reconcile` một entity chưa publish →
  khởi động `reconciler-orchestrator` thật (`RECONCILER_POLL_MS=1000`,
  `RECONCILER_ENTITY_FILTER` để cô lập) → log đúng thứ tự connect → poll → claim → reconcile
  fail đúng lý do ("no published low-code definition") → tick kế tiếp không claim lại (đúng, vì
  `blocked` nằm ngoài filter của `claim_due`) → gửi `SIGTERM` → log "shutdown signal received,
  exiting reconciler-orchestrator" → process thoát sạch, không cần kill -9.
- `cargo build/test/fmt/clippy -D warnings` sạch cho toàn workspace (bao gồm crate mới) xuyên
  suốt quá trình.

### Còn lại (cố ý chưa làm, ghi nhận rõ)

- Không thread `renames` (migration/rename ops) qua vòng lặp — giống mọi call site `reconcile()`
  trực tiếp khác trong repo hôm nay, một entity đang giữa quá trình rename cần được gọi riêng.
- Không topo-sort FK *xuyên* biên tenant `DedicatedDb` — chỉ trong phạm vi 1 tenant (đúng, vì FK
  target luôn nằm trong cùng database vật lý với entity tham chiếu nó).
- `POST /platform/reconciler/wave-rollout` chỉ chạy được cho tenant `Schema`-strategy (pool
  chung) — một tenant `DedicatedDb` muốn wave-rollout vẫn phải gọi `dev-tools enqueue-reconcile`
  từng tenant một, chưa có endpoint HTTP tương đương cho trường hợp đó.

### 2026-08-28 — đóng nốt 3 gap còn lại ở trên (topo-sort, DedicatedDb fan-out, HTTP wave-rollout)

Cả 3 gap "cố ý chưa làm" ghi ở bản gốc phase này (2026-08-27) đã được đóng, theo yêu cầu chủ dự
án làm tiếp phần "Phase 44 gaps":

- **Topo-sort FK trong 1 batch** — `metap_reconciler::orchestrator::topo_sort_waves` (hàm thuần,
  không I/O): với mỗi tenant trong batch, dựng đồ thị phụ thuộc từ field `Reference` (entity A có
  field trỏ `ref_entity: B`, cả A và B cùng nằm trong batch → A phụ thuộc B), rồi chạy Kahn's
  algorithm thành các "wave" — wave sau chỉ chạy sau khi wave trước xong hết. Chỉ xét cạnh trong
  cùng 1 tenant (FK target luôn ở cùng database vật lý); phụ thuộc vào entity ngoài batch (đã
  reconcile từ trước) không cần sắp thứ tự. Cycle (2 entity tham chiếu vòng nhau) rơi vào 1 wave
  chung, best-effort — không crash, chỉ mất tác dụng sắp thứ tự cho riêng phần đó. `run_once` giờ
  chạy `run_claimed_batch` theo từng wave tuần tự thay vì 1 lần cho cả batch — trước đây
  `buffer_unordered` có thể race FK constraint của entity tham chiếu với `CREATE TABLE` của entity
  bị tham chiếu nếu cả hai lần đầu cùng được claim chung 1 tick. 5 unit test pure-function (không
  cần DB): thứ tự đúng khi có phụ thuộc, độc lập thì cùng 1 wave, bỏ qua phụ thuộc ngoài batch,
  không sắp thứ tự xuyên tenant, cycle rơi về 1 wave.
- **Fan-out cho tenant `DedicatedDb`** — hàm mới `run_tick(control_pool, router, tenant_registry,
  config)`: chạy `run_once` như cũ cho pool chung (mọi tenant `Schema`-strategy), sau đó liệt kê
  mọi tenant `DedicatedDb` đang `Active` (`PostgresTenantRegistry::list`) và chạy thêm 1 lượt
  `run_once` riêng cho pool của từng tenant đó (`Router::pool_for`) — đóng đúng gap
  `crates/migrations/*.sql` áp cho mọi database tenant nên mỗi `DedicatedDb` tenant có bản
  `reconciler_entity_deployments` riêng, nhưng trước đây không ai poll nó (cùng loại gap
  `outbox-publisher` từng gặp — xem bullet của binary đó trong `CLAUDE.md`). 1 tenant fail (pool
  không resolve được, hoặc tick lỗi) chỉ log warning, không chặn tenant khác hay pool chung. Kèm
  fix liên quan bắt buộc phải sửa để fan-out này thật sự hữu dụng: `dev-tools enqueue-reconcile`
  trước đây luôn ghi thẳng vào pool chung `DATABASE_URL` — với tenant `DedicatedDb`, dòng đó nằm ở
  bảng không ai poll (vì `run_tick` giờ poll đúng bảng riêng của tenant) — sửa bằng
  `Router::pool_for(tenantId)` trước khi `enqueue_deployment`, giống pattern Phase 28 đã dùng cho
  `seed-admin`/`create-user`. 1 e2e test mới (`run_tick_reaches_a_dedicated_db_tenant_own_database`,
  `crates/reconciler-orchestrator/tests/e2e_postgres.rs`) dựng 1 database thật thứ hai
  (`create_throwaway_database`, cùng pattern `metap-control/tests/provisioning_postgres.rs`),
  provision thật qua `provision_dedicated_db_tenant`, publish + enqueue trên đúng database đó, rồi
  assert `run_tick` (không phải `run_once`) claim và reconcile đúng — `#[ignore]`d, đã **verify
  sống 2026-08-28** (phiên có Postgres thật): pass, kể cả dưới test mặc định chạy song song lẫn
  `--test-threads=1`; cùng lúc pass cả 3 e2e còn lại trong file (`run_once_reconciles_a_claimed_
  published_entity_into_its_own_table`/`run_once_records_failure_for_an_unpublished_entity`/
  `run_once_with_no_due_work_returns_zero`) và toàn bộ e2e của `metap-reconciler` (bao gồm
  `enqueue_deployment_seeds_then_ignores_a_non_newer_version`, `concurrent_claim_due_never_
  double_claims`). `cargo fmt --all --check`/`clippy -p metap-reconciler-orchestrator -p
  metap-reconciler --all-targets -- -D warnings` sạch.
- **HTTP publish/wave-rollout API** — `POST /platform/reconciler/wave-rollout`
  (`metap-control-http`, `PlatformAdminContext`-gated — đây là thao tác toàn platform trên nhiều
  tenant tuỳ ý, không phải quyền admin của riêng 1 tenant): bọc thẳng
  `metap_reconciler::orchestrator::advance_wave` (đã có unit test từ Phase 44 gốc), luôn chạy
  trên pool chung của platform (`state.pool`) vì `advance_wave` UPSERT nhiều tenant trong 1 câu
  lệnh trên 1 connection — đúng kịch bản §6.4 (nhiều tenant `Schema`-strategy dùng chung 1
  database), không áp dụng được cho tenant `DedicatedDb` (mỗi tenant loại đó ở 1 database riêng,
  không thể UPSERT chung 1 câu). Thêm path vào `metap-control-http/src/openapi_paths.rs` +
  1 test đảm bảo path này luôn có mặt trong OpenAPI doc.

`cargo build/clippy -D warnings/fmt --check` sạch toàn workspace; `cargo test --workspace` (chỉ
unit test, không cần DB) pass. `cargo test --workspace -- --ignored` (e2e) đã **verify sống
2026-08-28** ở phiên có Postgres thật — xem chi tiết ở bullet fan-out phía trên.
