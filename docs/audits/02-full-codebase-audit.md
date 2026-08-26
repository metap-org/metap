> **Trạng thái (2026-08-26):** 12 finding nghiêm trọng nhất trong bảng "Tóm tắt ưu tiên xử lý" bên
> dưới + 2 mục HIGH phần workflow đã được verify độc lập (fork đọc lại code thật, không tin nội
> dung audit) rồi fix — chi tiết từng mục, cách fix, test/verify sống ở
> [`docs/roadmap/41-audit-2-fixes.md`](../roadmap/41-audit-2-fixes.md). Phần còn lại của file này
> (mọi finding MEDIUM/LOW, phụ lục `apps/*`) **chưa được verify hay sửa** — vẫn còn nguyên giá trị
> tham khảo cho việc tiếp theo, danh sách còn lại được liệt lại ở cuối tài liệu Phase 41 nêu trên.

Review toàn bộ crates/ (backend Rust) và packages/platform-react (frontend library) ở trạng thái hiện tại (không phải diff — git status sạch, không có thay đổi chưa commit). apps/* (crm-server, jira-server, crm-fe, jira-fe) chỉ là demo/sample consumer nên được review ở mức nông hơn, gộp vào phụ lục cuối file.

Phương pháp: 6 agent độc lập đọc toàn bộ source (không skim) theo nhóm crate, tự đối chiếu với CLAUDE.md's các invariant đã công bố (tenant scoping, không SQL string-interpolation từ input client, không role caching, layering routes -> CrudService -> platform core -> Postgres/EventBus). Không sửa code — chỉ report.

## Tóm tắt ưu tiên xử lý

| # | Mức độ | Vị trí | Vấn đề |
|---|---|---|---|
| 1 | **HIGH** | metap-crud/src/crud_service.rs:1194-1201 | SQL injection qua tên field không được validate trong find_referencing_record |
| 2 | **HIGH** | metap-peripherals/src/auth.rs + metap-http/src/auth.rs:190-249 | Basic auth không ràng buộc user.tenant_id với tenant client tự khai báo qua header X-Tenant-Id |
| 3 | **HIGH** | metap-query/src/query_planner.rs + metap-crud/src/crud_service.rs:1464-1476 | Keyset pagination mất row âm thầm khi sort theo field nullable có nhiều NULL |
| 4 | **HIGH** | metap-reconciler/src/migration.rs:212-227 | WidenType/Coerce fallback double-JSON-encode giá trị cho cột scalar — data corruption âm thầm hoặc lỗi runtime |
| 5 | **HIGH** | metap-permission/src/policy_explainer.rs:36-113 | explain_policies bỏ qua PolicyEffect (Allow/Deny) — công cụ chẩn đoán "vì sao được phép" có thể báo sai hoàn toàn với enforcement thật |
| 6 | **HIGH** | outbox-publisher/src/lib.rs:36-70 | Không reconnect RabbitMQ sau khi mất kết nối — phá vỡ đảm bảo tin cậy của outbox pattern, mọi consumer khác đã được vá (Phase 37) riêng crate này thì chưa |
| 7 | **HIGH** | cron-scheduler/src/executor.rs:76-95 | Không có idempotency check khi message cron.job.due bị redeliver — webhook/bulk_query_action có thể thực thi 2 lần |
| 8 | **MEDIUM-HIGH** | metap-reconciler (compile/introspect/diff/executor/backfill) | Không có tenant scoping ở tầng DDL/backfill vật lý — an toàn chỉ nhờ quy ước "mọi bảng entities.* thuộc 1 DedicatedDb tenant", không được code enforce |
| 9 | **MEDIUM-HIGH** | metap-reconciler/src/orchestrator.rs | Không có cơ chế reclaim khi worker chết giữa chừng — deployment kẹt 'running' vĩnh viễn, cột lease_heartbeat được ghi nhưng không ai đọc |
| 10 | **MEDIUM-HIGH** | metap-cron/src/store.rs:459-491 | claim_due_retries đánh dấu started_at ngay khi enqueue outbox — nếu outbox không drain, retry kẹt vĩnh viễn, không timeout/reclaim |

Chi tiết đầy đủ theo từng crate ở các phần dưới.

---

## metap-crud

**[HIGH] SQL injection qua field name chưa được validate — crud_service.rs:1194-1201** (nhánh dedicated-table của find_referencing_record, chạy trên mọi DELETE /api/{entity}/{id}):

rust
if r.has_real_column {
    clauses.push(format!("\"{}\" = ${}::uuid", r.ref_field, param_idx));
} else {
    clauses.push(format!("data ->> '{}' = ${}", r.ref_field, param_idx));
}

r.ref_field là EntityField.name, được nội suy trực tiếp vào cả SQL identifier lẫn JSONB path literal. metap-metadata::compiler validate table_name bằng regex an toàn (có hẳn comment giải thích lý do: "interpolated directly into SQL") nhưng **không có validate tương đương cho field.name** — chỉ kiểm tra trùng lặp/khả năng resolve reference, không kiểm tra ký tự. Vì metadata entity có thể được tạo qua low-code (Phase 11, ghi vào DB), bất kỳ actor nào tạo/sửa được field definition (ví dụ tên field x") OR 1=1 --) có thể chèn SQL tuỳ ý, thực thi mỗi khi có record tham chiếu bị xoá. Đây là điểm duy nhất trong metap-crud nội suy field name không qua bind parameter.

**[MEDIUM] Field không khai báo lọt qua validate, ghi thẳng vào data** — validation.rs's test unknown_extra_fields_pass_through_unvalidated tự thừa nhận: bất kỳ key JSON nào không khai báo trên entity được copy nguyên vào record, không kiểm tra kiểu/hình dạng. assert_writable_fields (crud_service.rs:483-487, 623-627) chỉ chạy trên raw_data.keys() *trước* bước pass-through này — nếu policy engine không deny-by-default field lạ, client có thể nhét dữ liệu tuỳ ý vượt qua field-level allowlist.

**[LOW] Guard evaluation ở transition() dùng data thô, không đi qua enrich_record_for_actions** — run_guard (dòng 807, 1448) đánh giá guard trên existing_data trực tiếp, trong khi permission condition ở cùng method (dòng 766-778) có bản "enriched" (resolve relation 1-hop). Nếu một workflow guard cần thuộc tính cross-record như permission policy vẫn làm được, nó sẽ fail âm thầm thay vì resolve đúng.

Phần còn lại của metap-crud (transaction boundary, optimistic locking, reference-integrity delete guard, mask_record_for_read) đã verify đúng.

## metap-http

**[MEDIUM] cron.rs/preferences.rs bỏ qua Router, đi thẳng AppState.pool** — mọi handler trong routes/cron.rs và preferences.rs gọi metap_cron::*/metap_peripherals::* trên state.pool thay vì router.begin(tenant_id). Với tenant DedicatedDb, cron job CRUD/preferences âm thầm chạy trên DB nền tảng thay vì DB riêng của tenant. **Đã được ghi nhận là gap đã biết trong CLAUDE.md**, nhưng vẫn là bug isolation đang sống thật.

