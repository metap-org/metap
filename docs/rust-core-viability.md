# Rust cho `packages/core` — Bản ghi quyết định

Ngày: 2026-08-07

Trạng thái: **Đã quyết định — Phương án B.** `packages/core` sẽ chuyển sang Rust, cho mọi
profile triển khai (không chỉ riêng một binary Tiny-profile trong tương lai). Tài liệu này
ghi lại quá trình đi đến quyết định đó: lập luận được đưa ra, spike đo lường thực tế để
kiểm chứng lập luận, và các câu hỏi phát sinh cụ thể (chiến lược schema/codegen, khả năng
tiếp cận của contributor) mà nó đặt ra. **Cả 9 bước trong Migration Order bên dưới đều đã
hoàn thành** tính đến 2026-08-07 — xem mục của từng bước để biết những gì đã được xây dựng
và cách nó được xác minh, và ghi chú kết ở cuối bước 9 để biết chính xác những gì vẫn *chưa*
làm (chưa port entity nghiệp vụ nào, chưa có binary boot-sequence thực sự, các vấn đề của
Phase 8 Hardening) để không bị hiểu nhầm là thiếu sót.

## Nguồn gốc

Vấn đề được nêu ra trong buổi thảo luận architecture review ngày 2026-08-07, sau khi
`docs/architecture-review-2026-08-07.md` Part 5 khuyến nghị không nên viết lại toàn bộ
`packages/core` sang Rust ngay từ lần đầu (chưa có trigger hiệu năng nào được đo đạc). Câu
hỏi này sau đó được mở lại với một lập luận cụ thể hơn (bên dưới), được kiểm chứng bằng một
spike benchmark thực tế (`experiments/rust-outbox-poc/`), và đã được quyết định.

## Lập luận ủng hộ Rust

Có hai lý do được đưa ra, được đánh giá riêng biệt vì mỗi lý do cần một mức độ xem xét khác
nhau.

### 1. Dấu chân hạ tầng tối thiểu + tốc độ — đã được xác nhận bằng đo lường

Nếu mục tiêu là một bản triển khai thực sự tối giản, nhanh (RAM thấp, không có GC pause,
artifact phân phối nhỏ), Rust thắng Node — không phải một cuộc so kè sát nút, và cũng không
còn chỉ là lập luận suông nữa: spike bên dưới đã đo lường điều này trực tiếp. Các tùy chọn
single-executable của chính Node (`node --experimental-sea-config`, `bun build --compile`)
vẫn mang theo toàn bộ runtime V8 và GC; chúng cho ta "một file duy nhất để phân phối", chứ
không phải "dấu chân tối thiểu".

### 2. Sức hút với contributor — một động lực có thật, một rủi ro có thật, cần được nêu rõ chứ không nên gạt bỏ

Một ngôn ngữ đang thịnh hành thu hút contributor, nhưng đi kèm với đó là hai cái giá phải
trả: contributor đến vì trend không chắc sẽ ở lại khi trend hạ nhiệt, và cái khó thực sự của
metap nằm ở domain (mô hình hóa entity/workflow/permission), không nằm ở ngôn ngữ. Cụ thể
hơn, việc có một core viết bằng Rust song song với việc tác giả các business-module thay đổi
*ai* có thể động vào phần nào — xem mục "Khả năng tiếp cận cho Contributor / Outsource" bên
dưới để biết quyết định này giảm thiểu rủi ro đó như thế nào thay vì phớt lờ nó.

## Spike: Benchmark Rust Outbox-Publisher

**Đã xây dựng gì:** một bản triển khai lại bằng Rust của
`apps/crm/src/workers/outbox-publisher.ts` (poll `outbox_events` với
`FOR UPDATE SKIP LOCKED`, publish lên RabbitMQ, đánh dấu `published_at`), được benchmark đối
chứng với một bản triển khai Node độc lập tương ứng. Chọn phần này vì nó tách biệt hoàn toàn
khỏi Zod/OpenAPI/codegen frontend — bằng chứng cho thấy spike không thể vô tình biến thành
một cuộc viết lại toàn bộ core trước khi có đủ bằng chứng để biện minh cho việc đó. Phương
pháp luận đầy đủ nằm trong `experiments/rust-outbox-poc/README.md`.

### Kết quả (2026-08-07, đo trên Postgres/RabbitMQ dev thật của repo)

| Chỉ số | Rust | Node | Kết luận |
|---|---|---|---|
| Kích thước binary bản release | 3.1 MB, tự chứa (self-contained) | cần runtime Node 118 MB + `node_modules` | Rust thắng áp đảo |
| Cold start | 31–38 ms | 147–151 ms | Rust nhanh hơn ~4–5 lần |
| Idle RSS | 13.0–13.5 MB | 64.9–65.1 MB | Rust thấp hơn ~5 lần |
| Throughput drain (5 lần chạy mỗi bên, 500 dòng fixture) | 738–800 events/sec (trung bình ≈ 785) | 737–740 events/sec (trung bình ≈ 738) | **Rust cao hơn ~6–8%**, nhất quán qua các lần chạy |

