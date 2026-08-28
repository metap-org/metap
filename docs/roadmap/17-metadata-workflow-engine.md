## Phase 17: Metadata-driven Workflow Engine

**Trạng thái: Increment 1 đã xong (2026-08-21).** Trigger đi theo yêu cầu trực tiếp của chủ dự án
("ưu tiên cực cao"), sau khi review thấy `metap-cron` (Phase 13) đã có sẵn ~70% hạ tầng cần thiết
(claim-safe polling, outbox dispatch, `EventBus::subscribe`). Quyết định kiến trúc: tiến hoá
`metap-cron`/`cron-scheduler` tại chỗ, không tách crate `metap-orchestration` mới — chi tiết đầy
đủ + trade-off so sánh ở `docs/features/02-workflow-engine.md` và
[09. Architecture Decisions](architectures/09-adr.md).

**Increment 1 — trigger "on state transition"**: `cron_jobs` giờ có `triggerType`
(`schedule`/`on_transition`) + `triggerConfig` (`{entity, action}`), cùng bảng, cùng
`targetType`/`targetConfig`/`dispatchMode` như job lịch cũ. `cron-scheduler` thêm một consumer
mới (`trigger.rs`) subscribe `#.workflow.transitioned` — khi một transition thật khớp
`(tenant, entity, action)` của một job `on_transition` đang enable, job đó tự dispatch qua đúng
cơ chế outbox/direct đã có, không cần polling. Kèm retry-with-backoff
(`maxAttempts`/`retryBackoffSeconds`, backoff nhân đôi mỗi lần thất bại, mặc định 1 lần thử = giữ
nguyên hành vi cũ) cho mọi job (cả `schedule` lẫn `on_transition`) — đóng gap đã ghi từ Phase 5
("chỉ ghi `status: failed` rồi bỏ đó").

**Đổi kèm ngoài scope gốc, bắt buộc để trigger hoạt động đúng multi-tenant**:
`metap_workflow::emit_transitioned` giờ nhận thêm `tenant_id`, ghi vào payload outbox — trước đó
payload `<entity>.workflow.transitioned` không mang tenant, nên không consumer nào subscribe được
event đó mà biết chắc nó thuộc tenant nào.

**Verify sống qua HTTP + RabbitMQ + Postgres thật** (không phải suy đoán): tạo record C (draft) +
job `on_transition` khi `crm.customers` bị `block` → target `workflow_transition` activate C; tạo
record B, activate B (không khớp trigger, không dispatch gì) rồi block B (khớp trigger) → log
`cron-scheduler` ghi "cron job triggered on transition" rồi "cron job executed" → record C
`draft` → `active` thành công, `cron_job_runs` ghi `status: "success"`. Cộng 6 e2e test mới
(`crates/metap-cron/tests/cron_store_postgres.rs`): match đúng entity/action, không match sai
action, không rò cross-tenant, direct-mode job trả về đúng, retry-with-backoff lên lịch đúng +
`claim_due_retries` chỉ claim khi tới hạn, hết `maxAttempts` thì không retry nữa.

Migration: `crates/migrations/0015_cron_jobs_trigger_and_retry.sql`.