**[MEDIUM] Content-Disposition header không escape filename** — routes/attachments.rs:246-249: format!("attachment; filename=\"{}\"", record.filename) với filename lấy thẳng từ multipart upload, không escape dấu ". HeaderValue chặn được CR/LF (không response-splitting được) nhưng dấu " vẫn lọt, có thể chèn thêm tham số vào header.

**[LOW/MEDIUM] Không có rate-limit riêng cho POST /auth/login** — chỉ dùng chung governor limiter toàn cục (300 burst/5 req-s), không có lockout/backoff riêng cho auth — 300 lần thử mật khẩu/phút từ 1 IP vẫn khả thi cho credential-stuffing dù argon2id làm chậm mỗi lần thử.

**[LOW] Duplicate boilerplate scoped_tenant/router.begin lặp lại ~25+ lần** across admin.rs, cron.rs, dashboards.rs, attachments.rs, workflow_events.rs, users.rs, preferences.rs — nên có 1 helper AppState::begin_tenant_tx(), cũng sẽ giúp tránh lặp lại bug kiểu cron.rs/preferences.rs ở trên.

Phần còn lại (JWT validation, CORS, limit 1-200 trên mọi list endpoint, OIDC one-time-use CSRF/nonce cache, không route nào import sqlx/lapin trực tiếp) đã verify sạch.

## metap-query

**[HIGH] Keyset pagination mất row âm thầm khi sort theo field nullable** — sort_field_value (crud_service.rs) trả String::new() khi field sort là null ở row cuối trang. Cursor này khi bind vào điều kiện trang sau (sort_expr > $value OR (sort_expr = $value AND id > $id)) gặp sort_expr là NULL thật ở trang kế tiếp — logic 3 giá trị của SQL (NULL > '', NULL = '' đều unknown) khiến các row đó **không bao giờ khớp điều kiện nào, biến mất khỏi kết quả** mà không có lỗi. Ví dụ cụ thể: sort jira.issues theo dueDate tăng dần, nhiều issue chưa set dueDate hơn 1 trang → trang thứ 2 trở đi mất dữ liệu âm thầm.

