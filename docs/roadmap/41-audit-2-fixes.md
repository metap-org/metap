## Phase 41: Fix 12 bug từ AUDIT_2.md (đã verify độc lập từng cái) (2026-08-26)

Chủ dự án đưa `AUDIT_2.md` — báo cáo audit toàn bộ `crates/`+`packages/platform-react` do 6 agent
khác viết, chưa ai kiểm tra lại. Trước khi sửa: 1 fork đọc lại code thật (không tin nội dung
audit) cho 12 finding nghiêm trọng nhất (10 mục "ưu tiên xử lý" + 2 mục HIGH riêng phần workflow)
— **12/12 CONFIRMED**, không có claim nào sai/phóng đại. Chủ dự án duyệt: sửa toàn bộ 12 mục,
theo thứ tự bảo mật → operational/reliability → data integrity → correctness/tooling →
structural. `AUDIT_2.md` không commit (file tham khảo, gitignore-tương-đương với `REVIEWED.md`).
Số thứ tự dưới đây giữ đúng số trong bảng gốc của `AUDIT_2.md`.

**Sự cố ngoài lề**: giữa lúc làm, WSL bị crash vì `target/` lên 96GB (chưa từng dọn suốt session
dài) — mất Postgres/RabbitMQ containers đang chạy (không mất code, mọi thay đổi vẫn nguyên trong
working tree). Đã bổ sung mục trong `CLAUDE.md`: kiểm tra `du -sh target` trước phiên build dài,
`cargo clean` nếu vượt ~40GB.

### 1 — SQL injection qua `field.name` chưa validate (HIGH, bảo mật)

`metap-crud::find_referencing_record` nội suy `field.name` trực tiếp vào SQL identifier lẫn
JSONB path literal; `metap-metadata::compiler::validate()` có `is_safe_ident_segment` cho
`table_name` nhưng chưa từng có tương đương cho field name — field low-code-authored (ghi qua
DB) không bị chặn ký tự. Fix: thêm `is_safe_field_name` (`^[A-Za-z][A-Za-z0-9_]*$`, khớp camelCase
đã dùng thật) vào `validate()`, cùng trust boundary với `table_name`. 2 unit test mới (ký tự nguy
hiểm bị từ chối, camelCase+digit hợp lệ). Verify sống: `crm-server`/`jira-server` boot thật vẫn
qua — mọi field name thật đều camelCase sạch.

### 2 — Basic auth không ràng buộc tenant thật của user (HIGH, bảo mật)

`metap-http::basic_auth` lấy `tenant_id` từ header `X-Tenant-Id` client tự khai, dùng thẳng cho
`RequestContext` + roles lookup sau khi `verify_credentials` (chỉ `WHERE email = $1`, không lọc
tenant) xác thực mật khẩu đúng — không có bước so `user.tenant_id == tenant_id` khai báo. Fix:
thêm assertion tường minh, từ chối (401 chung, không lộ lý do) nếu không khớp — khác
`POST /auth/login` (đã an toàn sẵn vì mint JWT theo `user.tenant_id` thật, không theo giá trị
client khai). Test e2e mới (`jwt_security_postgres.rs`): cùng credential đúng, khai tenant B →
401; khai đúng tenant A → 200. Cả 6 test trong file (bao gồm 5 test cũ) pass.

### 3 — Keyset pagination mất row khi sort theo field NULL (HIGH, data integrity)

Cursor cũ encode giá trị NULL thành chuỗi rỗng `""` — `sort_expr > ''`/`sort_expr = ''` luôn
`unknown` (SQL 3-valued logic) khi `sort_expr` thật là NULL, nên mọi row NULL từ đó về sau biến
mất khỏi trang kế tiếp. Fix: `Cursor::value` đổi `String` → `Option<String>` (phân biệt NULL thật
với chuỗi rỗng thật); `QueryPlanner`'s `cursor_condition` viết lại theo 4 case (ASC/DESC ×
cursor-là-NULL/cursor-có-giá-trị), tận dụng đúng quy ước NULL-ordering của Postgres
(`NULLS LAST`/`NULLS FIRST` theo hướng sort, giờ khai tường minh trong `ORDER BY` thay vì ngầm
định). 2 e2e test mới (ASC + DESC riêng, vì 2 nhánh logic khác hẳn nhau) — trộn row có/không có
`score`, phân trang `limit: 2` hết toàn bộ, xác nhận không mất row nào. 13/13 test pass.