**Increment 2 — chuỗi activity tuần tự (2026-08-28, done).** `TargetType::Steps` thứ 5 —
`targetConfig: { steps: [{targetType, targetConfig}, ...] }`, chạy tuần tự trong cùng một lần
dispatch (chưa cần durable pause), mỗi step là 1 trong 4 `TargetType` còn lại (cấm lồng `"steps"`,
chặn ngay lúc tạo job). Bảng mới `workflow_runs` (`crates/migrations/0023_workflow_runs.sql`) theo
dõi `current_step_index`/`context`/`status` cho từng lần dispatch — thuần audit, không step nào
đọc lại output step trước. Step fail thì dừng cả chuỗi tại đó, `cron_job_runs` cũng `failed` và
retry-with-backoff của Increment 1 **tái dùng nguyên vẹn** (retry lại từ step 0, không resume —
resume có state là việc của Increment 3). `GET /admin/cron-jobs/{jobId}/runs/{runId}/workflow-run`
route mới để xem progress. 4 e2e test mới (`crates/metap-cron/tests/workflow_runs_postgres.rs`) +
verify sống đầy đủ qua HTTP + RabbitMQ + Postgres thật: happy path (2 step activate 2 record khác
nhau, cả hai đúng trạng thái cuối), failure path (step 0 fail 409 → chuỗi dừng, step 1's target
record không hề bị đụng, `workflow-run` ghi đúng step/lỗi), validation (thiếu/rỗng/lồng `steps`
đều 400 trước khi ghi DB). Chi tiết đầy đủ + ghi chú hạ tầng phiên (Postgres/RabbitMQ cài native
qua apt thay Docker — Docker Hub bị chặn bởi policy tổ chức lúc chạy phiên) ở
`docs/features/02-workflow-engine.md`'s "Tiêu chí chấp nhận (Increment 2)".

Còn lại — Increment 3 (`wait_event`, durable pause) — vẫn ở trạng thái approved, chưa code, chờ
Increment 1+2 chạy thật lộ ra nhu cầu cụ thể trước khi thiết kế tiếp (trigger-based, không suy
đoán trước). Admin UI cho `triggerType: on_transition`/`targetType: steps` không nằm trong phạm vi
Increment 1/2 (backend track, FE track khác lo riêng).

**Fix (2026-08-22, phát hiện khi nghiên cứu Organization & Identity × table-per-entity, độc lập
với cả hai — không phải một phần của feature brief `03-organization-identity.md`, vẫn `proposed`):**
`CrudService::delete()` (`crates/metap-crud/src/crud_service.rs`) trước đây không quét record nào
đang tham chiếu tới record bị xoá qua field `Reference` — xoá để lại orphan reference âm thầm,
không lỗi, không chặn. Giờ quét toàn registry tìm mọi field `Reference` trỏ tới entity đang xoá
(kể cả self-reference), chặn `409 record_referenced` nếu còn record tham chiếu, trong cùng
transaction với chính lệnh xoá — Restrict mặc định, chưa có per-field override. Verify sống qua
HTTP thật (`crm.customers.referredBy`): xoá A khi B còn trỏ tới → 409, A vẫn nguyên; xoá B trước
rồi xoá A → 200. 2 e2e test mới (`crates/metap-crud/tests/crud_service_postgres.rs`).

