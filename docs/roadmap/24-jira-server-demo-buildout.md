## Phase 24: xây đầy `apps/jira-server` cho demo — bước 1/nhiều: sprint, comment, kanban workflow (2026-08-23, đang tiếp tục)

Chủ dự án: "làm đầy jira-server lên để t xem, dashboard, isue, comment, sử cứm workflow chuyển
trạng thái, build sprint, date, kanban,... rất nhiều luôn, phải bắt đầu build thì mới biết metap
cần thêm gì" — chủ động build tăng dần thay vì lên kế hoạch đầy đủ trước, đúng tinh thần "bắt đầu
build để lộ ra chỗ metap còn thiếu". Batch này là phần backend nền cho kanban/sprint/comment;
frontend (`apps/jira-fe`, kanban board, dashboard) là bước tiếp theo.

**Entity mới**: `jira.sprints` (`project` Reference bắt buộc, `name`/`goal`, `startDate`/
`endDate` dùng `FieldKind::Date`, `status` workflow `planned → active → completed`) và
`jira.comments` (`issue` Reference bắt buộc, `authorEmail`, `body`, không workflow). `jira.issues`
thêm `sprint` (Reference tới `jira.sprints`, optional — không set nghĩa là nằm ở backlog) và
`dueDate` (`FieldKind::Date`, indexed). Thứ tự reconcile trong `main.rs`'s boot loop đổi thành
`project → sprint → issue → comment` — bắt buộc, không tuỳ ý: FK của field tham chiếu build thẳng
vào bảng vật lý của entity đích ngay lúc DDL chạy (`compile()`), nên bảng đích phải đã tồn tại
trước.

**Workflow `jira.issues` mở rộng từ 3 sang 4 trạng thái** để có đủ cột cho kanban board thật:
`todo → in_progress → in_review → done`, transition `start`/`submit_for_review`/
`request_changes`/`approve`/`reopen` tạo thành hình thoi (`in_review` có thể quay lại
`in_progress` hoặc tiến tới `done`) — đúng hình dạng 1 board kéo-thả cần để chọn đúng transition
action theo cặp cột nguồn/đích.

**Bug thật tìm được lúc verify sống — lần đầu có field `Date` được đánh `indexed`/`sortable`
trong toàn bộ codebase**: `metap-reconciler::compile()`'s nhánh expression-index (cho field
không có cột vật lý thật) tạo index dạng `((data ->> 'field')::date)` cho mọi field — nhưng cast
`text → date`/`text → timestamptz` của Postgres là `STABLE` (phụ thuộc `DateStyle`/`TimeZone`
GUC), không phải `IMMUTABLE`, mà `CREATE INDEX` trên expression bắt buộc mọi hàm/cast trong đó
phải `IMMUTABLE` — reconcile `jira.issues` (field `dueDate`) lỗi thẳng
`functions in index expression must be marked IMMUTABLE`, chặn đứng boot. Sửa: `Date`/`Datetime`
dùng expression **không cast** (`(data ->> 'field')`) thay vì cast kiểu — không mất gì về đúng
đắn: `metap-query`'s `condition_to_sql`/`sort_field_expression` chưa từng emit so sánh có kiểu
cho field không phải cột thật (luôn `jsonb_extract_path_text`, so sánh text thuần), nên index
không cast còn **khớp đúng hơn** với SQL query thật phát sinh so với bản có cast trước đây —
chuỗi ngày ISO-8601 (định dạng wire chuẩn của platform) vốn đã sort đúng thứ tự khi so sánh dạng
text. Thêm unit test
`indexed_date_field_gets_an_uncast_text_expression_index_not_a_stable_cast`
(`crates/metap-reconciler/src/compile.rs`).

