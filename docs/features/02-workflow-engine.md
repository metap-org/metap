# Metadata-driven Workflow Engine (State Machine + Workflow composition)

- **Trạng thái:** Increment 1 done (2026-08-21); Increment 2/3 vẫn approved, chưa code
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
- Migration mới cho `workflow_runs` (Increment 2) — namespace riêng, không đụng `cron_jobs`/
  `workflow_events` hiện có.
- `EventBus::subscribe` đã tồn tại, dùng lại nguyên trạng — không cần đổi `metap-infra`.

## Rủi ro / phụ thuộc

- Retry-with-backoff cho activity là một thiết kế riêng (đã ghi là gap từ Phase 5, chưa có state
  machine attempt-counter nào) — cần thống nhất shape (`max_attempts`, backoff cố định hay
  exponential) trước khi code, không phải chi tiết implementation tự quyết.
- Track FE hiện đang do người khác phụ trách — admin UI cho trigger `on_transition` không nằm
  trong brief này, cần đồng bộ riêng khi tới lúc.