### 4 — Double-JSON-encode fallback cho scalar cast (HIGH, data integrity)

`metap-reconciler::migration`'s `WidenType`/`Coerce` fallback dùng `fallback.to_string()`
(`serde_json::Value::to_string()` render JSON — chuỗi kèm dấu ngoặc kép) cho cast **scalar**
(`::text`/`::date`/...), trong khi cách này chỉ đúng cho cast `::jsonb` (dùng ở `AddField`). Test
cũ né được bug vì chỉ dùng fallback dạng số (`json!(0)`, không có ngoặc kép khi encode). Fix: hàm
riêng `scalar_fallback_text` lấy đúng giá trị raw (mirror `metap-query::condition_to_sql`'s
private `json_scalar_to_text`, không thêm dependency mới cho 1 helper nhỏ). Test e2e mới dùng
fallback dạng chuỗi JSON (`json!("0")`) cast sang `numeric(18,4)` — trước đây sẽ crash toàn bộ
migration (`'"0"'::numeric` invalid syntax), giờ coerce đúng ra `0.0`.

### 5 — `explain_policies` bỏ qua `PolicyEffect` (HIGH, tooling gây hiểu sai)

`PolicyTraceEntry` (backend cho policy-simulator admin) chỉ xét role/condition gate, không đọc
`PolicyRow.effect` — 1 policy `Deny` khớp được coi giống hệt 1 policy `Allow` khớp (`allowed`
tính bằng `entries.any(gate không Failed)`, không phân biệt effect). Enforcement thật
(`evaluate_policies`) đã có deny-overrides-allow từ trước, nên 2 hàm có thể trả verdict khác
nhau cho cùng 1 input — công cụ chẩn đoán "vì sao được phép" báo sai hoàn toàn so với thực tế.
Fix: thêm `effect: PolicyEffect` vào `PolicyTraceEntry` (giờ admin nhìn thấy được), viết lại
`allowed` theo đúng fold deny-overrides-allow của `evaluate_policies` (`any matching Deny` →
false; else `any matching Allow` → true; else false). 3 unit test mới, gồm 1 test cross-check
trực tiếp với `evaluate_policies`'s verdict cho cùng input — xác nhận 2 hàm giờ luôn đồng thuận.

### 6 — `outbox-publisher` không reconnect RabbitMQ (HIGH, operational)

Khác `notification-worker`/`cron-scheduler` (đã có `run_resilient_consumer` từ Phase 37),
`outbox-publisher::run` vẫn nhận `bus: &impl EventBus` đã connect sẵn, không có đường reconnect —
RabbitMQ blip/restart làm mọi `bus.publish()` fail âm thầm mãi mãi (row vẫn đúng logic `mark_failed`
nhưng không bao giờ thật publish lại) tới khi có người restart process tay. Fix: export
`backoff_delay`/`sleep_or_shutdown` từ `metap-infra` (trước đó riêng cho `run_resilient_consumer`,
giờ dùng chung cho cả phía publish) — `run` đổi chữ ký nhận `connect: F` (như 2 consumer kia),
`publish_pending` trả `Ok(bool)` (còn row nào publish fail không) thay vì `Ok(())`, `run` tự
reconnect với backoff khi phát hiện fail. Verify sống thật: chạy `outbox-publisher` thật, insert 1
row → publish OK; `docker restart` RabbitMQ giữa chừng, insert row thứ 2 ngay lúc restart → log
xác nhận fail 1 lần → reconnect → **row thứ 2 vẫn publish thành công** sau khi bus sống lại, không
cần restart process nào.

### 7 — `cron-scheduler::execute()` không idempotent khi redeliver (HIGH, operational)