**[LOW/MEDIUM] plan_list không tự enforce floor/ceiling cho limit** — chỉ clamp chặn trên theo list_view.max_limit, không chặn dưới. limit âm/0 sẽ lọt vào LIMIT {limit+1} sinh SQL sai. Validate 1..=200 hiện chỉ nằm ở metap-http/routes/records.rs, không phải ở QueryPlanner — vi phạm invariant "mọi list đều có max limit" mà chính doc comment của crate này công bố; một caller khác của plan_list trong tương lai (route mới, cron target) sẽ không được bảo vệ.

**[LOW] Substring-search (ILIKE) không validate field.kind trước khi build SQL** — chỉ check field.searchable, không như jql.rs's validate_op (giới hạn Contains cho JqlFieldType::Text). Field Number/Reference/Boolean được đánh dấu searchable: true sẽ sinh lỗi Postgres runtime thay vì 400 sạch.

Không tìm thấy injection risk khác — mọi giá trị client đều qua ParamBuilder/BindValue, field name interpolate làm identifier luôn được check trước.

## metap-workflow

Sạch — không tìm thấy bug correctness/security. find_transition, run_guard, các hàm emit_* đều parameterized đúng, không string interpolation.

## metap-reconciler

**[HIGH] WidenType/Coerce fallback double-JSON-encode giá trị scalar — migration.rs:212-227 (dòng 220)**:

rust
fallback = quote_literal(&fallback.to_string()),

fallback: &serde_json::Value. Value::to_string() trả JSON text (vd Value::String("N/A") → chuỗi "N/A" **kèm dấu ngoặc kép**), đúng khi target là cột jsonb (dùng ở AddField, dòng 206-207) nhưng bị tái sử dụng sai cho WidenType's coerce fallback — target ở đây là scalar cast (::text/::uuid/::date/::timestamptz). Widen sang text với fallback string sẽ **lưu thẳng chuỗi kèm dấu ngoặc kép vào mọi row bị coerce** — data corruption âm thầm. Widen sang date/uuid/timestamptz sẽ crash cả batch migration ('"2020-01-01"'::date invalid). metap-query::condition_to_sql's json_scalar_to_text xử lý đúng case này nhưng không được tái dùng ở đây.

**[MEDIUM-HIGH] Không có tenant scoping ở tầng DDL/backfill vật lý** — mọi DDL (build_sql, build_create_index_sql, build_add_fk_sql) và mọi batched mutation (backfill::run_batched_update, migration::apply_sql, quarantine::quarantine_bad_rows) chạy trên toàn bảng vật lý, không WHERE tenant_id = .... An toàn hiện tại chỉ nhờ quy ước "bảng entities.* luôn thuộc 1 DedicatedDb tenant" — **không được code trong crate này enforce**. Nếu reconcile() từng bị gọi cho tenant Schema (shared-DB) trên cùng qualified_table_name_for(...), bảng sẽ thực sự dùng chung nhiều tenant và mọi backfill/migration/quarantine sẽ đọc/ghi/leak xuyên tenant mà không ai chặn. Nên có assertion tường minh trong reconcile()/executor::execute thay vì dựa hoàn toàn vào convention ghi trong CLAUDE.md.

**[MEDIUM-HIGH] Orchestrator không có reclaim path khi worker chết giữa chừng — orchestrator.rs** — claim_due set status='running' + lease_heartbeat=now(), nhưng lease_heartbeat **không có nơi nào đọc lại**. claim_due's claim query chỉ chọn status IN ('pending','failed') — loại trừ vĩnh viễn row 'running'. Worker crash giữa chừng (trước record_success/record_failure) khiến deployment đó kẹt 'running' mãi mãi, không tự phục hồi.

**[LOW, defense-in-depth] field.name interpolate không validate vào DDL** — compile.rs:109,159,204,211 build index expression/column identifier bằng string interpolation trực tiếp field.name. An toàn hiện tại chỉ vì mọi EntityDefinition tới crate này đều hard-code trong crm-server/jira-server (đã verify: low-code path luôn hard-code table_name: "records", không bao giờ chạm compile() này). Nếu table-per-entity từng mở cho tenant tự định nghĩa field (hướng tương lai đã ghi trong roadmap low-code), đây là SQL-injection vector sống ngay mà không cần đổi code trong crate này.

Phần còn lại (diff()'s topo-sort/rename/backfill-resume, advisory-lock discipline, backfill::run_batched_update's checkpoint atomic) đã verify đúng và test tốt.

## metap-permission

**[HIGH] explain_policies bỏ qua PolicyEffect — policy_explainer.rs:36-113 (dòng 105-108)**:

