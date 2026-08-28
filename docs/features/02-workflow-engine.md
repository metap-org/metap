# Metadata-driven Workflow Engine (State Machine + Workflow composition)

- **Trạng thái:** Increment 1 done (2026-08-21); Increment 2 done (2026-08-28); **Increment 3 done (2026-08-28)** — `wait_event`, chờ 1 domain event khớp pattern (chủ dự án chọn hướng này qua 3 lựa chọn khác khi được hỏi trực tiếp, không suy đoán trước)
- **Người đề xuất:** chủ dự án, 2026-08-21
- **Track sở hữu:** Backend Core
- **Phase roadmap liên quan:** Phase 17

## Vấn đề / động lực

`docs/team-charter.md`'s "Định hướng đang ghi nhận, chưa có trigger" từng ghi nhận tầm nhìn dài
hạn (app kiểu Jira/Confluence dựng bằng metadata, tiến tới durable workflow runtime kiểu
Temporal) nhưng chưa có trigger cụ thể. 2026-08-21, chủ dự án chủ động quyết định ưu tiên hướng
này — đây chính là trigger.

State Machine hiện tại (`EntityWorkflow`/`metap-workflow`) chỉ trả lời "record đang ở state nào,
được phép chuyển sang state nào" — atomic, đủ tốt cho phần đó, không cần thay đổi. Nhưng không có
gì trả lời được "khi record chuyển sang state X thì tự động làm gì tiếp" (gán reviewer, gửi
notification, tạo task con, chờ một sự kiện khác rồi mới tiếp tục) — đúng thứ một app kiểu
Jira/Confluence cần ở tầng automation.

## Rà soát hạ tầng đã có (trước khi thiết kế cái mới)

- **State Machine** (`crates/metap-workflow`) — 3 hàm thuần, không giữ state riêng, không cần
  đổi.
- **`metap-cron`** (Phase 13) — đã có ~70%: `cron_jobs`/`cron_job_runs`, trigger schedule,
  `targetType: workflow_transition|bulk_query_action|webhook`, `dispatchMode: outbox|direct`,
  claim an toàn (`FOR UPDATE SKIP LOCKED`), gọi lại `/api/:entity/...` bằng service JWT (giữ
  entity-agnostic, tái dùng permission/validation/audit có sẵn).
- **`EventBus::subscribe`** (Phase 5) — đã có, `notification-worker` là consumer đầu tiên (chỉ
  log).
- **Outbox pattern** — mọi entity đã emit `<entity>.workflow.transitioned` khi transition, chỉ
  chưa ai dispatch tiếp từ đó.

## Phạm vi

**Trong phạm vi — tăng dần theo 3 increment, mỗi increment tự đứng được, không chờ increment sau:**

- **Increment 1 — Trigger "on state transition"**: mở rộng `cron_jobs`/`metap-cron` tại chỗ —
  thêm cột `trigger_type` (`schedule` mặc định | `on_transition`) + `trigger_config jsonb`
  (`{entity, action}` khi `on_transition`), `cron_expr`/`next_run_at` trở thành `Option`/chỉ bắt
  buộc khi `trigger_type = schedule`. `targetType`/`targetConfig`/`dispatchMode` giữ nguyên
  100%. Một consumer mới trong `cron-scheduler` subscribe `*.workflow.transitioned`, match
  `trigger_config`, dispatch qua đúng cơ chế outbox/direct đã có — không cần bảng "run state"
  mới, tái dùng `cron_job_runs`. **Không rename `cron_jobs`/crate ngay** — tên hơi lệch nghĩa
  ("cron" giờ không chỉ chạy theo lịch) chấp nhận được là nợ kỹ thuật đã ghi, không phải điều
  kiện tiên quyết để bắt đầu code (rename là việc riêng, sau, nếu thấy thật sự cần). Đây là phần
  giá trị cao nhất, chi phí thấp nhất — mở khoá phần lớn nhu cầu automation thực tế (Jira:
  "chuyển sang In Review → gán reviewer + notify") mà không cần multi-step hay wait_event.