Cả bốn chỉ số đo được đều nghiêng về Rust. Riêng kết quả throughput cần tới năm lần chạy mỗi
bên mới đủ để kết luận với độ tin cậy — lần so sánh hai-lần-chạy đầu tiên cho thấy hai bên
ngang nhau về mặt thống kê, và một phiên bản sớm có bug (chờ một future publisher-confirm
của RabbitMQ không hề được dùng đến, trong khi phía Node theo kiểu fire-and-forget không có
bước này) đã có lúc cho thấy Rust chậm hơn 7 lần — đây là một bug trong cách triển khai của
spike, không phải kết quả thật. Đáng ghi lại như một kiểu lỗi cụ thể mà một lần port thật sự
cần tránh: code async viết theo kiểu "idiomatic" một cách ngây thơ, không khớp với ngữ nghĩa
publish hiện có, có thể âm thầm làm giảm throughput.

Lý do có một khoảng chênh throughput cùng bậc độ lớn nhưng thực chất (~6–8%), trên một
workload mà thời gian round-trip tới Postgres/RabbitMQ chiếm ưu thế hơn là tính toán: round
trip mạng là như nhau cho cả hai ngôn ngữ, nhưng driver/runtime của mỗi ngôn ngữ vẫn cộng
thêm overhead cố định cho mỗi thao tác (serialization, lập lịch promise/task) lên trên round
trip đó. Qua 500 thao tác tuần tự, overhead trên mỗi thao tác thấp hơn của Rust cộng dồn lại
thành một khoảng chênh nhỏ nhưng nhất quán, dù cả hai cách triển khai đều không bị giới hạn
bởi CPU. Điều này cũng có nghĩa là khoảng chênh phần trăm nhiều khả năng sẽ *tăng* lên chứ
không giảm đi khi chạy trên hạ tầng nhanh hơn (RTT mạng thấp hơn để lại tỷ lệ không gian lớn
hơn cho phần chênh lệch overhead cố định trên mỗi thao tác thể hiện ra), và nhiều khả năng
còn lớn hơn nữa dưới điều kiện concurrency thực sự (nhiều lượt publish cùng bay song song)
thay vì kiểu tuần tự từng-cái-một của benchmark này.

## Quyết định

**Phương án B — Rust cho toàn bộ `packages/core`, ở mọi profile triển khai.** Không chỉ giới
hạn ở một binary Tiny-profile tương lai (Phương án A); đây là một cuộc thay thế toàn bộ
execution engine TS/Zod.

Quyết định này làm phát sinh hai câu hỏi tiếp theo cụ thể, được giải quyết bên dưới: việc
sinh schema/type sẽ tồn tại ra sao qua lần đổi ngôn ngữ này, và làm sao để tránh thu hẹp tập
contributor — chính rủi ro mà quyết định này đã nêu tên.

## Chiến lược Schema & Codegen

Hóa ra chuỗi sinh type cho frontend đã trung lập về ngôn ngữ hơn nhiều so với giả định ban
đầu của tài liệu này — đáng để đính chính rõ ràng, vì điều đó thay đổi mức chi phí thực tế mà
quyết định này gây ra.

**Những gì thực sự đang diễn ra hôm nay:** script `generate:types` của `packages/platform-react`
chạy `openapi-typescript http://localhost:3000/metadata/openapi.json` — nó tiêu thụ một tài
liệu OpenAPI dạng JSON qua HTTP, chứ không đọc trực tiếp source TypeScript/Zod. Và
`generateOpenApiDocument()` trong `packages/core/src/core/metadata/openapi-generator.ts` tự
nó chỉ là một hàm thuần túy biến đổi `EntitySummary`/`EntityField[]` (một cấu trúc dữ liệu
generic, có thể serialize: tên field, kind, required, enum values) thành một mảnh JSON
Schema — nó hoàn toàn không đụng tới type của Zod, chỉ dùng đúng cấu trúc field-metadata mà
`MetadataCompiler` vốn đã coi là wire contract.

**Điều này có ý nghĩa gì với Rust:** hợp đồng trao đổi dữ liệu (interchange contract) vốn đã
là OpenAPI JSON, không phải Zod. Một `packages/core` viết bằng Rust chỉ cần phục vụ một
`/metadata/openapi.json` tương đương — xây dựng theo cùng cách, như một hàm thuần túy biến
đổi từ entity field metadata sang JSON Schema (`serde_json::json!` là đủ; không cần một crate
OpenAPI dùng derive-macro như `utoipa`, vì metadata của dự án này vốn đã là dữ liệu động, chứ
không phải struct Rust riêng cho từng entity). Lệnh `generate:types` của
`packages/platform-react` **không cần thay đổi gì** — nó không hề biết tài liệu nó đang tiêu
thụ được phục vụ bởi ngôn ngữ nào.

**Cái thực sự bị mất:** vai trò của Zod như một *runtime validator* cho payload create/update
— đây là một mối quan tâm thật sự, tách biệt với việc sinh OpenAPI, và cần một phương án
tương đương bằng Rust. Hình dạng được khuyến nghị: một **validator generic được xây dựng
trực tiếp từ `EntityField[]`** (kind, required, enum values) thay vì một schema viết tay
riêng cho từng entity (một struct derive theo `validator`/`garde`, hay field `schema` của Zod
hiện nay). Đây không chỉ là để đạt sự tương đương — nó còn là một cải tiến so với thiết kế
hiện tại, vốn đòi hỏi developer phải tự tay giữ cho `schema` Zod viết tay đồng bộ với mảng
`fields` (một rủi ro trùng lặp vốn đã tồn tại, đặc thù của TS, không phải do Rust tạo ra). Nó
cũng khớp thẳng với hình dạng mà control plane low-code của Phase 11 cần: một khi entity được
tác giả trực tiếp trong DB thay vì trong code, sẽ không còn file source riêng cho từng entity
để viết tay schema, ở bất kỳ ngôn ngữ nào — chỉ còn một bộ diễn giải metadata generic. Xây
dựng bộ diễn giải generic đó ngay từ bây giờ, bằng Rust, là bước đi thẳng hướng tới Phase 11,
không phải một đường vòng.