**Fix (2026-08-22, gap đầu tiên đã ghi trong `docs/features/01-fe-platform-overhaul.md`, tự bản
thân feature đó vẫn `proposed`/chưa scope xong):** `plan_list`
(`crates/metap-query/src/query_planner.rs`) trước đây luôn dùng `entity.list_views.first()` —
list view thứ hai của một entity (vd `accounting.journal`'s `ledger`) không gọi được qua API list
dù đã khai báo đầy đủ trong metadata. Thêm `ListInput.list_view: Option<String>` +
`GET /api/:entity?listView=<name>` (`crates/metap-http/src/routes/records.rs`); không truyền giữ
nguyên hành vi cũ, tên không tồn tại trả `400 unknown_list_view` (không âm thầm fallback về view
mặc định). 2 e2e test mới (`crates/metap-query/tests/query_planner_postgres.rs`), verify sống
qua HTTP thật trên `accounting.journal`'s `ledger`. Xoá hàng risk tương ứng ở
`docs/architectures/11-risks.md` (đã resolve).

**Fix + benchmark thật (2026-08-22, trigger: chủ dự án yêu cầu bằng chứng số liệu trước khi bắt
đầu xây một app thật — dạng Jira — trên bảng `records` chung).** Hai phần:

1. **Fix gap `search_mode: "substring"` không có index** — xác nhận bằng code: `ILIKE
   '%value%'` (`condition_to_sql.rs`/`query_planner.rs`) trước đây không có index nào hỗ trợ
   (không `pg_trgm` ở đâu trong repo) — mọi filter substring là quét tuần tự trong phạm vi
   `(tenant_id, entity)`. Thêm `crates/migrations/0016_pg_trgm_extension.sql`
   (`CREATE EXTENSION pg_trgm`) + `IndexReconciler::ensure_trgm_index`
   (`crates/metap-peripherals/src/index_reconciler.rs`) — GIN trigram index, cùng field-expression
   phải khớp chính xác với `QueryPlanner` (nguyên tắc đã có từ index thường/FTS). 1 e2e test mới
   xác nhận Postgres planner thực sự chọn index này cho đúng dạng `ILIKE` được sinh ra
   (`crates/metap-peripherals/tests/peripherals_postgres.rs`).
   - **Phát hiện phụ, đã ghi vào risk row** (`docs/architectures/11-risks.md`): hai lệnh
     `CREATE INDEX CONCURRENTLY` đồng thời trên cùng bảng `records` từ hai session khác nhau
     **deadlock thật** — tái hiện trực tiếp qua `psql`. Không phải rủi ro trong một process
     (`reconcile_inner` tuần tự), nhưng là rủi ro thật khi ≥2 instance `crm-server` cùng boot một
     lúc (rolling deploy/scale ngang) và cùng cần build index mới — chưa fix (cần
     `pg_advisory_lock`), chỉ mới ghi nhận.
2. **Benchmark thật ở 500K record** (không phải 200 record như load test cũ) — seed thẳng qua SQL
   (`generate_series` + `jsonb_build_object`, ~500K record entity `bench.issues` mô phỏng Jira:
   `title`/`status` (indexed)/`assignee` (indexed)/`description` (fts)/`priority`), `VACUUM
   ANALYZE`, đo qua `EXPLAIN (ANALYZE, BUFFERS)` **và** qua HTTP thật trên `crm-server` đang chạy.
   Kết quả (debug build, một máy dev, không phải production):
   - Filter chính xác trên field `indexed` (status) + sort + limit 100: **~1.5ms** (SQL), **10-49ms**
     (HTTP end-to-end, warm request ~10-20ms).
   - Substring search (title, `ILIKE`) — **trước fix** (không trigram index): Parallel Seq Scan,
     **177ms**. **Sau fix** (trigram index): Bitmap Index Scan, **~35ms** (SQL), **22-29ms**
     (HTTP) — ~5x nhanh hơn, đo trực tiếp bằng cách drop rồi tạo lại index trên cùng dataset.
   - Full-text search (description, độ chọn lọc thật ~2%): **~32ms** (SQL), **27-30ms** (HTTP).
   - Ghi record mới (`POST`) với 4 index đang phải maintain trên 500K row nền: **9-15ms**.
   
   **Kết luận**: ở quy mô 500K row/entity — cao hơn nhiều so với năm đầu vận hành thật của một
   app kiểu Jira nội bộ — bảng `records` chung + JSONB expression index (không cần table-per-entity,
   không cần generated column/cột thật) cho latency dưới 50ms cho mọi dạng query thực tế
   (exact/substring/FTS/sort/write). Không phải bằng chứng "không bao giờ cần table-per-entity" —
   trigger `@10M/entity` (`docs/architectures/09-adr.md`) giữ nguyên, đây chỉ là bằng chứng số liệu
   rằng **500K không phải ngưỡng đáng lo**, đủ để bắt đầu xây một app thật mà không cần chờ
   table-per-entity trước. Benchmark chưa test: concurrent load (nhiều client cùng lúc), release
   build, deep keyset pagination xa trang đầu, quy mô >1M row.

**Đóng gói benchmark thành tooling tái sử dụng được (2026-08-22)** — benchmark thủ công ở trên
giờ là 2 script commit vào repo: `apps/crm-server/scripts/seed-bulk.sh` (seed 300K+ row qua SQL
trực tiếp, nhanh hơn nhiều bậc so với seed qua HTTP của `load-test.sh`) và `bench-queries.sh`
(chạy 4 dạng query thật, báo cáo cả `EXPLAIN ANALYZE` lẫn latency HTTP). Cộng một stack
Grafana/Prometheus **opt-in** (`docker compose --profile observability up -d`, KHÔNG thuộc stack
dev mặc định) xem tài nguyên Postgres realtime lúc benchmark (connections, cache hit ratio,
transactions/sec, deadlock, temp file spill) — dashboard tự động provision, không setup tay.
`pg_stat_statements` bật sẵn trên service `postgres` cho truy vấn per-query trực tiếp qua `psql`.
Verify sống: chạy cả 2 script thật ở 100K row, xác nhận đúng index được chọn (trigram/GIN/B-tree)
và pipeline metric Postgres → exporter → Prometheus → Grafana dashboard hoạt động end-to-end
(kiểm từng metric name dùng trong dashboard JSON tồn tại thật qua Prometheus API, không suy đoán).

**Thêm metric cho chính `crm-server` (2026-08-22)** — ban đầu chỉ đo tài nguyên Postgres, thiếu
phần BE. `GET /metrics` mới (`crates/metap-http/src/metrics.rs`+`routes/metrics.rs`, public,
cùng quy ước `/health`) — request-level (`axum-prometheus`: count/duration/in-flight theo từng
route) + process-level (`metrics-process`: CPU/RSS/fd/thread). Một vấn đề thật gặp phải và đã xử
lý: `PrometheusMetricLayer::pair()` cài global `metrics` recorder — gọi 2 lần trong cùng process
sẽ panic, đúng kịch bản 3 test e2e trong `crates/metap-http/tests/http_server.rs` mỗi test tự
gọi `build_router` riêng — sửa bằng `OnceLock` guard (`prometheus_handle()`), verify bằng cách
chạy cả 4 test e2e cùng lúc, không panic. Prometheus thêm scrape target `crm-server` (chạy trên
host qua `pnpm dev:rs`, cần `extra_hosts: host-gateway` để container Prometheus reach host trên
Linux). Dashboard thứ 2 "Metap — crm-server Resource Metrics" — verify sống mọi metric name qua
Prometheus API trước khi đưa vào dashboard JSON (phát hiện `postgres-exporter` tự nó cũng expose
metric tên `process_*` giống hệt — phải lọc `job="crm-server"` trong mọi panel để không lẫn).
FE (`crm-fe`) cố tình không đo — không phải service dài hạn có resource để scrape theo kiểu này.
Chỉ `crm-server`, chưa làm cho `outbox-publisher`/`notification-worker`/`cron-scheduler` (không
có HTTP server để gắn `/metrics` vào — chưa có trigger cho việc riêng đo các worker đó).

Chi tiết đầy đủ ở `docs/local-benchmarking.md`.

**Benchmark nghiệp vụ phức tạp thật, 10 phút, đồng thời (2026-08-23) — chủ dự án chỉ ra đúng:
benchmark `pgbench` ở trên vẫn chỉ là MỘT bảng, filter đơn giản, không phản ánh nghiệp vụ thật
(multi-entity, hydration, ABAC).** Dựng schema quan hệ thật qua chính low-code builder:
`hr.departments` (20 row) → `hr.employees` (200 row, `departmentId` Reference) →
`helpdesk.tickets` (500K row, `assigneeId`+`departmentId` Reference, có `refDisplayField` cho cả
hai, có workflow 3 transition), cộng policy ABAC record-level theo phòng ban
(`fromContext.departmentId`) — đúng pattern Organization & Identity (Phase 18). Chạy
`CrudService::list()` **trực tiếp** (không qua HTTP — bỏ qua rate limiter theo IP, thứ không
phản ánh năng lực xử lý nghiệp vụ, chỉ là giới hạn tầng HTTP riêng), 20 worker đồng thời, 600
giây liên tục. Mỗi lần gọi `list()` ở đây trả giá đúng: context permission check + load record
policy snapshot + base query có điều kiện ABAC + `hydrate_related_display` cho **cả hai** field
Reference (mỗi field lại có permission check + batch fetch + record-condition check riêng) —
không phải một xấp xỉ, đúng cost shape thật của một list view org-scoped.

**Kết quả**: 448,399 lệnh gọi `list()` thành công / 600s, **747.3 list()/s trung bình, 0% lỗi**,
latency ổn định suốt 10 phút (không suy giảm theo thời gian) — p50=26ms, p95=31ms, p99=34ms,
max=120ms. Cache hit ratio **100%** trong suốt cửa sổ đo (B-tree index nhỏ cho
`assigneeId`/`departmentId`, khác hẳn 2 GIN index 47-48MB của benchmark đơn bảng trước — vừa đủ
trong `shared_buffers` mặc định 128MB, không cần tune). 0 deadlock mới, 0 temp file spill.

**Kết luận**: nghiệp vụ multi-entity thật (2 Reference field hydrate + ABAC record-level + workflow)
**không phải điểm nghẽn** ở quy mô 500K ticket/200 employee/20 department — nhanh hơn nhiều so
với lo ngại ban đầu, kể cả với ~5-9 query/lệnh `list()` (permission + snapshot + base + 2×
hydration). Test tool: `crates/metap-crud/tests/crud_service_postgres.rs`'s
`sustained_concurrent_list_against_a_real_multi_entity_abac_workflow` (`#[ignore]`d, cần seed
out-of-band trước — không chạy trong e2e suite thường).

**Mở rộng 10M row / 10 tenant / 3 Reference field, 10 phút, đồng thời (2026-08-23) — chủ dự án hỏi
tiếp: "seed 10tr bản ghi xem còn k, entity khác nhau tenant khác nhau lookup dữ liệu join bảng
nhiều thì sao".** Seed 10 tenant độc lập, mỗi tenant 20 `hr.departments` → 200 `hr.employees` →
1,000,000 `helpdesk.tickets` (10,000,000 ticket tổng — đúng vào ngưỡng `@10M/entity` đã ghim ở
Data Model Strategy/ADR cho table-per-entity), cùng chia sẻ **một** bảng `records` vật lý —
đúng kịch bản ngưỡng đó nói tới. `helpdesk.tickets` thêm field Reference thứ 3 (`reporterId` →
`hr.employees`, cạnh `assigneeId`/`departmentId` đã có) để `hydrate_related_display` chạy 3 lượt
permission-check + batch-fetch + record-condition thay vì 2 — gần hơn với "join nhiều bảng" thật.
Policy ABAC theo phòng ban được tạo lại cho **cả 10 tenant** (context-role grant + record
condition `fromContext.departmentId`, insert thẳng vào `policies` cho nhanh thay vì lặp HTTP). 20
worker đồng thời, mỗi lần lặp chọn ngẫu nhiên một cặp `(tenant_id, departmentId)` trong số 200 cặp
trải trên 10 tenant — traffic thật sự trộn tenant, không phải một tenant lặp lại.

**Kết quả**: 65,756 lệnh gọi `list()` thành công / 600s, **109.5 list()/s trung bình, 0% lỗi**,
nhưng latency suy giảm rõ — p50=57ms, **p95=1306ms, p99=1479ms, max=2140ms** (so với p95=31ms/
p99=34ms ở benchmark 500K/1 tenant). Throughput giảm ~6.8 lần. Cache hit ratio Postgres tụt còn
92.4-94.75% (so với 100% ở benchmark 500K) với ~149,000 block read/s — **root cause giống hệt
benchmark `pgbench` đơn bảng trước đó: `shared_buffers` mặc định 128MB quá nhỏ**, lần này còn rõ
hơn vì working set đã lớn hẳn (`pg_total_relation_size('records')` = 6.5GB, so với ~600MB ở
benchmark trước) — index/dữ liệu bị đẩy ra khỏi buffer cache liên tục dưới tải đồng thời 10
tenant. Đã loại trừ nguyên nhân khác: `ANALYZE` đã chạy tự động sau seed (`last_autoanalyze` mới
hơn thời điểm seed xong, `n_live_tup` khớp số dòng thật — planner không dùng statistics cũ), 0
deadlock mới, không có `crm-server` nào chạy song song gây nhiễu (test gọi thẳng `CrudService`,
xác nhận qua Prometheus target `crm-server` ở trạng thái down trong lúc benchmark). `EXPLAIN
ANALYZE` một truy vấn mẫu cho thấy planner ưu tiên index theo `(tenant_id, entity, created_at)`
để tránh sort thay vì index riêng trên `departmentId` (chọn lọc thấp — mỗi phòng ban chỉ ~0.6%
dòng của tenant) — một composite index `(tenant_id, entity, departmentId, created_at)` sẽ tốt
hơn cho pattern filter+sort này, chưa làm, ghi lại làm việc tiếp theo nếu quy mô 10M/tenant trở
thành thật thay vì benchmark.

**Kết luận (bản đầu, trước verify)**: ngưỡng `@10M/entity` **có ý nghĩa thật, không phải con số
suy diễn** — cùng cấu hình Postgres mặc định (`shared_buffers=128MB`) từng đủ dùng ở 500K giờ đã
là điểm nghẽn rõ rệt. Multi-tenant tự nó (10 tenant chia sẻ 1 bảng) **không** làm chậm thêm — mỗi
tenant vẫn lọc đúng bằng `tenant_id`. 3 Reference field hydrate (thay vì 2) cũng không phải
nguyên nhân chính. Test tool: `crates/metap-crud/tests/crud_service_postgres.rs`'s
`sustained_concurrent_list_across_many_tenants_at_ten_million_rows` (`#[ignore]`d, cần seed
out-of-band 10 tenant/10M row — không chạy trong e2e suite thường, dữ liệu test đã được dọn sau
khi đo).

**Verify fix thật, không dừng ở suy luận (2026-08-23, trigger: chủ dự án phản bác đúng — "hiệu
năng k tốt r, bài test db quá ít logic, chỉ mới datastore mà đã mất hơn 1s", tức là kết luận "chỉ
cần tune, chưa cần table-per-entity" phải được **verify bằng đo lại**, không phải để nguyên như
một giả thuyết chưa kiểm chứng).** Áp 2 fix đã nêu: `shared_buffers` 128MB → **2GB** +
`effective_cache_size` → 4GB (`docker-compose.yml`, không còn revert như benchmark `pgbench`
trước — bake thẳng vào compose vì lần này có dự định giữ lại), và composite index
`(tenant_id, departmentId, created_at DESC)` `WHERE entity='helpdesk.tickets' AND deleted=false`
(`idx_records_helpdesk_tickets_dept_created`, `CREATE INDEX CONCURRENTLY`). Seed lại đúng bộ 10
tenant/10M ticket, chạy lại đúng bài test 10 phút/20 worker.

**Kết quả sau fix**: 179,549 lệnh gọi `list()` / 600s, **299.2 list()/s** (109.5 → 299.2, ~2.7
lần), **p50=66ms, p95=78ms, p99=91ms, max=190ms** (so với p95=1306ms/p99=1479ms/max=2140ms trước
fix — **giảm ~17 lần** ở p95/p99), 0 lỗi. Cache hit ratio Postgres trong suốt cửa sổ đo:
**99.35%** (so với 92.4-94.75% trước fix). `EXPLAIN (ANALYZE, BUFFERS)` trên đúng truy vấn mẫu
(department-scoped, sort theo `created_at`) xác nhận planner giờ dùng
`idx_records_helpdesk_tickets_dept_created` thay vì né sort qua `records_tenant_entity_created_idx`
như trước — 0.448ms execution, 55 buffer hit, toàn bộ từ cache, 0 đọc đĩa.

Một điểm cần nói thẳng: `pg_total_relation_size('records')` ở lần đo này là **13GB** (bảng + toàn
bộ index, kể cả GIN trigram/description áp cho `helpdesk.tickets` — lớn hơn ước tính 6.5GB ban
đầu vì tính thiếu các GIN index đó), trong khi host dev chỉ có **~9.7GB RAM tổng**. `shared_buffers
2GB` vẫn nhỏ hơn nhiều so với working set thật 13GB — kết quả tốt hơn hẳn không phải vì đã cache
toàn bộ working set, mà vì phần **thực sự được truy cập** (composite index cho truy vấn department
+ B-tree cho Reference field) đủ nhỏ để nằm gọn trong 2GB, còn phần ít dùng (GIN trigram cho search
tự do) không cần nằm trong cache cho pattern truy cập của bài test này.

**Kết luận đã verify**: fix ở tầng Postgres (shared_buffers + composite index) **thật sự giải
quyết được vấn đề đo được** ở quy mô 10M/10-tenant hiện tại — không còn là suy luận từ Prometheus,
mà là số đo trước/sau cụ thể. Nhưng điều đó **không có nghĩa table-per-entity không cần làm** —
nó có nghĩa: ở quy mô 10M **hiện tại**, chưa cần, và khi cần thì đã biết chính xác 2 đòn bẩy nào
tác động lớn nhất. Bản thân việc `pg_total_relation_size` đã lên 13GB chỉ với 1 entity 10M-row —
trên một host dev 9.7GB RAM — là tín hiệu thật của đúng vấn đề mà `docs/multi-tenant-platform-
design.md` §3.1 mô tả: "N entity × 10M trong một bảng chung = 100M+ → index phình, autovacuum ác
mộng, một entity nặng làm chậm cả hệ" — tune RAM/index cho **một** entity 10M là khả thi, nhưng
không scale tuyến tính khi có **nhiều** entity cùng ở quy mô đó cùng lúc (mỗi entity nặng lại đòi
thêm working set riêng, cộng dồn trên cùng một bảng vật lý). table-per-entity được nâng độ ưu
tiên: xem `docs/features/04-table-per-entity.md` (trạng thái cập nhật — trigger đã có bằng chứng
thực nghiệm, không còn thuần lý thuyết). Test tool:
`crates/metap-crud/tests/crud_service_postgres.rs`'s
`sustained_concurrent_list_across_many_tenants_at_ten_million_rows`, dữ liệu test đã được dọn sau
khi đo (10,002,200 record + 40 policy xoá đúng phạm vi 10 tenant test, tenant dev cố định
`00000000-0000-0000-0000-000000000001` không bị đụng — verify 2203 record/2 policy còn nguyên).

**Sustained load test thật, 10 phút, đồng thời (2026-08-23, trigger: benchmark trước đó chỉ đơn
luồng, vài request — chủ dự án yêu cầu test nhiều hơn, tối thiểu 10 phút).** `pgbench` (có sẵn
trong image `postgres:16-alpine`) chạy 5 kịch bản trộn theo trọng số (filter chính xác 30%,
substring 20%, FTS 20%, filter theo assignee 20%, ghi 10%) — **20 client đồng thời, 4 thread,
600 giây liên tục**, trên 1 triệu record `bench.issues` (build `--release`, không phải debug
như benchmark trước). Kết quả:

- **106,562 transaction / 600s, 177.6 TPS trung bình, 0% lỗi.**
- Filter chính xác (indexed): 8.9ms; filter assignee (indexed): 6.8ms; ghi: 7.0ms — không đổi
  nhiều so với benchmark đơn luồng trước.
- **Substring/FTS chậm hẳn dưới tải đồng thời: 270ms/269ms** (so với 30-35ms đơn luồng trước đó
  ở 500K row) — phát hiện thật mà benchmark đơn luồng trước **hoàn toàn bỏ sót**.

**Truy nguyên nguyên nhân bằng Prometheus** (không suy đoán): cache hit ratio trong lúc chạy chỉ
**95.68%**, ~69K block đọc/giây từ ngoài `shared_buffers`. `shared_buffers` mặc định của image
Postgres chỉ **128MB**, trong khi working set (bảng `records` 329MB + 2 GIN index 47-48MB mỗi
cái) đã ~600MB — không đủ chứa trong cache của Postgres dưới tải đồng thời. **Verify bằng thực
nghiệm**: tăng `shared_buffers` lên 1GB (`ALTER SYSTEM` + restart, không đụng
`docker-compose.yml`), chạy lại 90 giây cùng kịch bản — cache hit ratio lên **99.98%**, TPS
tổng gần **gấp đôi** (177.6 → 345.3), **FTS giảm 70%** (268ms → 81.5ms), substring giảm 36%
(270ms → 173.7ms). Đã revert `shared_buffers` về mặc định sau khi verify xong (không thay đổi
`docker-compose.yml` — đây là runtime tuning, để lại quyết định "có bake vào compose file
không" cho lúc thật sự cần).

Kiểm tra phụ trong lúc chạy: `pg_stat_database_deadlocks` tăng thêm **0** trong suốt 11 phút
(dùng `increase()`, không phải giá trị cộng dồn — giá trị cộng dồn là 10, dư âm từ lúc tái hiện
deadlock `CREATE INDEX CONCURRENTLY` thủ công trước đó trong phiên, không phải phát sinh mới);
`pg_stat_database_temp_bytes` tăng 0 (không có sort/hash tràn ra đĩa, loại trừ nguyên nhân
`work_mem`). 1,013,833 row đã dọn sau test.

**Kết luận cập nhật**: 500K-1M row/entity vẫn không cần table-per-entity — nhưng khác kết luận
benchmark đơn luồng trước, **`shared_buffers` mặc định của Postgres dev container là điểm nghẽn
thật dưới tải đồng thời thực tế**, không phải kiến trúc bảng chung/JSONB. Đáng làm trước khi
cân nhắc table-per-entity: tune `shared_buffers` (và các tham số bộ nhớ liên quan) cho target
production thật — rẻ hơn nhiều so với tách bảng, và benchmark này cho thấy tác động lớn hơn.

**Fix + capability mới (2026-08-22, trigger: benchmark ở trên chỉ chứng minh nhanh trong MỘT
entity — chủ dự án chỉ ra đúng: nghiệp vụ thật trải dài nhiều entity, list Issue kiểu Jira cần
hiển thị tên Project/Assignee, không chỉ ID thô).** Xác nhận bằng code: `QueryPlanner` không có
JOIN, và `CrudService::list()` không hề enrich field `Reference` (cơ chế enrich duy nhất có sẵn
chỉ chạy cho single-record, chỉ phục vụ permission condition, không lộ ra response). Phân tích
đầy đủ 3 hướng giải quyết (không loại trừ nhau) + implement hướng nhẹ nhất ở
`docs/features/05-cross-entity-relations.md`:

- **Mode 2 (done)**: `CrudService::hydrate_related_display` — batch-hydrate field `Reference`
  có khai báo `refDisplayField` sau khi `list()` filter/mask xong, một query `WHERE id = ANY($1)`
  mỗi entity liên quan cho cả trang (không phải mỗi row). `RecordDto` thêm `related_display`
  (additive, vắng mặt ở mọi response khác). 1 e2e test mới
  (`crates/metap-crud/tests/crud_service_postgres.rs`), verify sống qua HTTP thật (`demo.projects`/
  `demo.tickets` tạo qua low-code — `GET /api/demo.tickets` trả đúng `relatedDisplay.projectId`).
- **Mode 1 (denormalize lúc ghi) và Mode 3 (JOIN thật, cho filter/sort xuyên entity)** — ghi lại
  thiết kế sơ bộ + trigger cụ thể, chưa code. Chi tiết đầy đủ ở feature brief trên.