rust
let allowed = entries.iter().any(|entry| entry.role_gate != Gate::Failed && entry.condition_gate != Gate::Failed);

Không đọc PolicyRow.effect (Allow/Deny) — verdict coi 1 Deny policy khớp giống hệt 1 Allow policy khớp. Ví dụ: entity có Allow không điều kiện cho role sales, và Deny có điều kiện region == "apac" cho cùng role. Với caller apac/sales thật: enforcement thật (evaluate_policies, deny-overrides-allow) trả về Deny → 403 đúng. Nhưng PermissionService::explain (backend cho policy-simulator admin) báo allowed: true, và PolicyTraceEntry **không có field effect để admin nhìn thấy** Deny policy đã được xét. Công cụ chẩn đoán này hoàn toàn có thể đánh lừa admin. deny_overrides_allow được thêm 2026-08-21 nhưng explain_policies chưa bao giờ được cập nhật theo.

**[LOW/MEDIUM] RequestContext's context_attributes có thể trùng tên với field cố định** — doc comment tự cảnh báo tránh đặt tên tenantId/userId/roles/functionId nhưng không có enforcement. Nếu AUTH_CONTEXT_ENTITY có field tên roles, to_value()'s flattened view sẽ đè lên mảng roles thật cho mọi PolicyCondition::FromContext, trong khi is_admin()/role-gate check thật (đọc struct field) không bị ảnh hưởng — policy author không có cách nào biết điều này xảy ra. Nên reject tên trùng ngay lúc startup.

Phần còn lại (deny_overrides_allow trong evaluate_policies, admin short-circuit nhất quán, cache invalidate đúng on create/delete policy, scoped_tenant fail loud) đã verify sạch.

## metap-control

**[MEDIUM] Không rollback khi provisioning fail giữa chừng — provisioning.rs:39-103** — cả provision_schema_tenant/provision_dedicated_db_tenant ghi control.tenants với status: "active" **trước** khi chạy seed_local_auth_config/create_user/assign_role. Nếu 1 bước sau đó fail, tenant bị kẹt: đã active/routable (Router::begin mở transaction bình thường) nhưng không có auth config/admin user — khoá chết không tự phục hồi, và chạy lại provisioning cho cùng tenant_id sẽ fail vì PostgresTenantRegistry::provision's INSERT không có ON CONFLICT.

**[MEDIUM, latent] pool_for's nhánh Schema dựa vào invariant không được enforce — router.rs:189-212** — trả shared_pool.clone() không SET LOCAL search_path, đúng chỉ vì schema_name luôn là "public" trong thực tế. Nhưng validate_schema_name đã chấp nhận pattern t_[a-z0-9]+ — nếu tương lai có tenant Schema thật không phải public, pool_for sẽ âm thầm chạy sai schema thay vì fail loud như begin() làm.

Phần còn lại (validate_schema_name whitelist test cả input SQL-injection-shape, PostgresPolicyStore double-scope tenant, VaultStore renewal/lock discipline, EnvStore/DbCreds không leak secret qua Debug) đã verify sạch.

## metap-metadata

**[LOW] .expect() sống trên mọi request /metadata/entities — registry.rs:141** — to_metadata gọi compiler::hash(entity).expect(...). Lý do "JSON không có NaN/Infinity" hợp lý hôm nay, nhưng đây vẫn là panic path phủ toàn bộ endpoint, nằm sau dữ liệu merge từ low-code entity DB-authored (nằm ngoài tầm kiểm soát của crate này). Nên trả typed error thay vì panic.