## Thứ tự Migration (Migration Order)

Cách tiếp cận strangler: API Node của `apps/crm` tiếp tục phục vụ traffic thật xuyên suốt
quá trình. Mỗi bước port một đơn vị triển khai (deployable unit), có thể đảo ngược độc lập
(tag `v0.1.0` đánh dấu baseline TS/Node trước-Rust trên `master`, đã push lên `origin`,
phòng khi cần bỏ dở một bước nào đó) vì mọi bước đều đọc/ghi cùng một schema Postgres và
cùng một contract RabbitMQ, bất kể được viết bằng ngôn ngữ nào.

1. **Worker outbox-publisher — hoàn thành (2026-08-07).** Crate thật tại
   `crates/outbox-publisher/` (thành viên Cargo workspace, binary `outbox-publisher`; workspace
   root `Cargo.toml` sau đó được nâng lên repo root — xem ghi chú "Repo Structure" bên dưới),
   thay thế cho spike `experiments/rust-outbox-poc/`. Đảm bảo tương đương về retry/backoff với
   `OutboxService.publishPending` (ghi `attempts`/`last_error` theo từng dòng khi thất bại,
   batch được để lại cho chu kỳ poll tiếp theo) và với hợp đồng crash-khi-lỗi-không-xử-lý-được
   của `runOutboxPublisherLoop` (một lỗi batch không thể phục hồi sẽ lan truyền và khiến tiến
   trình thoát với mã khác 0 — một process manager được kỳ vọng sẽ khởi động lại nó, đúng như
   worker Node hiện nay, không âm thầm retry tại chỗ). Nạp config thật qua `dotenvy::dotenv()`
   từ current working directory, khớp chính xác cách `packages/core` resolve
   `import "dotenv/config"`, bao gồm cả việc override `OUTBOX_DATABASE_URL` rơi về
   `DATABASE_URL` khi không có. Được nối vào script `pnpm worker:outbox:rs` (`package.json` ở
   root) song song với `pnpm worker:outbox` cũ vẫn giữ nguyên — cả hai đều tồn tại; chọn chạy
   cái nào trong một lần triển khai là quyết định thuộc về config/ops, không phải thay đổi
   code, theo đúng nguyên tắc strangler nêu trên. Đã xác minh trên Postgres/RabbitMQ dev thật
   của repo: kết nối được, poll được, tắt sạch sẽ khi nhận SIGTERM. Rủi ro về HTTP bằng không —
   đây là một process riêng biệt; rollback chỉ đơn giản là chạy `pnpm worker:outbox` trở lại.
2. **Hạ tầng dùng chung (Shared infra) — hoàn thành (2026-08-07).** `crates/metap-infra/`:
   trait `EventBus` + implementation `RabbitEventBus` (interface mà
   `docs/architecture-review-2026-08-07.md` Part 2 đã khuyến nghị, theo đúng tiền lệ của
   `PolicyStore`), một wrapper `connect_db` cho pool Postgres, và `load_config`/`AppConfig`
   phản chiếu `packages/core/src/server/config.ts` từng field một (cùng các env var, cùng
   fallback `OUTBOX_DATABASE_URL`). `outbox-publisher` được refactor để dùng lại phần này —
   build lại, test lại trên Postgres/RabbitMQ dev thật, vẫn sạch.
3. **Lớp Metadata — hoàn thành (2026-08-07).** `crates/metap-metadata/`: các entity type
   (`entity.rs`, cố ý không có field `schema` — xem doc comment của nó), `MetadataCompiler`
   (`compiler.rs`: hash + validate, cùng các thông báo lỗi, cùng cách tiếp cận hash dựa trên
   stable-JSON-over-sorted-keys), `MetadataRegistry` (`registry.rs`), và bộ sinh OpenAPI
   (`openapi.rs`, JSON Schema `EntitySummary` viết tay phản chiếu `entity-wire-schema.ts` vì
   Rust không có bước reflection tương đương Zod). 14 unit test, bao phủ mọi case mà
   `metadata-compiler.test.ts`/`metadata-registry.test.ts` gốc từng bao phủ (field trùng lặp,
   field listView/defaultSort không tồn tại, các system field ngầm định, `refEntity` không
   tồn tại, `refDisplayField` không tồn tại, tính xác định/độ nhạy của hash). Tất cả đều pass.
4. **Permission service — hoàn thành (2026-08-07).** `crates/metap-permission/`:
   `PolicyCondition` (`eq`/`neq`/`in`/`notIn`, `all`/`any`, `fromContext`/`literal`,
   deserialize đúng hình dạng JSON wire mà bảng policies bên TS vốn đã lưu), trait
   `PolicyStore` + `PostgresPolicyStore` (SQL viết tay, không dùng ORM — đã xác minh trên
   Postgres dev thật của repo, không chỉ compile được: round-trip create/list/delete và
   round-trip JSONB `condition` đều pass dưới dạng integration test chạy thật),
   `PermissionSnapshot` (read-mask, write-gate ở cấp field/record, admin bypass — cùng logic,
   cùng cơ chế short-circuit cho admin tại mọi entry point như bản TS), `PermissionService`
   (giữ nguyên hành vi fail-loud-khi-tenant-rỗng của `scopedTenant`), và `PolicyExplainer`.
   10 unit test cho logic condition/role-gate thuần túy + 2 integration test chạy trên DB
   thật, tất cả đều pass.
