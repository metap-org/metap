# Metadata-driven Workflow Engine (State Machine + Workflow composition)

- **Trạng thái:** approved (2026-08-21 — quyết định kiến trúc "tiến hoá `metap-cron`" đã chốt)
- **Người đề xuất:** chủ dự án, 2026-08-21
- **Track sở hữu:** Backend Core
- **Phase roadmap liên quan:** chưa gắn — nếu duyệt, đề xuất là Phase 17

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

## Tiêu chí chấp nhận (cho Increment 1 — phần sẽ code trước)

- Một `workflow_definitions` row với `triggerType: on_transition` khớp `{entity: "crm.customers",
  action: "activate"}` được tạo qua admin API.
- Transition thật (`POST /api/crm.customers/:id/transitions/activate`) khớp trigger đó tự động
  dispatch đúng target đã cấu hình (vd `workflow_transition` sang một entity/action khác), không
  cần polling — latency tương đương cơ chế outbox hiện có.
- Một transition **không** khớp `entity`/`action` nào đã đăng ký thì không dispatch gì cả, không
  lỗi.
- `dispatchMode: "outbox"` sống sót qua một lần scheduler crash giữa lúc nhận event và lúc dispatch
  (at-least-once, cùng bảo chứng `cron-scheduler` đã có) — verify bằng test e2e kiểu
  `cron_job` hiện tại, không phải suy đoán.
- Activity fail có retry-with-backoff (số lần thử + delay cấu hình được), không còn "chỉ ghi
  `status: failed` rồi bỏ đó" như `cron_job_runs` hiện tại.
- Không có `metap-*` crate nào biết tên entity cụ thể (giữ đúng nguyên tắc CLAUDE.md) — consumer
  mới chỉ đọc `entity`/`action` như chuỗi cấu hình, giống `cron-scheduler` đã làm.

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