`cron.job.due` là at-least-once; process crash giữa lúc `dispatch()` xong và `event.ack()` landing
→ redeliver → chạy lại `webhook`/`bulk_query_action` lần 2, không có gì chặn. Fix: thêm
`metap_cron::run_status(pool, run_id)` — `execute()` check `cron_job_runs.status` trước khi
dispatch, skip nếu đã `success`/`failed` (nghĩa là lần chạy này đã hoàn tất trước đó), chỉ log
cảnh báo + return. Không giải quyết race hiếm hơn (2 delivery THẬT SỰ đồng thời trong lúc vẫn
đang chạy — kịch bản khác, hẹp hơn nhiều so với crash-rồi-redeliver mà audit mô tả).

### 8 — Reconciler không tenant-scope DDL/backfill vật lý (MEDIUM-HIGH, structural)

An toàn trước đây chỉ dựa convention "1 bảng `entities.*` = 1 `DedicatedDb` tenant", không code
enforce. DDL (CREATE INDEX/ALTER TABLE) đúng là không thể tenant-scope được (thao tác cấu trúc
bảng, không phải row) — phần thật sự cần sửa là các thao tác **mutate theo row**:
- `backfill::run_batched_update` (dùng chung bởi cả `executor`'s storage-column backfill lẫn
  `migration`'s widen-type transform) — batch-select thêm `AND t.tenant_id = $N`.
- `quarantine::quarantine_bad_rows` — thêm tham số `tenant_id`, scope cả candidate-select lẫn
  delete.
- `migration::preflight` (đọc, rủi ro thấp hơn nhưng cùng gap — số đếm sai nếu convention từng bị
  vi phạm) — cũng thêm `tenant_id`. `preflight_fk_orphans` bỏ qua (không có caller nào trong
  codebase, sửa giờ là speculative).
Test e2e mới: 2 tenant cùng chia sẻ 1 bảng vật lý (mô phỏng đúng kịch bản convention bị vi phạm,
việc audit lo ngại) — backfill scope theo tenant A không đụng tới row của tenant B, dù cùng bảng.

### 9 — Orchestrator's `lease_heartbeat` ghi nhưng không ai đọc (MEDIUM-HIGH, operational)

`claim_due` chỉ chọn `status IN ('pending', 'failed')` — 1 dòng `'running'` bị worker crash giữa
chừng (sau `claim_due`, trước `record_success`/`record_failure`) kẹt vĩnh viễn, không tự phục
hồi. Fix: `claim_due` thêm nhánh reclaim `status = 'running' AND lease_heartbeat < now() - 60
phút` (`LEASE_STALE_AFTER_MINUTES`, hằng số — chưa có cơ chế refresh heartbeat giữa chừng 1
reconcile dài, nên cửa sổ phải đủ rộng để không reclaim nhầm 1 reconcile hợp lệ đang chạy lâu; ghi
rõ trong doc comment là gap còn lại nếu sau này orchestrator thật sự chạy liên tục). Test e2e mới:
claim lần 1 → claim lần 2 ngay (còn fresh) → 0 (không bị cướp) → backdate `lease_heartbeat` 2 giờ
→ claim lần 3 → reclaim thành công.

### 10 — `claim_due_retries` set `started_at` lúc enqueue, không lúc chạy thật (MEDIUM-HIGH, operational)

`started_at` = claim marker, set ngay sau khi enqueue outbox event (chưa chạy thật) — nếu outbox
tenant đó không bao giờ drain, `WHERE started_at IS NULL` loại trừ vĩnh viễn, retry biến mất âm
thầm. Fix: cùng pattern reclaim như #9 — `claim_due_retries` thêm `RETRY_CLAIM_STALE_AFTER` (5
phút, ngắn hơn #9 vì 1 lần dispatch retry vốn phải nhanh, không như 1 reconcile có thể chạy lâu),
`WHERE (started_at IS NULL OR started_at < stale_before)`, log phân biệt "claimed" (mới) vs
"reclaimed" (đã stale). Test e2e mới: claim → không stale → backdate → reclaim thành công.

### Workflow §1 — `transition()` không cross-check `to`/`from` với `enumValues` (HIGH)

`compiler::validate()` chỉ check transition có đủ from/to/action, không đối chiếu với
`stateField`'s `enumValues` — 1 transition định nghĩa sai (typo, vd `"actvie"` thay vì
`"active"`) compile sạch, rồi `CrudService::transition` ghi thẳng giá trị typo vào cột state
record — hỏng vĩnh viễn, không transition nào khác match `from` lại được. Fix: `validate()` thêm
cross-check `initialState`/`terminalStates`/mọi `transition.from`/`to` với `enumValues` của
`stateField` (chỉ áp dụng khi `stateField` thật sự là `Enum` có khai `enumValues` — degrade
thành no-op nếu không, không báo lỗi sai). 3 unit test mới. Verify sống: `crm-server`+`jira-server`
boot thật vẫn qua — mọi workflow thật (kể cả `jira.issues`'s 4-state diamond) đã đúng sẵn.

### Workflow §2 — Guard chỉ có Eq/Neq/In/NotIn, thiếu so sánh số (HIGH)

`ConditionOp` (dùng chung cho guard workflow lẫn record-level read policy) không biểu đạt được
"amount > 10000" — bằng chứng thật: `journal_entry_entity.rs`'s `post` guard phải lách bằng
`Neq 0` ("khác 0"), vô tình chấp nhận cả giá trị **âm**. Fix: thêm `Gt`/`Gte`/`Lt`/`Lte` vào
`ConditionOp` — 2 nơi implement (buộc phải sửa cả 2, exhaustive match không có nhánh `_`):
- `metap-permission::policy_condition` (in-memory, dùng cho guard) — so sánh JSON Number (as f64)
  hoặc JSON String (lexicographic); type mismatch/null/array/object → fail closed (`false`).
- `metap-query::condition_to_sql` (compile ra SQL cho record-level list policy) — cast kiểu chọn
  theo **giá trị đang so sánh** (Number → `::numeric` cả 2 vế, String → text thường), vì crate
  này không có context field-kind như `query_planner`'s `sort_field_expression`; `Uuid`-typed
  field từ chối thẳng (không có ngữ nghĩa "lớn hơn" hợp lý cho UUID ở đây).
- Sửa luôn `journal_entry_entity.rs`: `nonzero()` → `positive()`, `Neq 0` → `Gt 0` — đúng ví dụ
  audit nêu, closes loop thật chứ không chỉ thêm hạ tầng.
- 8 unit test mới (3 ở `policy_condition.rs` bao gồm 1 test tái hiện đúng bug Neq-chấp-nhận-âm, 3
  ở `condition_to_sql.rs` bao gồm cast numeric/text/UUID-reject).

---

`cargo build/fmt --check/clippy --workspace --all-targets -D warnings` sạch xuyên suốt. `cargo
test --workspace` (unit) sạch sau mỗi fix. E2e tests: `metap-metadata` (unit, không cần DB),
`metap-http::jwt_security_postgres` (6/6), `metap-query::query_planner_postgres` (13/13, 2 mới),
`metap-reconciler` (17/17 across 3 file, 3 mới), `metap-cron::cron_store_postgres` (11/11, 1
mới), `metap-permission` (25/25 unit, 4 mới). Boot thật `crm-server`+`jira-server` sau #1 và
Workflow §1 (2 fix có rủi ro breaking-registration cao nhất) — cả 2 vẫn boot sạch, không entity
thật nào bị `validate()` mới chặn nhầm. Live-verify thật (không chỉ đọc code) cho #6 (RabbitMQ
restart giữa chừng) và #8 (2-tenant-1-table).

**Còn lại chưa sửa** (ngoài phạm vi 12 mục đã duyệt lần này, ghi chú để không quên):
- `preflight_fk_orphans` cùng gap tenant-scoping như #8 nhưng chưa có caller nào — không sửa
  (speculative).
- Idempotency ở tầng `dispatch_on_transition_matches`/`dispatch_on_record_event_matches` (redeliver
  event *trigger* gốc, khác với #7's redeliver `cron.job.due`) — MEDIUM, không nằm trong 12 mục.
- Race hẹp hơn #7: 2 delivery thật sự đồng thời trong lúc job đang chạy (chưa `finish_run`) — khác
  kịch bản "crash rồi redeliver" đã sửa.
- Mọi finding MEDIUM/LOW khác trong `AUDIT_2.md` chưa được verify/sửa (metap-http's
  Content-Disposition, metap-auth's OIDC race, packages/platform-react's 5 mục MEDIUM, apps/jira-fe's
  CRITICAL UI-only bug, ...).

Diff chưa commit.