5. **QueryPlanner — hoàn thành (2026-08-07).** `crates/metap-query/`: `cursor.rs`
   (encode/decode, cùng kiểm tra hình dạng UUID), `condition_to_sql.rs`
   (`recordPolicyWhereClause` + `conditionToSql`, cùng bản fix admin-bypass như mục ADR bên
   TS — với một sai khác cố ý, được ghi rõ trong doc comment của module: bind tham số theo
   kiểu-cho-từng-cột, có thể fail (fallible), thay vì dựa vào cơ chế ép kiểu text-parameter
   ngầm định của node-postgres, vì cơ chế binding dựa trên `Encode` của sqlx không tái tạo
   lại được kiểu suy luận đó), và `query_planner.rs` (`planList`: giới hạn phạm vi theo
   tenant/entity/soft-delete, các bộ lọc substring/FTS/exact, phân giải sortable-field với
   fallback về `defaultSort`, giới hạn (clamp) limit, kiểm tra + phân trang keyset cursor).
   11 unit test cộng thêm **8 integration test thực thi SQL sinh ra thật trên Postgres dev
   của repo** (`tests/query_planner_postgres.rs`) — giới hạn theo tenant, loại trừ bản ghi
   soft-delete, khớp substring bằng ILIKE, lọc exact-match, default-sort + clamp limit, sắp
   xếp tăng dần trên một field được khai báo sortable, fallback khi field sort được yêu cầu
   không sortable, phân trang keyset hai trang với kết quả rời nhau (disjoint), và từ chối
   khi cursor/sort không khớp nhau. Tất cả đều pass. Điều này đáp ứng đúng cam kết ban đầu
   của Migration Order là phải xác minh module này trên kết quả query thật, chứ không chỉ
   unit test logic thuần túy một cách cô lập.

   **Quy ước testing, cố định từ bước này trở đi:** unit test (logic thuần túy, không I/O)
   nằm trong `src/*.rs` của mỗi crate dưới `#[cfg(test)]` và chạy được bằng một lệnh
   `cargo test` trần, không bao giờ cần dependency bên ngoài. Test có chạm DB là một mối
   quan tâm riêng — e2e, không phải unit — và nằm trong `tests/*.rs` của mỗi crate, được đánh
   dấu `#[ignore]` để `cargo test`/`cargo test --workspace` không bao giờ mở kết nối database
   theo mặc định; chạy chúng một cách tường minh bằng `cargo test -- --ignored` khi DB dev đã
   sẵn sàng. Đã xác minh cả hai chiều: một lệnh `cargo test --workspace` trần với
   `DATABASE_URL` chưa được set thì pass 35/35 unit test và báo cáo các test đụng DB là
   `ignored` (chưa từng được thử, không phải bị skip lúc chạy); `cargo test --workspace --
   --ignored` khi DB dev đã bật thì pass cả 10 test.
6. **WorkflowEngine — hoàn thành (2026-08-07).** `crates/metap-workflow/`: không có struct
   `WorkflowEngine` (class bên TS không giữ state thật sự nào cả — dependency duy nhất của
   nó, `OutboxService`, chỉ được dùng để gọi tới `enqueue`, mà bản thân hàm đó cũng bỏ qua
   `this`), nên đây chỉ là một module gồm các hàm thuần túy — `get_initial_status`,
   `find_transition`, `run_guard`, `record_event` (ghi audit dạng append-only vào
   `workflow_events`), và `emit_transitioned`/`emit_created`/`emit_deleted`/`emit_updated`
   (các lần enqueue vào outbox). `WorkflowTransition::guard` giờ là một
   `metap_permission::PolicyCondition` (xem doc comment của `entity.rs`) — đúng hình dạng
   khai báo mà phát hiện Workflow ban đầu của `docs/rust-core-viability.md` đã khuyến nghị,
   được áp dụng ngay từ bước này thay vì hoãn lại, vì Rust ngay từ đầu đã không có khái niệm
   tương đương một function-guard bên TS để mà port. Cũng đã thêm
   `metap-infra::outbox::enqueue` (nửa-ghi của `OutboxService` — nửa đọc/publish thì đã có ở
   `crates/outbox-publisher/`, bước 1). 6 unit test (phân giải initial-status, tra cứu
   transition, đánh giá guard-less và có-guard) + 2 e2e test ghi dòng thật vào Postgres dev
   (hình dạng dòng audit log, topic và hình dạng payload của dòng outbox cho hai lần emit
   liên tiếp). Tất cả đều pass.