- **Increment 2 — Chuỗi activity tuần tự**: `targetConfig` mở rộng thành `steps: [Activity, ...]`
  chạy tuần tự, không có nhánh rẽ/wait. Cần bảng mới `workflow_runs` (id, job_id, tenant_id,
  status, current_step_index, context jsonb) để biết đang chạy tới bước nào — vẫn chưa cần
  "durable pause", vì các bước chạy nối tiếp ngay trong cùng một lần dispatch.
- **Increment 3 — `wait_event`**: một bước có thể tạm dừng chờ một event/topic khác rồi mới chạy
  tiếp — cần thêm bảng index các run đang chờ theo topic, và một consumer khớp event đến với run
  đang chờ để resume. Đây là phần khó nhất (durable execution state qua nhiều lần dispatch/crash)
  — chỉ bắt đầu thiết kế chi tiết khi Increment 1+2 đã chạy thật và lộ ra nhu cầu cụ thể, đúng kỷ
  luật trigger-based (không suy đoán trước).
- Retry-with-backoff cho activity thất bại — gap đã ghi từ Phase 5, đóng cùng lúc với Increment 1
  vì cùng đường dispatch.

**Ngoài phạm vi (rõ ràng, không lẫn vào bất kỳ increment nào ở trên):**
- Durable/replay-able execution kiểu Temporal thật (event sourcing toàn bộ lịch sử run, time-travel
  debug) — level 4-5 trong roadmap 5-level đã ghi ở team-charter, không phải mục tiêu của brief này.
- UI builder cho workflow definition mới — tái dùng đúng pattern `WorkflowBuilder` (guard JSON thô)
  đã có ở Phase 11B, không thiết kế lại; nằm ngoài phạm vi brief này (backend trước, FE track khác
  lo — theo phân công hiện tại của dự án).
- BPM visualize/diagram — đã ghi riêng ở team-charter's "Workflow visualize/BPM nhẹ", tách biệt.
- Cross-module workflow (một workflow chạy qua nhiều service/deployable unit) — trigger riêng
  (Phase 9), chưa xảy ra.

## Tiêu chí chấp nhận (Increment 1) — Đã xong (2026-08-21)

- Một `cron_jobs` row với `triggerType: "on_transition"` khớp `{entity: "crm.customers", action:
  "block"}` được tạo qua `POST /admin/cron-jobs` (`cronExpr`/`nextRunAt` đều `null` — không có
  schedule). Đã verify.
- Transition thật (`POST /api/crm.customers/:id/transitions/block`) khớp trigger đó tự động
  dispatch đúng target đã cấu hình (`workflow_transition` sang một record khác), không cần
  polling. Verify sống qua HTTP + RabbitMQ + Postgres thật (không phải test giả lập): tạo record
  C (draft) + job trigger `on_transition` khi record khác bị `block` → target activate C; tạo
  record B, activate (không khớp trigger, không dispatch) rồi block (khớp trigger) → log
  `cron-scheduler` ghi "cron job triggered on transition" rồi "cron job executed" → record C
  chuyển `draft` → `active` thành công, `cron_job_runs` ghi `status: "success"`. Toàn bộ round
  trip: `emit_transitioned` (mang `tenantId`, field mới thêm) → outbox → `outbox-publisher` →
  RabbitMQ → `cron-scheduler`'s consumer mới trên `#.workflow.transitioned` →
  `dispatch_on_transition_matches` → outbox `cron.job.due` → RabbitMQ → executor → gọi lại HTTP
  API thật của `crm-server`.
- Một transition **không** khớp `entity`/`action` nào đã đăng ký thì không dispatch gì cả, không
  lỗi — verify bằng e2e test (`on_transition_job_does_not_fire_for_a_non_matching_action`) và bằng
  live test ở trên (activate B không kích hoạt job đăng ký cho action `block`).