Phần còn lại (compiler::validate's table_name whitelist, hash() determinism có test regression, register/merge_with reject duplicate không partial-mutate) sạch.

## metap-infra

Sạch, ít rủi ro nhất. Ghi chú thiết kế nhỏ: db.rs:12 và metap-control/router.rs:114-115 hardcode max_connections(5), không cấu hình được qua AppConfig — nên expose config trước khi có target production load thật. event_bus.rs's DLQ/backoff/reconnect (run_resilient_consumer) đã verify đúng (backoff exponential capped 30s, đóng connection cũ trước reconnect, BasicNackOptions::default() đúng là requeue: false).

## metap-peripherals

Sạch — tenant scoping đầy đủ, ON CONFLICT DO NOTHING cho role grant race-free.

**[LOW] Doc comment lỗi thời — role_assignment.rs:1-6** — claim metap-http's AuthContext có "bản copy cũ" của get_roles_for_user, thực tế không còn tồn tại trong code — comment mô tả sai 1 tech debt đã được giải quyết.

**[Design note]** users.email có unique index toàn cục (không kèm tenant_id, crates/migrations/0009_users.sql:10) — cùng 1 email không thể là user ở 2 tenant khác nhau. Cần xác nhận đây là chủ đích cho multi-tenant SaaS.

## metap-storage

Sạch — mọi method đều qua scoped_key → validate_key trước khi chạm backend, credentials là SecretString không leak qua log.

## metap-cache

**[MEDIUM] TTL sub-second bị floor lên 1s — redis_cache.rs:68** (self.ttl.as_secs().max(1)) — Cache trait không document/enforce TTL tối thiểu, footgun tiềm ẩn cho caller tương lai cần expire gần-tức-thời.

Đã verify: không có role/user_roles nào lọt vào cache — chỉ policy rows (load_snapshot) được cache, invalidate đúng khi create/delete policy.

## metap-cron

**[MEDIUM] dispatch_on_transition_matches/dispatch_on_record_event_matches không có idempotency/claim marker — store.rs:345-392** — khác claim_due_jobs/claim_due_retries (có FOR UPDATE SKIP LOCKED), 2 hàm này chỉ SELECT thường, được gọi mỗi khi consume 1 message RabbitMQ, ack sau khi trả Ok. Process chết giữa lúc transaction commit và ack landing → message redeliver → chạy lại từ đầu → thêm 1 dòng cron_job_runs + fire lại target action. WorkflowTransition tự heal (version conflict → 409) nhưng **Webhook/BulkQueryAction thì không** — redelivery gọi trùng webhook ngoài hoặc áp dụng lại bulk action lên toàn bộ record đã match.

**[MEDIUM-HIGH] claim_due_retries có thể kẹt vĩnh viễn nếu outbox không drain — store.rs:459-491** — started_at được set làm claim marker **ngay khi enqueue outbox event**, không phải lúc job thực sự chạy. Nếu outbox của tenant đó không bao giờ được drain (dedicated-DB tenant trỏ nhầm pool, RabbitMQ down dài hạn), started_at đã non-null nên WHERE started_at IS NULL không bao giờ chọn lại được — retry biến mất âm thầm, không timeout/reclaim, admin không biết.

**[LOW/MEDIUM] Không validate retry_backoff_seconds >= 0 trong crate — store.rs:59-63** — 0 làm backoff mất tác dụng, số âm khiến scheduled_for nằm trong quá khứ → due ngay lập tức → kết hợp max_attempts cao thành retry storm. Validate (nếu có) chỉ nằm ở tầng HTTP admin-API, crate này không tự vệ.

Đã verify đúng: claim_due_jobs (FOR UPDATE SKIP LOCKED + advance next_run_at cùng transaction), cron-expression/DST/leap-year math (thư viện cron 0.17.0 xử lý đúng).

## outbox-publisher

**[HIGH] Không reconnect RabbitMQ sau khi mất kết nối — lib.rs:36-70, main.rs:23,27** — connect 1 lần lúc start, không bao giờ reconnect. RabbitMQ restart/network blip → mọi bus.publish() sau đó fail âm thầm mãi mãi theo chu kỳ poll_ms, không backoff, không tự phục hồi — phá vỡ chính lời hứa reliability của outbox pattern cho tới khi có người restart process tay. Đây chính là pattern mà run_resilient_consumer (Phase 37, 2026-08-25) đã fix cho notification-worker/cron-scheduler — **outbox-publisher chưa được migrate theo, giờ là "kẻ lạc loài" duy nhất còn thiếu.**

**[LOW]** Không có max-attempts/poison-row handling — 1 row lỗi vĩnh viễn bị retry mãi, do query ORDER BY created_at LIMIT batch_size nên có thể chặn event mới hơn dưới tải lỗi kéo dài.

Đã verify: bug pool sai cho dedicated-DB tenant (từng phát hiện 2026-08-24) **đã fix đúng** — apps/jira-server/main.rs:200-222 dùng router.pool_for(jira_tenant_id).

## notification-worker

Sạch — wrapper mỏng đúng nghĩa quanh run_resilient_consumer, không panic trên payload lỗi.

## cron-scheduler

**[HIGH] executor.rs:76-95's execute() không check cron_job_runs.status trước khi chạy lại** — cùng họ bug với metap-cron's finding ở trên: redeliver message cron.job.due sau khi run đã thành công (cửa sổ crash hẹp giữa lúc execute() xong và event.ack() landing) → chạy lại lần 2, kể cả Webhook target, không có guard nào chặn.

**[MEDIUM]** finish_run_with_retry (metap-cron/store.rs:584-620) trong kịch bản redelivery trên, nếu lần chạy lại cũng fail, sẽ chèn thêm 1 dòng retry sibling cho cùng attempt (không có unique constraint (job_id, attempt)) → claim_due_retries sau đó fire trùng 2 retry cho 1 lỗi logic.

**[LOW/MEDIUM]** run_webhook (executor.rs:229-252) nhận url/headers/method tuỳ ý từ target_config do tenant khai báo, không allowlist — SSRF surface cho ai tạo được cron job kiểu webhook.

Đã verify đúng: trigger.rs catch-all "#", classify_topic phân loại đúng, unmatched routing key ack im lặng (không nack/DLQ), payload lỗi hình dạng bị nack requeue:false (dead-letter) đúng thiết kế; cả run_executor và run_trigger_listener đều route qua run_resilient_consumer, không còn loop tự viết riêng.

## db-migrate

Sạch — không hardcode credential, sqlx::migrate! tự quản lý advisory lock + transaction per-migration.

## dev-tools

Đã verify parity thật (không chỉ theo doc comment): mint_token/create_user/seed_admin/provision_tenant/bootstrap_platform_admin gọi đúng cùng hàm metap_peripherals/metap_control mà HTTP path dùng.

**[MEDIUM] Password/DSN truyền qua positional CLI arg dạng plaintext** — create-user, provision-tenant ... dedicated_db's dedicatedDatabaseUrl, vault-put-dsn's dsn đều là arg thường, lộ qua ps//proc/<pid>/cmdline trên host dùng chung, và lưu lại trong shell history. pnpm provision:tenant/pnpm bootstrap:platform-admin wrapper cho thấy đây không chỉ dùng throwaway local.

**[LOW-MEDIUM]** provision_tenant's nhánh dedicated_db in cả connection string (kèm credential) ra stdout dạng hướng dẫn "export env var này" — ai capture output đó (CI log, terminal scrollback) có DSN dạng plaintext. Không nhất quán với vault_put_dsn (chỉ echo tên ref).

## metap-attachments

Thư viện sạch — mọi bind qua sqlx, table_name luôn qua validate_table_name trước khi vào format!().

**[LOW]** ensure_dedicated_table (lib.rs:79-104) chỉ kiểm tra hình dạng ký tự của table_name, không loại trừ trùng tên bảng nền tảng sẵn có (records, users,...) — rủi ro caller-discipline, chưa khai thác được từ input client.

Route layer (metap-http/routes/attachments.rs) tenant-scoping đúng, chống IDOR bằng cross-check entity_name/record_id sau lookup theo tenant_id. Vấn đề Content-Disposition filename đã nêu ở phần metap-http trên.

## metap-auth

**[HIGH] Basic auth không ràng buộc tenant thật của user với tenant client tự khai báo** — verify_credentials (metap-peripherals/src/auth.rs:115) chạy SELECT ... FROM users WHERE email = $1, **không lọc theo tenant_id**. Vì TenantStrategy::Schema hiện luôn dùng chung schema public (theo CLAUDE.md), bảng users trên thực tế shared giữa mọi tenant Schema-strategy. basic_auth handler (metap-http/src/auth.rs:190-249) lấy tenant_id từ header X-Tenant-Id do client tự khai, verify xong gọi thẳng get_roles_for_user(tx, tenant_id, user.id) — **không có bước kiểm tra user.tenant_id == tenant_id**. Kịch bản cụ thể: user hợp lệ ở Tenant A gửi X-Tenant-Id: B (B cũng bật Basic auth) — mật khẩu vẫn đúng vì cùng dòng users, request được coi là "thuộc tenant B" dù chưa từng chứng minh quan hệ với B. Vi phạm trực tiếp invariant "mọi route giả định tenant scope thật" trong CLAUDE.md.

**[LOW/MEDIUM]** JIT-provision OIDC user (metap-auth/src/lib.rs:124-144) dùng INSERT ... RETURNING không ON CONFLICT — 2 login đồng thời đầu tiên cho cùng (tenant_id, external_subject) sẽ khiến 1 request lỗi 500 thay vì fallback find_oidc_user.

**[LOW]** oidc.rs:63-69's http_client() tắt redirect đúng (chống SSRF) nhưng không set timeout — IdP phản hồi chậm/treo có thể treo handler vô thời hạn.

**[LOW]** issuer_url do admin cấu hình tự do, dùng làm target HTTP outbound không allowlist — SSRF-shaped ở mức trust "admin", nên ghi vào threat model.

Điểm sạch đã verify: không timing side-channel ở Basic auth (dummy-hash verify khi email không tồn tại), JIT provisioning cấp 0 role mặc định (deny-by-default), CSRF state/nonce dùng 1 lần đúng.

## metap-dashboards

Thư viện sạch — tenant/user scoping đúng qua bind param.

**[MEDIUM]** lib.rs:73-115 không giới hạn kích thước/độ sâu/số widget của layout (JSONB) trước khi ghi — client có thể PUT /dashboards/me payload nhiều MB lặp lại → phình bảng dashboard_configs không giới hạn (DoS dạng storage).

Route layer: gating admin cho tenant-default **đúng** (save_tenant_default_dashboard dùng AdminContext extractor, không phải AuthContext).

---

## packages/platform-react (frontend library dùng chung)

**[MEDIUM] Hardcode business-entity crm.customers trong package "generic"** — src/i18n/entityLabels.ts:29-43 hardcode field/transition name của crm.customers (code, referredBy, activate, block,...) ngay trong package tái sử dụng. Vi phạm rule "không metap-*/package nào biết business entity" — nên chuyển vào apps/crm-fe.

**[MEDIUM] URL không encode entity name** — src/metadata/useEntity.ts:7 build ` /metadata/entities/${entityName}  không encodeURIComponent, khác với LoginForm.tsx có encode tenantId. entityName đến từ route param — URL segment chứa /, ?, # sẽ phá vỡ hoặc redirect request.

**[MEDIUM x2] OidcCallbackPage.tsx:18-25** — (1) nếu URL hash không phải #token=... (IdP trả lỗi, hoặc nav trực tiếp), effect không làm gì — trang treo mãi ở "Signing you in…" không có fallback/error UI; (2) sau khi đọc token, code không strip fragment (history.replaceState) trước khi navigate — JWT thô nằm lại trong browser history.

**[MEDIUM] Swallowed fetch error ở reference field UI** — ReferenceFieldInput.tsx:41-62 và ReferenceFieldValue.tsx:16-28 đều bỏ qua error từ useApiQuery — lookup fail hiển thị như "không có kết quả"/UUID thô thay vì lỗi rõ ràng.

**[MEDIUM] Masking heuristic nhầm lẫn "rỗng" với "không có quyền"** — FieldValue.tsx:6-18 coi mọi giá trị null/undefined trên field required là "bị mask do thiếu quyền", kể cả khi field đó chỉ đơn giản là rỗng hợp lệ (data cũ, schema đã migrate).

**[MEDIUM] Detail action không gate theo capability** — RecordDetail.tsx:107-125 render nút Edit/Delete vô điều kiện, bỏ qua record.capabilities.canUpdate/writableFields, khác với WorkflowActionBar trong cùng component — user chỉ đọc thấy nút bấm được, chỉ fail sau round-trip.

**[LOW]** LocaleProvider.tsx:28-47 không reset locale khi logout; UsersAdminPage.tsx:139 role-revoke không có confirm dialog (mọi hành động destructive khác đều có); GeneratedForm.tsx có non-null assertion (L88, L103) an toàn hôm nay nhưng dễ vỡ khi refactor, không có client-side required-field validation (phụ thuộc hoàn toàn round-trip server).

Sạch: api/client.ts, useApiQuery/useApiInfiniteQuery/useApiMutation.ts, AuthContext.tsx, Can.tsx, LoginForm.tsx, charts/BarChart.tsx, WorkflowActionBar.tsx, GeneratedList.tsx, recordCapabilities.ts, admin/*. generated-types.ts là artifact hợp lệ, không có dấu hiệu bị sửa tay/lệch so với generated convention.

---

## Phụ lục: apps/* (demo/sample — mức ưu tiên thấp hơn)

Các app này chỉ là consumer minh hoạ, không phải sản phẩm — liệt kê gọn, không đi sâu.

### apps/crm-server
- **[HIGH]** main.rs:85-87's reconcile_indexes hardcode build index trên bảng records, không biết crm.customers đã chuyển sang table-per-entity (Phase 36) — kể từ đó lệnh này không còn match dòng nào, các field code/name/phone/email không có index/unique/trigram thật trên bảng đang thực sự được query → full-scan âm thầm (lỗi bị nuốt, không log rõ).
- **[LOW]** sales_order_entity.rs's cancel chỉ từ draft, không từ confirmed; journal_entry_entity.rs's guard post không đảm bảo double-entry thật (có debit và credit khác 0 vẫn post được).

### apps/jira-server
- **[MEDIUM]** issue_entity.rs:258-268's guard approve chỉ check reporterEmail != "" — vì field required, validator chỉ check "có mặt" chứ không check nội dung có ý nghĩa, guard gần như luôn đúng, không ngăn tự-approve.
- **[LOW]** Reconcile-at-boot order, OUTBOX_WORKER_INLINE pool wiring — đã verify đúng, không có bug.
- **[LOW]** epic_entity.rs/sprint_entity.rs's trạng thái cuối không có transition ngược (khác issue), không nhất quán quy ước; watcher_entity.rs thiếu unique constraint DB cho (issue, userEmail).

### apps/jira-fe
- **[CRITICAL, UI-only]** IssuePanels.tsx:61's AssigneePicker.handleChange — clear assignee dùng email ?? undefined, JSON.stringify drop key undefined → PATCH body rỗng {} → unassign không hoạt động, UI snap về giá trị cũ không báo lỗi.
- **[HIGH]** Mọi write mutation trong IssuePanels.tsx (AssigneePicker, WorklogsPanel, WatchersPanel) không có .catch — lỗi 409/network bị nuốt hoàn toàn im lặng.
- **[MEDIUM]** Comment author có thể tự gõ authorEmail tuỳ ý (comment spoofing); JQL string interpolation không escape ở LogworkReportPage.tsx; silent truncation (limit cứng, không thông báo) ở AdvancedSearchPage/LogworkReportPage/DashboardPage; SprintReportPage treo loading vĩnh viễn nếu 1 trong N request /workflow-events fail; CustomizableDashboardPage không validate layout JSON từ backend, không optimistic-concurrency khi save.
- **[LOW]** Duplicate constant map (STATUS_LABEL/PRIORITY_COLOR) copy-paste giữa BoardPage/DashboardPage/BacklogPage.

### apps/crm-fe
Sạch — thin wrapper quanh platform-react, không có logic riêng đáng chú ý.

---

## Ghi chú tổng hợp

Hai phát hiện HIGH có tính "operationally significant" nhất, tức là **loại lỗi im lặng chỉ lộ ra khi có sự cố production** (RabbitMQ blip, worker crash) chứ không phải bug lộ ngay khi test thông thường:
- outbox-publisher không reconnect (#6) — phá vỡ đúng cái outbox pattern được xây ra để đảm bảo.
- cron-scheduler/metap-cron thiếu idempotency khi message redeliver (#7, #10) — webhook/bulk action có thể chạy trùng.

Hai phát hiện có tính bảo mật nghiêm trọng nhất:
- SQL injection qua field name chưa validate (#1) — cần validate field.name giống hệt table_name đã làm trong metap-metadata::compiler.
- Basic-auth không ràng buộc tenant (#2) — cần thêm check user.tenant_id == tenant_id (hoặc filter WHERE email = $1 AND tenant_id = $2) trước khi coi request là hợp lệ cho tenant đó.


---- WORKFLOW
Kiến trúc tổng thể: metap-workflow là 1 flat FSM (danh sách transition "from→to" kèm guard), chưa có
  "State" như đối tượng bậc nhất — đây là nguyên nhân gốc của phần lớn bug tìm được. Coupling với
  metap-permission (guard tái dùng PolicyCondition) là quyết định đúng, không phải smell. So với Jira
  thật (mà jira-server demo mô phỏng, có 3 khái niệm condition/validator/post-function),
  metap-workflow mới chỉ có condition — thiếu hẳn validator (transition không nhận payload) và
  post-function (không có side-effect đồng bộ khi transition).

  Bug đáng chú ý nhất:
  - HIGH — transition() ghi to_state xuống DB không qua validate, và compiler không cross-check
    from/to với enum_values → 1 transition định nghĩa sai (typo) có thể ghi giá trị status "hỏng" vĩnh
    viễn vào record.
  - HIGH — guard chỉ hỗ trợ Eq/Neq/In/NotIn, không có so sánh số (Gt/Lt...) → không biểu đạt được
    "amount > 10000 cần duyệt cấp cao"; đã tìm thấy bằng chứng thực tế phải lách bằng Neq 0 trong
    journal_entry_entity.rs, vô tình chấp nhận cả giá trị âm — khả năng là bug nghiệp vụ ẩn.
  - MEDIUM — guard evaluation lệch giữa lúc preview khả dụng (GET, dùng data đã enrich) và lúc thực
    thi transition thật (dùng data thô) — nút UI có thể hiện "khả dụng" nhưng bấm vào lại fail.

  Khuyến nghị ưu tiên: vá 2 điểm HIGH trước (chi phí thấp), sau đó thêm payload cho transition() + cơ
  chế "action" đơn giản đồng bộ trong cùng transaction (thay vì luôn phải đi qua outbox→RabbitMQ→cron
  cho mọi side-effect nhỏ).