7. **CrudService — hoàn thành (2026-08-07).** `crates/metap-crud/`: `list`/`get`/`create`/
   `update`/`transition`/`delete`, cộng thêm `validate_payload` (validator generic, dẫn dắt
   bởi `EntityField`, thay thế Zod riêng cho từng entity — xem doc comment của
   `validation.rs` để biết điểm đơn giản hóa duy nhất đã biết: chỉ kiểm tra kiểu JSON, không
   kiểm tra định dạng string theo từng field như email/hình dạng UUID, vì metadata
   `EntityField` không có khái niệm format để dẫn dắt việc đó) và
   `mask_record_for_read`/`compute_capabilities` (logic masking cột `code`/`status` phản
   chiếu và logic guard-availability theo từng transition). 7 unit test cho validator +
   **3 e2e test chạy toàn bộ vòng đời trên Postgres dev**: create → get
   (capabilities/guard-availability chính xác) → update (409 do stale-version, sau đó thành
   công, chứng minh được state field không đổi) → transition (guard pass, sau đó 409 do
   invalid-from-state) → soft-delete → 404 sau khi delete, cộng thêm khẳng định chính xác số
   lượng `workflow_events` và chuỗi topic của `outbox_events`; test thứ hai cho `list` giới
   hạn theo tenant; test thứ ba thực thi một policy field-write non-admin thật, end-to-end,
   thông qua `PostgresPolicyStore`.

   **Một bug thật sự đã bị bắt bởi e2e test, không phải unit test**, đáng ghi lại như một
   trường hợp cụ thể cho lý do vì sao quy ước testing của crate này nhất quyết đòi cả hai:
   giá trị initial-status của `create` đang rơi đúng vào cột `status` ở top-level nhưng
   không rơi vào bên trong blob JSONB `data`, vì các schema Zod riêng theo từng entity bên
   TS thường đặt default cho state field (`status: z.enum([...]).default("draft")`), âm
   thầm điền sẵn `data.status` trước cả khi `getInitialStatus` chạy — một hành vi mà
   validator đơn giản hơn, không-default, của crate này không tái tạo lại. Đã fix bằng cách
   để `create` tự ghi initial status đã được phân giải vào `data` khi nó vắng mặt, cách này
   tường minh hơn là phụ thuộc vào việc một dòng default trong schema riêng của entity phải
   tồn tại và phải khớp với `workflow.initialState`.
8. **Lớp HTTP — hoàn thành (2026-08-07), phạm vi được thu hẹp như ghi chú bên dưới.**
   `crates/metap-http/`: các route `axum` phản chiếu `records.ts`/`metadata.ts`/`health.ts`
   (list/get/create/update/delete/transition, `/metadata/openapi.json` +
   `/metadata/entities(/:entity)`, `/health`), một extractor `AuthContext` (xác minh JWT
   RS256 qua `jsonwebtoken` + một lượt tra cứu `user_roles` sống theo từng request — nửa-đọc
   của `RoleAssignmentService`, được kéo lên trước từ bước 9 vì không route nào xác thực
   được nếu thiếu nó), và một hình dạng error-response phản chiếu bảng
   `SERVICE_ERROR_MESSAGES` của `error-handler.ts`. **Không** nằm trong phạm vi bước này,
   một cách cố ý: `helmet`/rate-limiting (thuộc Phase 8 Hardening, không phải mục tiêu "nối
   dây mỏng" của bước này), các route admin (policy CRUD, phần ghi của
   `RoleAssignmentService`, `IndexReconciler`, `MetadataDriftService` — thuộc đúng nghĩa
   Peripherals của bước 9), và `requestId`/`traceId` trong body lỗi (một đơn giản hóa nhỏ,
   cố ý — xem doc comment của `error.rs`). 1 e2e test — một **server axum thật, bind vào một
   socket thật, một JWT RS256 thật được mint và verify, Postgres thật** — chạy qua toàn bộ
   stack trong một lượt HTTP-driven duy nhất: `/health` và `/metadata/openapi.json` công
   khai, 401 khi không có token, create (201), get (200, với capabilities/guard-availability
   được tính đúng xuyên suốt toàn bộ stack), transition (200), update với stale-version (409
   đúng hình dạng error-body), delete (200), get sau khi delete (404). Tất cả đều pass. Đây
   là phần tương đương bằng Rust của `packages/core` — phần tương đương bằng Rust của
   `apps/crm` (một binary mỏng đăng ký các business entity thật và gọi
   `metap_http::build_router`) không nằm trong Migration Order này; chưa có business entity
   nào được viết bằng Rust vào thời điểm này.