- Một job đăng ký cho tenant này không bao giờ fire cho tenant khác — verify bằng e2e test
  (`on_transition_job_does_not_fire_for_another_tenant`), khả năng đã có sẵn nhờ `tenantId` giờ
  nằm trong payload `<entity>.workflow.transitioned`.
- `dispatchMode: "outbox"` dùng đúng cùng cơ chế `cron.job.due` đã proven của `cron_jobs` gốc
  (at-least-once) — không cần cơ chế riêng.
- Activity fail có retry-with-backoff: `cron_jobs.maxAttempts`/`retryBackoffSeconds` (mặc định 1
  lần thử/30s, không đổi hành vi job cũ khi không set), backoff nhân đôi mỗi lần
  (`retryBackoffSeconds * 2^(attempt-1)`). Một `finish_run_with_retry` thất bại còn attempt sẽ tự
  ghi một `cron_job_runs` row mới (`attempt+1`, `scheduled_for` = giờ + backoff);
  `cron-scheduler`'s ticker poll thêm `claim_due_retries` mỗi tick để claim khi tới hạn. Verify
  bằng 2 e2e test (`failed_run_with_attempts_remaining_schedules_a_retry_that_claim_due_retries_picks_up`,
  `failed_run_with_no_attempts_remaining_does_not_schedule_a_retry`).
- Không có `metap-*` crate nào biết tên entity cụ thể (giữ đúng nguyên tắc CLAUDE.md) — consumer
  mới (`cron-scheduler::trigger`) chỉ đọc `entity`/`action` như chuỗi cấu hình/payload, giống
  `cron-scheduler::executor` đã làm; verify bằng grep thủ công (không có `use metap_metadata`/
  entity-specific import nào trong `cron-scheduler`/`metap-cron`).

**Migration**: `crates/migrations/0015_cron_jobs_trigger_and_retry.sql` — `cron_jobs` thêm
`trigger_type`/`trigger_config`/`max_attempts`/`retry_backoff_seconds`, `cron_expr`/`next_run_at`
đổi thành nullable; `cron_job_runs` thêm `attempt`.

**Đã đổi thêm ngoài scope ban đầu (bắt buộc để trigger hoạt động đúng multi-tenant)**:
`metap_workflow::emit_transitioned` giờ nhận thêm `tenant_id: Uuid`, ghi vào payload outbox
(`{"tenantId": ..., "recordId": ..., ...}`) — trước đây payload không mang tenant, nên một
consumer subscribe `#.workflow.transitioned` không có cách nào biết event thuộc tenant nào để
scope lookup đúng. Cập nhật 1 call site (`CrudService::transition`) + 1 e2e test.

## Tiêu chí chấp nhận (Increment 2) — Đã xong (2026-08-28)

`TargetType::Steps` thứ 5 (cạnh `workflow_transition`/`bulk_query_action`/`webhook`/`email`) —
`targetConfig: { steps: [{ targetType, targetConfig }, ...] }`, mỗi step là một trong 4
`TargetType` còn lại (không cho lồng `"steps"` — chặn ngay lúc tạo job qua
`metap-http::routes::cron::validate_target_config`, không đợi tới lúc chạy mới phát hiện).
`cron-scheduler::executor::run_steps` chạy tuần tự từng step **trong cùng một lần dispatch** —
đúng scope đã ghi ("chưa cần durable pause vì các bước chạy nối tiếp ngay"), không có nhánh
rẽ/wait, không step nào đọc lại output của step trước (`targetConfig` mỗi step tĩnh, không có
templating).