**Kiểm chứng sống đầy đủ vòng đời**: boot lại `jira-server` sau khi sửa — cả 4 entity reconcile
thành công (`ops_applied` 4/18 lần đầu, 0/0/0/0 lần chạy lại kế tiếp — idempotent thật, không
drop/tạo lại index vô hạn). `\d entities.jira_issues` qua psql xác nhận `project`/`sprint` là FK
thật, index `dueDate` đúng dạng không cast. Tạo dữ liệu thật qua HTTP (không mock): project →
sprint → issue (tham chiếu cả hai) → comment (tham chiếu issue) → transition issue qua đủ 4 trạng
thái (`start`/`submit_for_review`/`approve`, guard trên `approve` pass đúng) → list filter theo
`sprint` (FK hydration trả đúng `relatedDisplay: {project, sprint}`) → list sort theo `dueDate`
không lỗi. `cargo fmt --check`/`clippy --workspace --all-targets -D warnings`/
`test --workspace --lib --bins` sạch toàn bộ.

### Bước 2/nhiều: `apps/jira-fe` — dashboard + kanban board (2026-08-23, cùng ngày)

`apps/jira-fe` mới (`@metap/jira-fe`), mirror y hệt khung `apps/crm-fe` (Vite + React +
TypeScript + `packages/platform-react` qua `workspace:*`, cổng dev riêng 5174 để chạy song song
với `crm-fe`'s 5173, proxy sang jira-server's cổng 3100). `pnpm-workspace.yaml` glob sẵn `apps/*`
nên không cần sửa gì để `pnpm install` nhận app mới.

**Phần lớn UI không cần code mới** — đúng giá trị cốt lõi "metadata-driven UI" của
`packages/platform-react`: `jira.projects`/`jira.sprints`/`jira.comments` và cả `jira.issues` ở
dạng bảng/form đã chạy ngay qua `GeneratedList`/`GeneratedForm`/`RecordDetail` sẵn có, kể cả field
`Date` (`DateInput`/`DateTimePicker` của Mantine đã map sẵn cho `FieldKind::Date`/`Datetime` từ
trước) — không phải build thêm form/table nào cho sprint hay comment.

**2 component thật sự mới**:
- `DashboardPage` — đếm issue theo `status`/`priority`, bảng issue tạo gần đây nhất. Dùng thẳng
  `useApiQuery` sẵn có của `platform-react`, không thêm hook mới.
- `BoardPage` — kanban board thật: chọn project (dropdown từ `jira.projects`), cột lấy từ
  `enumValues` của field `status` qua `GET /metadata/entities/jira.issues` (không hardcode 4
  trạng thái tay — đổi workflow ở entity definition thì board tự đổi cột theo, không cần sửa FE).
  Kéo-thả dùng **HTML5 drag-and-drop gốc của trình duyệt** (`draggable`/`onDragStart`/
  `onDragOver`/`onDrop`), không thêm dependency DnD mới — đúng tinh thần tối thiểu, board này
  không cần animation/reorder phức tạp. Thả issue sang cột khác tra đúng
  `WorkflowTransition` theo cặp `(from, to)` trong `entity.workflow.transitions` rồi gọi thẳng
  `POST /api/jira.issues/{id}/transitions/{action}` — **y hệt lời gọi** `WorkflowActionBar` của
  `platform-react` đã dùng (cùng 1 code path, không phải luồng song song mới) — nếu không có
  transition trực tiếp giữa 2 cột thì từ chối thả + báo lỗi rõ ràng (Mantine notification), không
  âm thầm không làm gì.

