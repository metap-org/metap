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

- Không có API HTTP publish/rollout (`advance_wave` vẫn chỉ gọi được từ Rust trực tiếp, chưa có
  route) — chưa có nhu cầu thật, một entity low-code cụ thể muốn fan-out multi-tenant qua
  orchestrator này mới là trigger hợp lý để xây nó.
- Không poll riêng cho tenant `DedicatedDb` (chỉ pool chung platform) — xem "Quyết định phạm vi"
  ở trên.
- Không topo-sort phụ thuộc FK giữa nhiều entity trong cùng 1 batch claim (đúng giới hạn đã ghi
  trong `reconcile()`'s doc comment từ trước — "a caller driving many entities is responsible for
  reconciling a referenced entity before the one that references it, exactly what the
  orchestrator will automate later"; batch hiện tại xử lý độc lập từng entity, không sắp thứ tự).
- Không thread `renames` (migration/rename ops) qua vòng lặp — giống mọi call site `reconcile()`
  trực tiếp khác trong repo hôm nay, một entity đang giữa quá trình rename cần được gọi riêng.