- Bảng mới `workflow_runs` (`crates/migrations/0023_workflow_runs.sql`): `id`, `tenant_id`,
  `job_id`, `cron_job_run_id` (FK 1-1 vào đúng `cron_job_runs` row của lần dispatch đó, unique
  index), `status` (`running`/`success`/`failed`), `current_step_index`, `total_steps`, `context`
  jsonb (kết quả từng step, key `step_<index>`, thuần audit — không step nào đọc lại), `error`.
  4 hàm store mới (`start_workflow_run`/`advance_workflow_run`/`finish_workflow_run`/
  `fail_workflow_run`) + 1 hàm đọc (`get_workflow_run_by_cron_job_run`).
- Một step fail thì dừng cả chuỗi ngay tại đó — `workflow_runs` ghi `status: failed`,
  `current_step_index` giữ nguyên ở step fail (không advance qua), `error` là lỗi của đúng step
  đó. Cả lần dispatch (`cron_job_runs`) cũng `failed` — **retry-with-backoff của Increment 1 tái
  dùng y nguyên, không sửa gì**: job có `maxAttempts > 1` sẽ tự retry lại từ step 0 (không phải
  resume từ step fail — resume có state là việc của Increment 3's `wait_event`, không phải đây).
- `run_webhook`/`run_email` (tái dùng y hệt cho cả job đơn lẻ lẫn từng step trong chuỗi) đổi chữ
  ký nhận `job_id`/`run_id`/`trigger_entity`/`trigger_record_id` tường minh thay vì đọc từ
  `CronJobDuePayload` — một step dùng đúng `job_id`/`run_id`/trigger context của **cả chuỗi**
  (không phải giả một payload rỗng/nil, cách đó sẽ làm webhook body của step nói dối về
  `jobId`/`runId`).
- `GET /admin/cron-jobs/{jobId}/runs/{runId}/workflow-run` — route admin mới, đọc progress từng
  step cho một lần dispatch cụ thể (song song `GET .../runs` đã có cho `CronJobRun` thô).
- Migration: `crates/migrations/0023_workflow_runs.sql`.

**Verify sống qua HTTP + RabbitMQ + Postgres thật** (đúng độ nghiêm ngặt Increment 1 đã đặt ra —
không dừng ở e2e test giả lập, dù có 4 e2e test mới `crates/metap-cron/tests/
workflow_runs_postgres.rs` phủ store layer): boot thật `crm-server` + `outbox-publisher` +
`cron-scheduler` (Postgres 16 + RabbitMQ 3.12 cài native qua apt trong phiên này thay cho
Docker — Docker Hub bị chặn bởi policy tổ chức lúc chạy phiên này, không phải lỗi cấu hình, xem
ghi chú cuối mục này).

- **Happy path**: job `on_transition` (khi `crm.customers` bị `block`) với `targetType: "steps"`,
  2 step `workflow_transition` (activate record B rồi activate record C) — block record A (nguồn
  trigger) → round trip đầy đủ `emit_transitioned` → outbox → `outbox-publisher` → RabbitMQ →
  `cron-scheduler`'s trigger listener → `run_steps` → cả B lẫn C chuyển `draft` → `active` đúng
  thứ tự. `GET .../workflow-run` trả về `status: success`, `currentStepIndex: 2`,
  `context: {step_0: {...B...}, step_1: {...C...}}`.
- **Failure path**: step 0 cố activate một record đã `active` sẵn (409 `invalid_transition`) →
  chuỗi dừng ngay, step 1's target (record E, đang `draft`) **không** bị đụng tới (verify trực
  tiếp qua `GET /api/crm.customers/{E}` — vẫn `draft`) → `workflow-run` trả `status: failed`,
  `currentStepIndex: 0`, `error` đúng nội dung lỗi HTTP 409 của step 0, `context: {}` (step 0
  chưa từng thành công nên không có gì để ghi).
- **Validation**: `POST /admin/cron-jobs` với `targetType: "steps"` — thiếu `targetConfig.steps`,
  `steps: []`, hoặc một step có `targetType: "steps"` (lồng chuỗi) đều `400 validation_failed`
  đúng thông điệp, chặn trước khi ghi job xuống DB.