9. **Peripherals — hoàn thành (2026-08-07).** `crates/metap-peripherals/`:
   `index_reconciler.rs` (các partial expression index riêng cho từng entity —
   `idx_`/`uniq_`/`gin_` — thông qua `CREATE INDEX CONCURRENTLY IF NOT EXISTS`, được kiểm
   tra trước với `pg_indexes` để mỗi lần chạy lại chỉ tốn công build khi thực sự có gì đó
   thay đổi; cùng lập trường graceful-degradation-khi-DB-trục-trặc như `metadata_drift.rs`),
   `metadata_drift.rs` (log first-boot/drift + upsert `metadata_versions`), và
   `role_assignment.rs` (`get_roles_for_user`/`assign_role`/`revoke_role`/`list_users` —
   phần ghi mà extractor `AuthContext` của `metap-http` không cần đến ở bước 8; extractor đó
   giờ gọi `get_roles_for_user` của crate này thay vì bản copy inline nó bắt đầu với, nên
   giờ chỉ còn một implementation, không còn hai bản có thể lệch nhau). `HealthService` và
   xác minh JWT đã được làm xong ở các bước 2/8 tương ứng, được liệt kê lại ở đây theo kế
   hoạch gốc nhưng không bị làm lại lần hai. 3 unit test (dựng tên index, escape
   SQL-literal/identifier) + 3 e2e test trên Postgres dev: round-trip gán role (bao gồm cả
   case gán trùng hai lần với ON-CONFLICT-DO-NOTHING), phát hiện drift qua hai lần gọi
   `check()` với hash khác nhau, và — khớp với đúng mức độ khắt khe của bộ test TS gốc,
   không chỉ dừng ở "index có tồn tại hay không" — một khẳng định `EXPLAIN` rằng planner của
   Postgres thực sự **chọn** index vừa tạo cho đúng hình dạng biểu thức
   `jsonb_extract_path_text` mà `QueryPlanner` dùng. Tất cả đều pass.

   **Cả 9 bước của Migration Order giờ đã hoàn thành.** `crates/` là một Cargo workspace gồm
   9 crate (`metap-infra`, `metap-metadata`, `metap-permission`, `metap-query`,
   `metap-workflow`, `metap-crud`, `metap-http`, `metap-peripherals`, cộng thêm binary
   `outbox-publisher`), 51 unit test (không phụ thuộc DB, đã xác minh bằng cách chạy với
   `DATABASE_URL` chưa được set) và 19 e2e test (Postgres thật, RabbitMQ thật ở những chỗ
   liên quan, một server HTTP thật bind vào socket thật với JWT RS256 thật) đều pass,
   `cargo build --release --workspace` sạch. Việc port entity `crm.customers` thật và xóa
   hẳn `apps/crm`/`packages/core` cả hai đều nằm ngoài phạm vi ban đầu của Migration Order
   này — cả hai đều đã diễn ra dù vậy, ngay trong cùng ngày, một khi mục "Live Demo" bên dưới
   chứng minh việc port đã hoàn tất; xem mục đó và `docs/roadmap.md` Phase 12 để biết những
   gì đã thay đổi. Các mối quan tâm của Phase 8 Hardening (header tương đương helmet, rate
   limiting, lan truyền `requestId`/`traceId`) vẫn được hoãn lại một cách tường minh, không
   bị âm thầm bỏ qua — đã đóng vào 2026-08-09, xem `docs/roadmap.md` Phase 8.

`packages/platform-react`/`apps/demo` không cần thay đổi gì xuyên suốt quá trình — contract
`/metadata/openapi.json` (xem "Chiến lược Schema & Codegen" ở trên) giữ nguyên ổn định qua
mọi bước.

## Live Demo: `crates/crm-server`

Được xây ngay sau Migration Order để trả lời trực tiếp câu hỏi "cái này có thật sự chạy
được không", chứ không chỉ qua bộ test suite — một binary tương đương `apps/crm` thật sự
(`crates/crm-server/`) nối router của `metap-http` với một boot sequence thật (đăng ký
`crm.customers`, `validate_references`, `metadata_drift::check`, `index_reconciler::reconcile`,
serve), khớp với `buildApp` của `app.ts`. Nó chạy entity `crm.customers` **thật**
(`src/customer_entity.rs`, được port trực tiếp từ
`apps/crm/src/modules/crm/customer.entity.ts`), không phải một fixture `test.*` — đúng
entity mà app TS từng phục vụ, trên cùng một bảng `records`.

**Chạy nó:** `pnpm dev:rs` từ repo root (build + chạy từ `crates/crm-server/`, với
`.env`/`keys/` tự chứa riêng của nó). Mint một token bằng `pnpm mint-token`. Cả `apps/crm`
lẫn `packages/core` — bao gồm cả `.env`/`keys/`/dev script của chúng — đều đã bị xóa một khi
stack này được chứng minh hoàn chỉnh; xem `docs/roadmap.md` Phase 12 và
`crates/dev-tools`/`crates/db-migrate` (thay thế `packages/core/scripts/*.mjs` và
`db:generate`/`db:migrate` của Drizzle) cùng `crates/migrations/` (đúng các file `.sql` mà
Drizzle từng sinh ra, được copy nguyên văn và xác minh bằng cách chạy lại toàn bộ e2e suite
trên một database vừa `db-migrate` xong, trước khi xóa bất cứ thứ gì).

**Đã xác minh live** (2026-08-07), CRUD đầy đủ qua HTTP thật trên binary đang chạy:
`POST /api/crm.customers` (create), `GET /api/crm.customers/:id` (capabilities/transitions
được tính đúng), `GET /api/crm.customers` (list, dữ liệu thật), `POST
/api/crm.customers/:id/transitions/activate` (transition có kiểm tra guard, `draft` →
`active`) — tất cả đều chạy trên database dev thật, không phải fixture dùng-rồi-bỏ.

**Một bug thật thứ hai, chỉ bị bắt được khi thực sự chạy binary** (không bộ unit test lẫn
e2e test nào chạm tới nó): lớp CORS trong `build_router` panic ngay lúc khởi động —
`allow_credentials(true)` kết hợp với `allow_headers(Any)` là không hợp lệ theo đặc tả CORS,
và `tower-http` cưỡng chế điều này bằng một hard panic, không phải lỗi compile. e2e test của
`metap-http` luôn truyền vào một `cors_origins` rỗng, nên đi theo một nhánh khác, chưa từng
được test. Đã fix bằng cách dùng một allowlist header tường minh (`Authorization`,
`Content-Type`, `Accept`) thay vì wildcard, và giờ e2e test truyền vào một danh sách origin
thật để nhánh này luôn được bao phủ. Đáng nêu tên như dữ liệu thứ hai (sau bug default
`data`/status ở bước 7) cho lý do vì sao lần port này liên tục nhấn mạnh việc xác minh live
thay vì tin rằng compile-được/unit-test-xanh là đủ — một số kiểu lỗi chỉ tồn tại lúc runtime,
dưới cấu hình thật.