**Kiểm chứng**: `tsc -b` (typecheck), `oxlint` (9 file, 0 warning/error), `prettier --check`,
`vite build` production đều sạch — theo đúng chính sách FE đã chốt trong dự án ("không tự
Playwright-verify thay đổi FE — code xong, typecheck/lint/build sạch, bàn giao người dùng tự kiểm
tra trên trình duyệt"), nên **chưa** tự kiểm tra tương tác kéo-thả/click thật trên trình duyệt.

**Gap thật tìm được lúc thử đăng nhập demo (2026-08-24)**: `LoginForm`'s `POST /auth/login` query
`AppState.pool` (DB platform), không phải DB dedicated của tenant — gap đã ghi trong
`apps/jira-server/src/main.rs`'s doc comment từ trước nhưng chưa từng thực sự chặn ai, tới lúc thử
đăng nhập thật qua trình duyệt mới lộ ra: **không đăng nhập được vào app demo bằng form thật**.
Sửa tạm bằng 1 fallback dev-only ngay trong `apps/jira-fe/src/demo/LoginPage.tsx`
(`PasteTokenFallback`) — dán token mint bằng `pnpm mint:jira-token` vào, gọi `setToken` thẳng, có
ghi chú rõ đây không phải luồng auth thật, chỉ tồn tại tới khi gap `/auth/login` được sửa triệt để
(cần `AppState.pool`-based routes route theo tenant thật, chưa làm — xem `main.rs`'s doc comment).

**Còn lại (chưa làm)**: UI thread comment lồng trực tiếp trên trang chi tiết issue (hiện phải xem
qua `/records/jira.comments` lọc theo `issue` — dùng được nhưng chưa "tự nhiên" như 1 thread thật);
dashboard chưa scope theo project (đang gộp toàn bộ tenant); chưa có UI tạo/sửa comment ngay trong
board card; `/auth/login` vẫn chưa route theo tenant thật cho `DedicatedDb` (gap kế thừa từ
`crm-server`, không mới) — `PasteTokenFallback` chỉ là lối tắt demo, không phải bản sửa thật.

### Bước 3/nhiều: `outbox-publisher` không hề chạy cho tenant của jira-server — sự cố tìm được khi thử demo trực tiếp (2026-08-24)

Lúc bật server thật lên cho chủ dự án xem demo, chủ dự án hỏi thẳng: "phần jira server có run
reconcile, outbox + scheduler chung binary với main k?" — câu hỏi lộ ra 1 gap thật đang tồn tại,
không phải giả định: kiểm tra trực tiếp `outbox_events` trong `metap_myjira` (DB dedicated của
tenant jira) thấy **9 event `published_at IS NULL`** — mọi lần tạo/transition `jira.issues` từ lúc
demo tới giờ **chưa từng được publish lên RabbitMQ**, vì:
- `metap-crud::CrudService` ghi outbox event vào đúng transaction với business write, luôn qua
  `Router::begin(tenant_id)` — với tenant `DedicatedDb` (như jira-server's tenant) nghĩa là ghi
  thẳng vào DB riêng của tenant đó (`metap_myjira`), không phải DB nào khác.
- `outbox-publisher` (worker chuẩn để drain bảng này) chỉ tồn tại dưới dạng process riêng
  (`pnpm worker:outbox:rs`), và script đó **hardcode `cd apps/crm-server`** — trỏ vào
  `DATABASE_URL` của `crm-server` (platform DB), không bao giờ chạm tới `metap_myjira`.
- Kết quả: **không có bất kỳ worker nào từng drain outbox của tenant jira-server cả** — im lặng,
  không lỗi, không log cảnh báo, chỉ đơn giản là event nằm mãi ở `published_at = NULL`.

**Sửa theo đúng pattern đã có sẵn trong repo** (`notification-worker`'s binary+lib +
`NOTIFICATION_WORKER_INLINE`, không phát minh pattern mới):
- `crates/outbox-publisher` tách thành `[lib] outbox_publisher` (giữ nguyên toàn bộ logic
  `run`/`publish_pending`/`mark_published`/`mark_failed`) + `[[bin]] outbox-publisher` (giờ chỉ
  còn load config, connect DB/RabbitMQ, gọi `outbox_publisher::run(...)`) — binary độc lập
  (`pnpm worker:outbox:rs`) vẫn chạy y hệt trước, không đổi hành vi cho `crm-server`.
- `apps/jira-server`: thêm cờ `OUTBOX_WORKER_INLINE=true` — khi bật, spawn 1 task chạy
  `outbox_publisher::run(&tenant_pool, ...)` **ngay trong process jira-server**, dùng đúng
  `tenant_pool` (đã resolve qua `Router::pool_for` từ bước reconcile) chứ không phải `pool`
  (platform DB) — đây chính là điểm khác biệt bắt buộc so với `crm-server`: tenant dedicated-db
  cần 1 thứ gì đó drain đúng DB của riêng nó, không thể dùng chung 1 worker trỏ platform DB như
  các tenant `Schema`-strategy vẫn làm được.
- Không bắt buộc phải inline — chạy `outbox-publisher` như 1 process riêng với `.env` riêng trỏ
  DSN của tenant cũng đúng (giống hệt cách `crm-server` làm), inline chỉ đơn giản là lựa chọn gọn
  hơn cho 1 app demo single-tenant đã sẵn `tenant_pool` trong tay. Đúng như chủ dự án chốt: "single
  binary hoặc chia process ra sao thì tùy, nhưng phải biết chắc chắn phải có" — bản chất là phải
  tồn tại 1 worker drain đúng DB, hình dạng deployment (inline hay tách process) là lựa chọn.

**Kiểm chứng sống**: trước khi sửa — `SELECT count(*) FILTER (WHERE published_at IS NULL)` = 9/9.
Bật `OUTBOX_WORKER_INLINE=true` chạy jira-server thật → cả 9 event cũ được publish hết trong vòng
poll đầu tiên (0/9 unpublished). Tạo thêm 1 record mới qua HTTP thật → event mới cũng được drain
đúng trong vòng poll kế tiếp (~500ms), xác nhận không phải chỉ dọn 1 lần lúc boot mà đúng là vòng
lặp poll liên tục. `cargo build --workspace`/`fmt --check`/`clippy --workspace --all-targets -D
warnings` sạch — cả `outbox-publisher`'s standalone binary lẫn `notification-worker` (crate khác
dùng chung pattern binary+lib) không bị ảnh hưởng bởi refactor.

**Còn lại**: `cron-scheduler` vẫn chưa wire cho jira-server — nhưng chưa có cron job nào định
nghĩa cho tenant này nên chưa có gì cụ thể để verify; sẽ làm theo đúng pattern trên (binary+lib đã
có sẵn) khi có nhu cầu thật, không build trước khi có use case.

### Bước 4/nhiều: siết JWT leeway 60s → 20s (2026-08-24)

Lúc review roadmap tổng thể, chủ dự án hỏi kỹ hơn về mục "JWT leeway 60s" đã ghi nhận ở
`testing/security/checklist.md`'s "Chưa cover" (không phải bug, mặc định của crate
`jsonwebtoken`'s `Validation::new()` — token vẫn được chấp nhận tới 60s sau `exp`, để chống lệch
đồng hồ giữa server ký/verify) — sau khi giải thích rủi ro thật (hệ thống không có cơ chế revoke
token riêng, exp+leeway là biên duy nhất chặn 1 token bị lộ còn dùng được bao lâu) và 3 lựa chọn
(giữ 60s / siết 5-10s / leeway=0), chủ dự án chốt **20s**.

`crates/metap-http/src/auth.rs`: thêm `validation.leeway = 20;` tường minh (trước đó dùng default
ẩn của crate). `crates/metap-http/tests/jwt_security_postgres.rs`'s `expired_token_is_rejected`
cập nhật theo: sleep 65s → 25s (vẫn đủ qua leeway mới, chạy nhanh hơn), doc comment + assert
message sửa từ "60s" sang "20s". `testing/security/checklist.md` cập nhật: xoá khỏi mục "chưa
cover", ghi nhận giá trị mới vào hàng "JWT hết hạn" ở mục "Đã cover".