**Ghi chú hạ tầng phiên này (không phải một phần thiết kế/code của Increment 2, chỉ là cách môi
trường được dựng để verify)**: Docker daemon của phiên bị dừng giữa chừng và việc pull ảnh
`postgres`/`rabbitmq` mới từ Docker Hub bị chặn bởi policy tổ chức (403 ở tầng proxy khi gọi
`production.cloudfront.docker.com`, không phải lỗi cấu hình proxy — không thử lại theo đúng
hướng dẫn `/root/.ccr/README.md`). Postgres 16 hoá ra đã có sẵn dưới dạng gói hệ thống (apt) với
đúng database `metap`/user `metap` từ trước, chỉ cần `service postgresql start`; RabbitMQ được
cài mới qua `apt-get install rabbitmq-server` (từ kho Ubuntu chính thức, không phải Docker Hub —
không vướng policy) rồi tạo user `metap`/`metap` khớp `.env`. Không ảnh hưởng gì tới code hay kết
quả verify — cùng phiên bản Postgres 16/protocol AMQP y hệt `docker-compose.yml` dùng.

## Tiêu chí chấp nhận (Increment 3 — `wait_event`) — Done (2026-08-28)

Chủ dự án được hỏi trực tiếp giữa 4 hướng ("chờ domain event khớp pattern", "chờ external callback
qua HTTP resume token", "chỉ delay/sleep", "domain event + timeout fallback") — chọn hướng đầu
tiên, phạm vi hẹp nhất còn đúng nghĩa "chờ event": **không có timeout**, một chain chờ mãi tới khi
có event khớp (hoặc bị resume/huỷ thủ công qua tooling vận hành — chưa xây ở đợt này).

- `TargetType::WaitEvent` thứ 6 (cạnh `workflow_transition`/`bulk_query_action`/`webhook`/`email`/
  `steps`) — **chỉ hợp lệ như một step bên trong `steps`**, bị chặn ngay lúc tạo job nếu đặt làm
  `targetType` cấp cao nhất của chính job đó (cùng lý do "chains cannot nest" của `steps` không
  cho lồng chính nó). `targetConfig` của step: `{ entity, action? , event? }` — đúng một trong hai
  `action`/`event` phải có mặt (validate ngay lúc tạo job qua
  `metap-http::routes::cron::validate_wait_event_config`, không đợi lúc chạy mới phát hiện) — mô
  phỏng đúng shape `OnTransitionTriggerConfig`/`OnRecordEventTriggerConfig` đã có.
- Khi chuỗi chạy tới step `wait_event`: `cron-scheduler::executor::run_step_range` (tách ra từ
  `run_steps` cũ để dùng chung được cho cả lần chạy đầu lẫn lần resume) dừng ngay, ghi
  `workflow_runs.status = "waiting"` (kèm `wait_entity`/`wait_action`/`wait_record_event`, cột mới
  — migration `0024_workflow_run_wait_event.sql`) **và** `cron_job_runs.status = "waiting"` (giá
  trị `RunStatus` mới) trong cùng một transaction (`metap_cron::pause_workflow_run`). Không dispatch
  gì thêm cho lần dispatch này — `executor::execute`'s idempotency check giờ coi `"waiting"` giống
  `"success"`/`"failed"`: một message `cron.job.due` bị redeliver cho một run đang chờ sẽ bị bỏ
  qua, không chạy lại từ đầu (sẽ vỡ do unique index `workflow_runs_cron_job_run_idx` nếu không có
  check này).
- **Resume**: `cron-scheduler::trigger`'s listener (đã subscribe rộng mọi
  `<entity>.workflow.transitioned`/`<entity>.record.*` từ Increment 1/Phase 38, không cần consumer
  mới) — mỗi event tới giờ vừa thử khớp job `on_transition`/`on_record_event` mới (như cũ) **vừa**
  thử khớp chain đang `"waiting"` qua `metap_cron::dispatch_on_wait_event_transition_matches`/
  `dispatch_on_wait_event_record_matches` (2 hàm mới, `FOR UPDATE SKIP LOCKED` + tự flip khỏi
  `"waiting"` trong cùng transaction — tự nhiên idempotent, một event khớp lần 2 sẽ không thấy gì
  vì row đã rời `"waiting"`). Khớp thì `cron-scheduler::executor::resume_steps` (hàm mới) chạy tiếp
  từ `current_step_index + 1`, dùng lại `run_step_range` — nếu gặp `wait_event` thứ hai thì lại
  pause tiếp, không có gì đặc biệt phải xử lý.