## Gỡ bỏ TS: Xóa `apps/crm` và `packages/core` (2026-08-07)

Một khi live demo ở trên chứng minh stack Rust đã hoàn chỉnh trên entity nghiệp vụ thật,
`apps/crm` và `packages/core` bị xóa hẳn — `master` và tag `v0.1.0` cả hai vẫn còn đầy đủ
lịch sử TS nếu sau này cần revert lại. Trước khi xóa, ba khoảng trống mà stack Rust chưa cần
đến tính tới thời điểm này đã được lấp đầy, để việc xóa phía TS không để lại thứ gì mà phía
Rust vẫn đang âm thầm phụ thuộc:

- **Khóa JWT** — public key của `apps/crm/keys/` và private key của `packages/core/keys/`
  (app Node chia chúng ra hai thư mục riêng; hai nửa public giống hệt nhau, nên gộp lại là
  an toàn) được chuyển sang `crates/crm-server/keys/`, gitignore giống như bản gốc.
- **Công cụ dev** — `packages/core/scripts/{generate-dev-jwt-keypair,mint-dev-token,
  seed-admin}.mjs` (ba script nhỏ) trở thành các subcommand `gen-keys`/`mint-token`/
  `seed-admin` của `crates/dev-tools` — `seed-admin` gọi đúng
  `metap_peripherals::assign_role` mà bộ e2e test của chính nó đã xác minh, không phải một
  query viết tay mới.
- **Migration schema** — `db:generate`/`db:migrate` của Drizzle không có phiên bản Rust
  tương đương. `packages/core/src/infra/db/migrations/*.sql` (SQL thật đã được sinh ra,
  không phải `schema.ts` — thứ không có lý do gì để port, vì không có gì đọc file *định
  nghĩa* schema lúc runtime cả, chỉ có SQL mà nó từng sinh ra) được copy nguyên văn vào
  `crates/migrations/`, cùng với `crates/db-migrate` (`sqlx::migrate!`) được thêm vào để áp
  dụng chúng. **Đã xác minh trước khi xóa bất cứ thứ gì**: chạy `db-migrate` trên một
  database scratch hoàn toàn mới, xác nhận cả 6 bảng đều xuất hiện, sau đó chạy *toàn bộ* bộ
  e2e suite (cả 19 test) trên chính database từ-đầu đó — pass, chứng minh stack Rust không
  còn cần `packages/core` tồn tại để một môi trường mới có thể dựng lên schema. Từ đây trở
  đi, migration mới là các file `.sql` viết tay trong `crates/migrations/`, không có công cụ
  diff nào cả.

Các script trong `package.json` và `CLAUDE.md` đã được cập nhật cho khớp (lệnh mới, đường
dẫn file mới, cả phần mô tả stack). `packages/platform-react`/`apps/demo` không hề bị đụng
tới — đã xác nhận bằng grep rằng không nơi nào tham chiếu `packages/core`/`apps/crm` theo
đường dẫn (frontend vốn luôn chỉ giao tiếp qua HTTP, chưa bao giờ import trực tiếp), sau đó
`pnpm install` sinh lại lockfile để loại bỏ sạch hai workspace member đã bị xóa.

**Những gì vẫn chưa tồn tại tại thời điểm này, nêu rõ ràng:** các route HTTP admin (policy
CRUD, cấp/thu hồi role qua HTTP — `metap_peripherals::assign_role`/`revoke_role`/`list_users`
đã tồn tại dưới dạng hàm và đã được bao phủ bởi e2e test, nhưng chưa có gì trong `metap-http`
expose chúng thành endpoint cả) và các mối quan tâm ở tầng application của Phase 8 Hardening
(xem ở trên). Cả hai đều là khoảng trống đã biết từ trước lần xóa này, không phải khoảng
trống mới phát sinh do nó, và cả hai từ đó đến nay đều đã được đóng lại — route admin vào
2026-08-08 (`crates/metap-http/src/routes/admin.rs`), phần tầng application của Hardening
vào 2026-08-09 (xem `docs/roadmap.md` Phase 8).

Theo mục "Khả năng tiếp cận cho Contributor / Outsource" bên dưới: các bước 1–3 là phạm vi
hợp lý để một đội nhỏ kiểm chứng năng lực Rust thực sự trước khi bất kỳ ai động vào
`CrudService`/bề mặt HTTP (bước 7–8), nơi một sai sót có phạm vi ảnh hưởng lớn nhất.

## Khả năng tiếp cận cho Contributor / Outsource

Rủi ro có thật, đã được nêu tên, từ mục "Sức hút với contributor" ở trên, được xử lý trực
tiếp thay vì biện minh cho qua: giữ cho việc **tác giả** entity/workflow/permission (phần mà
một contributor thuê ngoài hay một tác giả business-module động vào) mang tính khai báo và
dạng dữ liệu — một danh sách field, một bảng transition workflow — chứ không phải code Rust
idiomatic. Tập trung phần chuyên môn Rust mà bản thân engine cần (`CrudService`,
`QueryPlanner`, `WorkflowEngine`, SPI `EventBus` sau này theo
`docs/architecture-review-2026-08-07.md` Part 2) vào một đội core nhỏ hơn. Điều này vốn đã
gần với hình dạng thực tế hiện nay — các file `*.entity.ts` là những object khai báo đơn
giản dù engine bên dưới là TypeScript phức tạp hơn nhiều — quyết định này chỉ thay đổi ngôn
ngữ của engine, không thay đổi hình dạng những gì một tác giả module viết ra hàng ngày.

## Cấu trúc Repo: Nâng Cargo.toml lên root + Đổi tên `rust/` → `crates/` (2026-08-08)

Hai việc dọn dẹp cấu trúc tiếp theo, cả hai đều được giới hạn phạm vi hẹp sau khi cân nhắc
(và từ chối) một đề xuất rộng hơn về công cụ điều phối lệnh kiểu `justfile`/`Makefile`, coi
đó là còn quá sớm theo đúng lập trường trigger-based/YAGNI của dự án này — chỉ với hai hệ
sinh thái (Cargo, pnpm) và `package.json` đã đóng vai trò điều phối viên, một tầng thứ ba
không có trigger cụ thể nào để biện minh.

- **Nâng root của Cargo workspace lên.** `[workspace]` được chuyển từ một `Cargo.toml` lồng
  bên trong lên `Cargo.toml` ở repo root, khớp với sự tiện lợi ở cấp root mà pnpm vốn đã có
  — `cargo build`/`test`/`clippy` giờ chạy được từ repo root mà không cần `--manifest-path`.
  `Cargo.lock` cũng chuyển theo. Giữ nguyên `resolver = "2"` (không phải `"3"`, thứ cần
  edition 2024; workspace vẫn đang ở edition 2021).
- **Đổi tên `rust/` thành `crates/`.** Thuần túy là đổi tên thư mục, không có thay đổi code
  nào — mọi đường dẫn crate (`crates/metap-*`, `crates/crm-server`, v.v.) được cập nhật
  trong `members` của `Cargo.toml` ở root, trong các script của `package.json` ở root, và
  trong mọi tham chiếu đường dẫn ở doc/source-comment xuyên suốt repo.

Cả hai đều được xác minh sau khi thay đổi, không chỉ compile được: `cargo build --release
--workspace` (12 crate, sạch), 51 unit test (hermetic, `DATABASE_URL` chưa set), toàn bộ
e2e suite chạy trên Postgres dev thật, và một vòng `pnpm dev:rs` + `pnpm mint-token` chạy
live (health check, `GET /api/crm.customers` đã xác thực → 200) trên các đường dẫn mới.

**Một bug thứ ba, không liên quan, bị bắt được trong lượt xác minh này**, đáng ghi lại bên
cạnh hai bug trong Migration Order ở trên: helper `cleanup()` của
`crates/metap-crud/tests/crud_service_postgres.rs` xóa `outbox_events` theo
`aggregate_type = 'test.orders'` — không giới hạn theo tenant — nên dưới chế độ chạy song
song mặc định của `cargo test --workspace -- --ignored`, bước cleanup của một test có thể
xóa mất các dòng outbox còn đang bay của một test khác đang chạy đồng thời. Chạy riêng lẻ
(`--test-threads=1`) thì lần nào cũng pass, nhưng fail không đều đặn khi chạy song song toàn
workspace — cùng kiểu mẫu "chỉ khi chạy thật, chạy đồng thời mới phát hiện ra" như panic CORS
ở trên. Đã fix bằng cách giới hạn phạm vi xóa vào
`aggregate_id IN (SELECT id FROM records WHERE tenant_id = $1)`; chạy lại toàn bộ e2e suite 3
lần sau khi fix, đều xanh (19 e2e test mỗi lần chạy — xem ghi chú bên dưới về số lượng test
của Migration Order).

**Đính chính số lượng test:** Migration Order (ghi chú kết ở bước 9) và `docs/roadmap.md`
Phase 12 cùng ghi "20 e2e test" ở một chỗ và "19" ở chỗ khác — một sự không nhất quán đã tồn
tại từ trước lần đổi tên này. Đếm lại trực tiếp (`grep -rc '#\[ignore' crates/*/tests/*.rs`
đếm dư một cho mỗi file, vì doc comment riêng của mỗi file đều nhắc tới `` `#[ignore]`d ``
trong phần văn xuôi): con số chính xác là **19**, được xác nhận bằng ba lần chạy liên tiếp
`cargo test --workspace -- --ignored` toàn bộ đều xanh sau khi fix ở trên. Con số "20" ở cả
hai tài liệu đã lỗi thời và cần sửa lại thành 19.

## Liên hệ với các tài liệu khác

- Mẫu hình Capability SPI (Level 1/2/3) trong `docs/modular-spi-architecture.md` được đóng
  khung như trung lập về ngôn ngữ khi được viết ra. Với `packages/core` giờ đã cam kết dùng
  Rust, một SPI `EventBus`/`Storage` trong tương lai (nếu và khi trigger riêng của nó xảy ra
  — xem Part 2/thứ tự triển khai của tài liệu đó, không đổi vì quyết định này) sẽ tự nhiên
  là một trait Rust thay vì một interface TypeScript, khớp với chính bản phác thảo
  `trait EventBus { ... }` trong đề xuất gốc. Kỷ luật về số lượng SPI/trigger của tài liệu đó
  không bị ảnh hưởng ở các khía cạnh khác: quyết định này chỉ liên quan đến ngôn ngữ triển
  khai của `packages/core`, không phải lý do để xây sáu SPI còn lại trước khi trigger của
  chúng xảy ra.
- Được ghi lại trong mục "Các quyết định đáng chú ý không có spec riêng" của
  `docs/architectures/09-adr.md`.