- **Quyết định an toàn quan trọng**: việc "fire job mới" (query cũ) và việc "resume chain đang chờ"
  (query mới) **không được phép cùng quyết định retry/nack của 1 message** — nếu fire thành công
  (đã commit job mới) mà resume thất bại rồi tự động retry cả message, sẽ gọi lại
  `dispatch_on_transition_matches` lần 2 và bắn trùng job mới. Fix: chỉ retry-cả-message khi chính
  fire fail (y hệt hành vi trước Increment 3); resume chỉ chạy **sau khi** biết fire đã `Ok`, và
  resume fail chỉ log cảnh báo (best-effort, chain vẫn `"waiting"`, có thể được resume bởi 1 event
  khớp sau này) — không nack lại.
- Fail sau khi resume (step nào đó sau wait_event lỗi) đi qua đúng `finish_run_with_retry` sẵn có
  — retry lại từ step 0 (không phải resume tiếp từ chỗ đang dừng), y hệt chính sách "no
  partial-resume" của Increment 2. `resume_steps` tự dựng lại một `CronJobDuePayload` từ job +
  `cron_job_runs` row (không có sẵn payload gốc ở thời điểm resume — cùng gap đã ghi nhận ở
  `dispatch_claimed`'s doc comment cho một lần retry thường) — `triggerRecordId`/`triggerEntity`
  của các step sau resume là của **event vừa resume** (không phải trigger gốc của cả chain).
- `GET /admin/cron-jobs/{jobId}/runs/{runId}/workflow-run` không đổi route — `status: "waiting"`
  cộng 3 field mới (`waitEntity`/`waitAction`/`waitRecordEvent`) tự động lộ ra qua route có sẵn.

**Migration**: `crates/migrations/0024_workflow_run_wait_event.sql` — `workflow_runs` thêm 3 cột
nullable + 2 partial index (`WHERE status = 'waiting'`) cho 2 dạng match.

**Verify**: `cargo build/clippy -D warnings/fmt --check` sạch toàn workspace, `cargo test --workspace`
(unit, không cần DB) pass. 4 e2e test (`crates/metap-cron/tests/wait_event_postgres.rs`, `#[ignore]`d,
chạy thật trên Postgres 16 qua `docker compose up -d postgres rabbitmq`) phủ store layer:
`pause_workflow_run` ghi đúng cả 2 bảng; một transition khớp chỉ resume đúng chain khớp, không đụng
chain khác đang chờ action khác; một record-event khớp resume đúng, và khớp lần 2 (chain đã rời
`"waiting"`) trả về rỗng; chain của tenant A không bao giờ bị resume bởi event khớp của tenant B.
Bug thật tìm thấy khi viết test (không phải bug ở code sản phẩm): helper test tạo 2 job cùng
`trigger_config` (`{entity: crm.customers, action: "block"}`) trong cùng tenant — fire job thứ 2
vô tình khớp luôn job thứ 1 (`dispatch_on_transition_matches` khớp mọi job enabled cùng
entity/action, không chỉ job vừa tạo), làm `claimed` đếm sai. Fix bằng cách cho mỗi job trong test
dùng `fire_action` riêng.

**Verify sống qua HTTP + RabbitMQ + Postgres thật (2026-08-28, sau khi có Docker)**: boot thật
`crm-server` + `outbox-publisher` + `cron-scheduler` (3 tiến trình riêng, không phải test giả
lập). Job `on_transition` (khi `crm.customers` bị `block`) với `targetType: "steps"`, 2 step —
`wait_event {entity: crm.customers, action: activate}` rồi `workflow_transition` activate record
B thành `blocked`. Kịch bản: activate + block record A (nguồn trigger) → job fire →
`run_step_range` dừng ngay ở step 0, log "workflow chain paused at wait_event step" →
`GET .../workflow-run` xác nhận `status: "waiting"`, `waitEntity: "crm.customers"`,
`waitAction: "activate"`, `currentStepIndex: 0` → activate record C (event khớp, entity/action
đúng) → `cron-scheduler::trigger` resume đúng chain, log "cron job chain resumed and completed" →
`GET .../workflow-run` lần 2: `status: "success"`, `currentStepIndex: 2`, `context.step_1` chứa
đúng kết quả transition, `finishedAt` đã set → `GET /api/crm.customers/{B}` xác nhận `status:
"blocked"` thật (không phải suy đoán từ log). `cron_job_runs.status` cũng `success` đúng (không
kẹt ở `"waiting"`). Phát hiện phụ (không phải bug, hành vi đúng thiết kế): step `block` B tự nó
cũng khớp `trigger_config` của chính job (cả hai đều `entity: crm.customers, action: block`) nên
tự fire thêm 1 lần chạy mới của job, lại pause ở `wait_event` tiếp — xác nhận đúng tài liệu đã ghi
("resume không được phép cùng quyết định retry/nack với fire", 2 việc độc lập nhau).

**Ngoài phạm vi (cố ý, đúng lựa chọn hẹp nhất)**: timeout — một chain `wait_event` không có event
khớp sẽ chờ vô thời hạn, không tự fail; external callback resume qua HTTP (đã cân nhắc, không
chọn); admin/ops tooling để resume/huỷ thủ công một chain đang `"waiting"` khi không event nào tới.

## Ranh giới kiến trúc bị đụng tới

- **Quyết định 2026-08-21: tiến hoá `metap-cron`/`cron-scheduler` tại chỗ**, không tách crate
  mới. So sánh trade-off đã cân nhắc:
  - Crate mới (`metap-orchestration`) được: `metap-cron` không bị đụng, zero-risk, không cần ADR
    ngay, rollback dễ nếu bỏ giữa chừng. Mất: phải viết lại gần như y hệt claim-safe polling +
    outbox dispatch + retry + audit lần thứ hai (2 bản phải giữ đồng bộ khi sửa bug), 2 process +
    2 bộ admin route + 2 audit table cho người vận hành phải nhớ.
  - Tiến hoá `metap-cron` được: tái dùng ngay ~70% hạ tầng đã proven, một hệ thống duy nhất cho
    "trigger → dispatch activity". Mất: tên `cron_jobs`/`metap-cron` không còn khớp nghĩa 100%
    (chấp nhận là nợ kỹ thuật, xem Increment 1 ở trên — không rename ngay nên không cần ADR).
- Migration mới cho `workflow_runs` (Increment 2, đã áp dụng) — namespace riêng, không đụng
  `cron_jobs`/`workflow_events` hiện có; chỉ FK 1-1 vào `cron_job_runs.id`.
- `EventBus::subscribe` đã tồn tại, dùng lại nguyên trạng — không cần đổi `metap-infra`.

## Rủi ro / phụ thuộc

- Retry-with-backoff cho activity là một thiết kế riêng (đã ghi là gap từ Phase 5, chưa có state
  machine attempt-counter nào) — cần thống nhất shape (`max_attempts`, backoff cố định hay
  exponential) trước khi code, không phải chi tiết implementation tự quyết.
- Track FE hiện đang do người khác phụ trách — admin UI cho trigger `on_transition` không nằm
  trong brief này, cần đồng bộ riêng khi tới lúc.
