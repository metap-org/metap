# Team Charter

Viết ngày 2026-08-10, trước khi có contributor thứ hai thật sự tham gia — mục tiêu là chuẩn bị sẵn
module ownership và một roadmap có thể chia việc song song *trước khi* việc onboarding tạo áp lực
phải ứng biến chúng tại chỗ. Đây là tài liệu sống: khi có người thật tham gia, thay các nhãn track
trong "Phân công hiện tại" bằng tên thật. Phần còn lại (ranh giới, cách chia work-stream) vẫn nên
đúng bất kể số lượng người.

Tài liệu này bổ sung, không thay thế, các doc đã có:

- `docs/architectures/index.md` — cái gì đã được xây và tại sao (arc42/C4).
- `docs/roadmap.md` — trạng thái theo từng phase, nguồn sự thật duy nhất cho "cái gì đã xong."
- `docs/CONTRIBUTING.md` — quy trình cơ học (branch, check, review) để merge một thay đổi.
- `docs/agile-process.md` — nhịp làm việc: cadence review, Definition of Ready/Done.
- `docs/features/` — brief cho từng tính năng cụ thể (nhỏ hơn một phase), phạm vi + tiêu chí chấp nhận.

Tài liệu này trả lời hai câu hỏi mà các doc trên chưa trả lời: **ai nên review một thay đổi cụ
thể**, và **các phase còn lại chia cho nhiều người làm song song mà không đụng nhau như thế nào**.

## Bắt đầu từ đâu (contributor mới)

Đọc theo thứ tự:

1. `CLAUDE.md` — stack, cấu trúc monorepo, các lệnh, tóm tắt kiến trúc, quy ước bắt buộc.
2. `docs/architectures/index.md` và các phần nó dẫn tới cho khu vực bạn sẽ làm.
3. Bảng Current Status trong `docs/roadmap.md` — tìm phase bạn sẽ nhận.
4. Mục "Module Ownership & Track" bên dưới — tìm phase đó thuộc track nào, và những module bạn sẽ
   đụng vào.
5. `docs/CONTRIBUTING.md` — branch, check bắt buộc, cách route review.

Sau đó dựng dev stack theo mục Commands trong `CLAUDE.md` trước khi viết code.

## Đề xuất một tính năng mới

Nếu việc bạn muốn làm không nằm sẵn trong một phase của `docs/roadmap.md`: viết một feature brief
trong `docs/features/` (template có sẵn ở đó) thay vì bắt đầu code luôn. Track sở hữu module liên
quan (bảng ở dưới) là người duyệt — với team một người như hiện tại, tự duyệt cũng được, miễn là
file đã có đủ phạm vi/tiêu chí chấp nhận trước khi code, đúng Definition of Ready trong
`docs/agile-process.md`. Tính năng đủ lớn thì khi duyệt nên gắn luôn vào một phase mới hoặc phase
đang có trong roadmap, để `docs/roadmap.md` không bị lệch khỏi thực tế đang làm.

## Module Ownership & Track

Hiện chỉ có một contributor duy nhất, nên mọi track dưới đây đang chưa có ai giữ — bảng này tồn
tại để khi có contributor thứ hai, câu hỏi "ai review cái này" và "tôi được đụng tới đâu mà không
cần sign-off chéo track" đã có sẵn câu trả lời, thay vì phải thương lượng theo từng PR.

| Track | Sở hữu | Module |
|---|---|---|
| **Backend Core** | Execution engine không biết business entity: metadata compiler, permission engine, query planner, workflow engine, CRUD orchestration. Bán kính ảnh hưởng lớn nhất — thay đổi ở đây lan sang mọi track khác. | `crates/metap`, `crates/metap-metadata`, `crates/metap-permission`, `crates/metap-query`, `crates/metap-workflow`, `crates/metap-crud` |
| **HTTP/API Surface** | Router axum, auth extractor, shape request/error, security headers, rate limiting. | `crates/metap-http` |
| **Backend Ops/Infra** | Mọi thứ chạy như process/CLI riêng: drain outbox, event consumer, cron dispatch, migration, dev tooling, plumbing config/DB pool/EventBus. | `crates/metap-infra`, `crates/outbox-publisher`, `crates/notification-worker`, `crates/metap-cron`, `crates/cron-scheduler`, `crates/db-migrate`, `crates/dev-tools`, `crates/metap-peripherals` |
| **Frontend Platform** | Thư viện React tái sử dụng mà app khác import: api client, generated CRUD UI, field renderer, permission/auth primitive, shell, admin kit, i18n. | `packages/platform-react` |
| **App/Entity** | Consumer ví dụ cụ thể: đăng ký business entity, wire một binary chạy được, frontend demo harness. Nơi các module nghiệp vụ mới (Phase 7) sẽ nằm. | `apps/crm-server`, `apps/crm-fe` |

Các ranh giới khiến việc phân track có ý nghĩa (danh sách đầy đủ ở
`docs/architectures/02-constraints.md` và `CLAUDE.md`):

- HTTP/API Surface không bao giờ vòng qua Backend Core để đụng thẳng `sqlx`/`lapin`.
- App/Entity không bao giờ bị import ngược lại bởi bất kỳ crate `crates/metap-*` nào — hướng phụ
  thuộc là một chiều (thư viện ← consumer).
- Frontend Platform không bao giờ hardcode tên entity cụ thể, cũng không vượt qua HTTP API để đụng
  vào nội bộ backend.
- Thay đổi cần đụng nhiều track (vd: thêm property mới cho `EntityField`, đụng cả Backend Core lẫn
  generated types của Frontend Platform) cần sign-off từ cả hai track, và thường nên có mục ADR
  (`docs/architectures/09-adr.md`) vì đây đúng kiểu quyết định tốn kém nếu làm lại một mình.

### Phân công hiện tại

| Track | Người phụ trách |
|---|---|
| Backend Core | *(chưa có)* |
| HTTP/API Surface | *(chưa có)* |
| Backend Ops/Infra | *(chưa có)* |
| Frontend Platform | *(chưa có)* |
| App/Entity | *(chưa có)* |

## Các luồng làm song song (những phase roadmap còn lại)

Theo trạng thái `docs/roadmap.md` ngày 2026-08-10, các phase chưa xong/đang làm là 7, 8, 11, 12,
14. Nhóm lại thành các luồng chạy song song được, kèm phụ thuộc giữa chúng để hai người không vô
tình sửa lại cùng một chỗ:

### Stream A — Metadata Control Plane (track Backend Core)

Phần còn lại của Phase 11 (các sub-project của Phase A: runtime loader, publish validation
pipeline, admin API — spec lưu trữ/versioning đã viết ở `docs/low-code-metadata-storage-design.md`,
cần retarget từ bản nháp TS ban đầu sang Rust trước khi implement). Đây là việc nặng về thiết kế
trước khi nặng về code; coi "viết lại spec sang Rust" là một bước review riêng, không gộp vào PR
implement đầu tiên.

**Mở khóa cho:** phần metadata-label translation của Phase 14 (đang bị block bởi stream này — đừng
bắt đầu việc đó độc lập).

**Rủi ro cần phối hợp:** stream này nhiều khả năng nhất sẽ đổi shape của `EntityDefinition`
(`crates/metap-metadata`) và generated types phía frontend. Ai đang làm Stream B để thêm module
entity mới nên đồng bộ trước khi thay đổi schema của stream này được merge, không phải sau.

### Stream B — Module Migration (track App/Entity)

**Đã xong (2026-08-10).** Phase 7 đóng đủ 4/4 module (`crm.customers`, `sales.orders`,
`inventory.movements`, `accounting.journal`) — chi tiết ở `docs/features/demo/`. Pattern
metadata-driven generalize tốt qua field kind/workflow shape/list view khác nhau mà không cần
đổi `crates/metap-*`. Không phát sinh nhu cầu cross-module workflow thật trong lúc làm.

### Stream C — Production Readiness (track Ops/Infra)

Phần còn lại của Phase 8 (tích hợp secret manager, load test, backup/restore drill) và quyết định
cutover thật sự của Phase 12. Cả hai đang bị block rõ ràng trong `docs/roadmap.md` bởi một điều
kiện tiên quyết còn thiếu: **chưa có quyết định về deployment topology cho production**
(`docs/architectures/11-risks.md`).

**Việc đầu tiên của stream này không phải code** — mà là một ADR chọn deployment topology
(`docs/architectures/09-adr.md`): thực tế sẽ chạy ở đâu, secret manager là gì, "production" nghĩa
là gì với một dự án ở giai đoạn này. Mọi thứ khác trong Phase 8/12 phụ thuộc vào quyết định đó và
không nên bắt đầu trước, để tránh harden theo một topology mà sau này lại chọn khác.

### Tóm tắt

```
Stream A (Backend Core)   ──viết spec──▶ implement control plane ──▶ mở khóa Phase 14
Stream B (App/Entity)     ──đã xong (4/4 module, 2026-08-10)
Stream C (Ops/Infra)      ──ADR chọn topology trước──▶ rồi Phase 8 + Phase 12 song song với A/B
```

A, B, C có thể chạy với ba người bắt đầu cùng tuần. Trong một stream, các bước con vẫn phải tuần
tự (spec của Stream A trước khi implement; ADR của Stream C trước khi làm hardening).

## Định hướng đang ghi nhận, chưa có trigger — không phải stream, chưa nên bắt đầu

Nảy sinh từ các buổi thảo luận kiến trúc, hợp lý về mặt sản phẩm nhưng đi trước trigger-based
discipline hiện tại (`docs/architectures/02-constraints.md`'s "Tiến hóa theo trigger"). Ghi lại
ở đây để không quên, không phải để bắt đầu code — mỗi mục cần một feature brief trong
`docs/features/` (trạng thái `proposed`) nêu rõ trigger cụ thể trước khi ai đó bắt tay vào:

- **Workflow hai chế độ** (in-process trong một module, cross-module qua command/event) mà
  cùng một logical model chạy được ở cả hai, không rewrite khi deployment đổi. Đối lập trực
  tiếp với kết luận hiện tại trong `docs/architectures/09-adr.md`: `WorkflowRuntime` là một
  trong các Capability SPI **chưa có trigger**, chưa nên xây. Cần một trigger cụ thể (một
  module thứ hai thật sự cần cross-module workflow — Phase 7/Phase 9) trước khi đảo lại.
- **Workflow visualize được / hướng BPM nhẹ** — chưa có ở đâu trong roadmap hay entity nào hiện
  tại yêu cầu điều này. Giá trị sản phẩm hợp lý cho low-code, nhưng là yêu cầu mới, chưa phải
  kiến trúc đã quyết.
- **Tiny deployment profile** (single binary, SQLite, in-memory EventBus, không cần RabbitMQ)
  — đã được đặt tên trong `docs/modular-spi-architecture.md`'s Deployment Profiles, nhưng chính
  tài liệu đó khuyến nghị "Option 1: giữ một triết lý deployment duy nhất" cho hiện tại. Chọn
  Tiny nghĩa là sửa `docs/architectures/02-constraints.md`'s ràng buộc Postgres/RabbitMQ-duy-
  nhất và kiểm toán dialect Postgres-specific của `QueryPlanner` — một quyết định sản phẩm
  (có nhắm khách self-host không?), không phải gap kỹ thuật.
- **Migration path từ generic `records` table sang bảng riêng cho một entity** — chưa viết ở
  đâu. Chỉ nên viết thành spec khi Data Model Strategy Step 3
  (`docs/architectures/05-building-blocks.md`) thực sự được kích hoạt bởi một nhu cầu hiệu năng
  đo được của một entity cụ thể, không phải chuẩn bị sẵn trước.
- **Computed/derived field** (field tính từ field khác *cùng một record*, ví dụ
  `display_name = first_name + " " + last_name`) — mở rộng tự nhiên từ `searchable`/`sortable`
  đã có sẵn trên `EntityField` (`crates/metap-metadata/src/entity.rs`): rule đề xuất là field
  computed mặc định không searchable/sortable trừ khi được materialize. Hai điểm cần chốt
  trước khi viết spec: (1) recompute phải chạy trong pipeline có sẵn của `CrudService`
  (validate → recompute → write), không phải một layer riêng, để REST/webhook/cron không tính
  ra kết quả khác nhau; (2) `depends_on` nên giới hạn trong cùng record — cho phép phụ thuộc
  record khác biến việc này thành bài toán materialized view/CQRS, không còn đơn giản là
  "recompute on write" nữa. Trigger: một entity thật sự cần field dạng này để search/sort.
- **Schema versioning cho entity** (metadata và record cùng mang `schema_version`, physical
  mapping field → JSONB path đổi theo version, ví dụ `v2: data->>'total'` vs.
  `v3: data->'pricing'->>'total'`) — đối lập trực tiếp với cách `QueryPlanner` hoạt động hôm
  nay: `field_expression()`/`sort_field_expression()`
  (`crates/metap-query/src/condition_to_sql.rs`, `crates/metap-query/src/query_planner.rs`)
  map thẳng một `field_name` sang một path JSONB cố định duy nhất, không biết đến version. Cần
  quyết định trước cách filter/sort xử lý khi nhiều record cùng entity nhưng khác
  `schema_version` nằm trong cùng một query. Cũng cần đặt tên khác cột `version` đã có sẵn
  trong `records` (`crates/migrations/0000_*.sql`, đang dùng cho optimistic locking) để tránh
  đụng độ tên. Trigger: một entity thật sự cần đổi field shape mà không muốn migrate toàn bộ
  record cũ ngay lập tức.
- **Organization & Identity layer — P1/P2 còn lại** (P0 đã xong, xem `docs/roadmap.md` Phase 18
  và `docs/architectures/09-adr.md`; mục này chỉ còn giữ phần chưa code). P0 đóng đúng gap hẹp đã
  phát hiện 2026-08-22: RBAC+ABAC (`metap-permission`) đã đủ biểu đạt lực cho "role có scope" (vd
  "Sales Manager chỉ áp dụng trong phòng Sales"), chỉ thiếu chỗ để `RequestContext` mang attribute
  của caller — nay đã có (`context_attributes`, opt-in qua `AUTH_CONTEXT_ENTITY`, có cache). Còn
  lại, chưa có trigger để code: `hr.positions`/`hr.locations` (P1), `Employee.managerId`
  self-reference + policy "chỉ manager trực tiếp" (P1, dùng cross-record condition đã có — Phase
  3 #3, 2026-08-21), Legal Entity/Business Unit/Cost Center/Approval Authority/Org Chart
  visualize (P2). Chi tiết phân kỳ đầy đủ ở `docs/features/03-organization-identity.md`.
  Nghiên cứu 2026-08-22 (đối chiếu với table-per-entity) cũng lộ ra một gap reference-integrity
  độc lập, đã đóng cùng ngày (`docs/roadmap.md`'s fix note trước Phase 18) —
  `docs/features/03-organization-identity.md`'s mục "Quan hệ với table-per-entity" giữ chi tiết
  nghiên cứu đó.
- **Metadata low-code theo từng Tenant** (tenant tự định nghĩa entity riêng, thay vì mọi tenant
  dùng chung một tập entity DB-authored như hôm nay) — ghi lại 2026-08-22 từ thảo luận sau khi
  Phase 18 xong, không phải yêu cầu cấp thiết, mà chốt hướng dài hạn cho Phase 11C's "quy tắc cô
  lập schema cấp Tenant" (mục đã ghi ở Phase 11, `docs/roadmap.md`, hiện chưa có trigger). Rà
  code thật trước khi phác hướng (không đoán):
  - **Chỉ tầng DB-authored (`metap-lowcode`) đổi, code-authored giữ nguyên global.** Quyết định
    Phase A (`docs/low-code-metadata-storage-design.md`: "Metadata toàn cục, không phải theo
    từng tenant, cho Phase A... hoãn lại một cách tường minh") đã tự đóng khung đúng ranh giới
    này — không cần đảo `apps/crm-server/src/entities/*.rs`, chỉ mở rộng cơ chế `merge_with`
    (base cố định + extra) đã có, đổi "extra" từ một tập entity toàn cục thành một tập theo từng
    tenant.
  - **Storage**: `low_code_entity_drafts`/`low_code_entity_versions`
    (`crates/migrations/0010_low_code_entities.sql`) thêm cột `tenant_id`, khóa chính/unique đổi
    từ `entity_name` sang `(tenant_id, entity_name)` — kéo theo sửa mọi hàm public của
    `metap-lowcode::store` (draft/publish/rollback/list/export/import) để nhận thêm tham số
    `tenant_id`, một diff cơ học không nhỏ (tương tự quy mô diff mà `audit.rs`'s doc comment đã
    né khi không thread `actor` qua transaction của `store.rs`).
  - **Registry resolution — phần khó thật.** `AppState.metadata` hôm nay là MỘT
    `Arc<ArcSwap<MetadataRegistry>>` toàn cục, build một lần bằng cách merge `metadata_base` với
    toàn bộ low-code entity đã publish. Theo tenant nghĩa là mỗi tenant cần registry riêng —
    build tươi mỗi request (theo đúng triết lý "role luôn tra mới") quá đắt cho việc này (một
    lần merge phải validate lại toàn bộ field/list-view của mọi entity tenant đó, nặng hơn nhiều
    một role lookup). Hướng hợp lý hơn: một cache theo tenant, cùng mẫu
    `metap-control::RegistryCache`/`metap_http::cache::ContextAttributesCache` đã có hai lần
    trong repo — nhưng khác `ContextAttributesCache` ở chỗ invalidate nên là **explicit-trên-ghi
    là chính, TTL chỉ là backstop**: `publish`/`rollback` đã đi qua đúng một code path, gọi
    `.invalidate(tenant_id)` ngay tại đó cho hiệu lực tức thì hợp lý hơn nhiều so với chấp nhận
    độ trễ TTL như `context_attributes` (ở đó operator sửa dữ liệu qua ghi record thường, không
    có một hàm "publish" duy nhất để móc vào).
  - **`metap-lowcode-http` hôm nay dùng thẳng `state.pool`, không qua `Router`** (đã kiểm tra
    trực tiếp: mọi handler trong `crates/metap-lowcode-http/src/lib.rs` gọi
    `metap_lowcode::*(&state.pool, ...)`) — **đúng** cho thế giới global hôm nay (metadata sống ở
    control-plane DB, không thuộc riêng tenant nào). Nếu metadata theo tenant, đây sẽ tái hiện
    đúng loại gap Phase 16 đã đóng một lần cho role lookup/`PostgresPolicyStore` ("vẫn dùng
    `AppState.pool` trực tiếp, không qua Router") — nghĩa là mọi handler này cũng cần đi qua
    `Router::begin(tenant_id)`, và một câu hỏi thiết kế chưa trả lời: bảng metadata theo tenant
    nên sống Ở ĐÂU — cùng chỗ `Router` đã route dữ liệu của tenant đó (tự nhiên với tenant
    `DedicatedDb`, nhưng metadata giờ nằm rải rác nhiều DB vật lý), hay tập trung một bảng ở
    control-plane DB kèm cột `tenant_id` (đơn giản hơn để truy vấn tổng hợp, nhưng phá bất biến
    "dữ liệu tenant `DedicatedDb` sống trong DB riêng của họ" mà control plane đang giữ cho dữ
    liệu nghiệp vụ). Chưa quyết — để lại cho lúc có trigger thật.
  - **Blast radius ra ngoài backend**: `GET /metadata/openapi.json` hôm nay là MỘT schema toàn
    cục, public, không cần token — pipeline codegen frontend
    (`pnpm --filter @metap/platform-react generate:types`) giả định đúng một schema duy nhất.
    Theo tenant nghĩa là endpoint này cần biết "hỏi cho tenant nào", và bước codegen (chạy lúc
    dev, không phải lúc runtime của một tenant cụ thể) cần một câu trả lời riêng — chưa nghĩ tới,
    không hand-wave ở đây.
  - **Trigger: chưa có.** Tầng data (bảng `records` JSONB) đã cho mỗi tenant dữ liệu hoàn toàn
    riêng trên cùng một schema — "mỗi tenant là một business" hôm nay đã đúng ở mức dữ liệu.
    Chưa tenant nào trong repo cần entity/field *khác nhau về shape* so với tenant khác. Khi
    trigger đó xảy ra thật, viết feature brief riêng (`docs/features/`) trước khi code, đúng quy
    trình.
- **Cross-entity relations trong list view — Mode 1 (denormalize lúc ghi) và Mode 3 (JOIN thật)**
  (Mode 2 — batch-hydrate `refDisplayField` sau khi list — đã xong 2026-08-22, xem
  `docs/roadmap.md` và `docs/features/05-cross-entity-relations.md`; mục này chỉ còn giữ 2 hướng
  chưa code). Mode 1: một cờ metadata denormalize field display vào record lúc ghi — rẻ, nhưng
  lệch dữ liệu nếu record liên quan đổi giá trị sau đó; trigger là một nghiệp vụ thật cần snapshot
  (audit trail, hoá đơn), chưa entity nào cần. Mode 3: JOIN thật trong `QueryPlanner` cho phép
  filter/sort xuyên entity — nặng nhất, đụng cả `metap-query` (module rủi ro cao nhất theo ADR),
  `metap-permission` (record-level condition cho `list()`, đang cố tình bị chặn vì lý do này), và
  keyset pagination cursor; trigger là nhu cầu filter/sort thật theo field của entity liên quan
  (Mode 2 đã đủ cho hiển thị). Chi tiết đầy đủ ở feature brief trên.
- **Entity variant kiểu polymorphic/discriminated-union** (một entity logic chứa nhiều "hình
  dạng" record khác nhau trong cùng một logical collection, kiểu MongoDB) — rủi ro cao nhất
  trong ba ý mới này, vì `EntityDefinition.fields` hôm nay là một danh sách phẳng dùng chung
  cho validation, `list_views`/filters, OpenAPI generator, và codegen type phía FE
  (`packages/platform-react/src/metadata/generated-types.ts`); thêm variant nghĩa là lồng thêm
  một tầng `variant → fields` ở tất cả các chỗ đó, không chỉ thêm một cột `variant` vào
  `records`. Trigger: một entity thật sự cần nhiều schema khác nhau trong cùng logical
  collection — chưa entity nào trong repo hôm nay cần điều này.

- **Tầm nhìn application platform dài hạn (Jira/Confluence-style apps trên metadata, rồi
  Durable Workflow Runtime kiểu Temporal/Cadence)** — ghi lại 2026-08-20 từ một brainstorm trước
  đó bị dán nhầm vào `checklist.txt` (đã dọn khỏi file đó cùng ngày). Đào sâu hơn hai bullet đã
  có ở trên (workflow hai chế độ, workflow visualize/BPM nhẹ), không phải ý mới độc lập:
  - **State Machine và Workflow là hai primitive tách biệt, compose với nhau** — State Machine
    (state/transition/guard/action) mô tả một entity đang ở đâu và được phép chuyển đi đâu;
    Workflow (trigger/condition/activity/timer/event) mô tả một chuỗi việc có thể kéo dài, và có
    thể vừa lắng nghe state-transition làm trigger, vừa gọi ngược lại một transition như một
    action. Khác với `WorkflowTransition.guard` hiện tại (`crates/metap-metadata/src/entity.rs`)
    — guard đó *là* `PolicyCondition`, tức đã là state-machine-guard, không phải một workflow
    engine riêng; ý ở đây là thêm một lớp Workflow *phía trên* state machine đã có, không thay
    thế nó.
  - **Roadmap 5 level tham khảo**: (1) metadata-driven CRUD — hiện tại, xong; (2)
    metadata-driven application (Jira/Confluence/CRM/ERP dựng chủ yếu bằng entity/field/view
    definition, không viết CRUD riêng cho từng loại) — cần metadata mô tả thêm
    Action/Command/Event/Condition/State/Transition/Policy/Workflow/Trigger/Job/Schedule, không
    chỉ Entity/Field/Relation/View như hôm nay; (3) metadata-driven workflow — engine Workflow
    nói trên; (4) durable workflow runtime (retry/timeout/timer/signal/replay/idempotency,
    kiểu Temporal) — khác hẳn về độ khó so với `WorkflowEngine` hiện tại (chỉ atomic transition +
    guard + audit, không có gì persist execution state giữa các bước); (5) distributed workflow
    platform. Metap hôm nay đứng ở level 1, mới bắt đầu chạm level 2 qua Phase 11 (low-code
    metadata).
  - **Trigger đã xảy ra — quyết định ưu tiên trực tiếp từ chủ dự án, 2026-08-21.** Rà lại code
    thật trước khi lên kế hoạch (không đoán): State Machine hiện tại
    (`crates/metap-workflow/src/lib.rs`, 272 dòng) chỉ là 3 hàm thuần
    (`get_initial_status`/`find_transition`/`run_guard`) — không giữ execution state riêng,
    transition là một UPDATE atomic trong `CrudService::transition`. Không cần refactor gì để
    "tách" nó — nó vốn đã không biết gì về trigger/schedule/activity. Bất ngờ hơn: `metap-cron`
    (Phase 13) đã là ~70% hạ tầng Workflow cần — trigger schedule (`cron_jobs`), activity gọi
    transition/webhook, reliable dispatch qua outbox, audit (`cron_job_runs`). Cái thật sự thiếu:
    (1) trigger "on state transition" (subscribe `<entity>.workflow.transitioned`, chưa ai lắng
    nghe ngoài `notification-worker` chỉ log), (2) chuỗi nhiều activity (`cron_jobs` mỗi job chỉ
    1 target), (3) `wait_event`/durable pause (phần khó nhất — cần bảng lưu "đang dừng ở đâu"),
    (4) retry-with-backoff cho activity (gap đã ghi từ Phase 5). Kế hoạch chi tiết + phạm vi
    tăng-dần (increment 1: on-transition trigger tái dùng dispatch có sẵn; increment 2: chuỗi
    activity tuần tự; increment 3: wait_event) nằm ở feature brief
    `docs/features/02-workflow-engine.md` (trạng thái `approved` — kiến trúc "tiến hoá
    `metap-cron`" đã chốt 2026-08-21, sẵn sàng bắt đầu code Increment 1).
- **Tách monorepo thành nhiều repo trong một GitHub organization** (platform public, multi-tenancy
  management + low-code private, và tách hẳn frontend ra thành repo riêng nữa) — ghi lại
  2026-08-21. Trigger cụ thể: **lúc đổi remote GitHub hiện tại sang một organization** — chưa
  làm gì trước đó.
  - **`low-code` (`metap-lowcode`/`metap-lowcode-http`) tách được ngay, không cần refactor** —
    kiểm tra trực tiếp dependency graph (`Cargo.toml` của từng crate) xác nhận zero-coupling
    một chiều: `metap-lowcode-http` phụ thuộc `metap-http`, nhưng không chiều ngược lại — đúng
    chủ đích thiết kế "optional platform capability" đã ghi trong doc comment của chính crate đó
    từ đầu.
  - **`metap-control` (multi-tenancy — `Router`, `PostgresPolicyStore`, tenant provisioning)
    CHƯA tách sạch được** — không phải add-on như low-code, mà đã hàn cứng vào core execution
    path từ Phase 16 Giai đoạn 1 (`CrudService` mở transaction qua `Router::begin`, không phải
    `&PgPool` trực tiếp): `metap-crud` và `metap-http` cả hai đều `metap-control = { path = ... }`
    thẳng trong `Cargo.toml`, không qua trait nào. Tuần 2026-08-20 còn dính chặt hơn nữa —
    `PostgresPolicyStore` (impl RBAC/policy Postgres duy nhất có thật) chuyển từ
    `metap-permission` sang sống trong `metap-control` (lý do: né dependency cycle
    `metap-metadata -> metap-permission`, `metap-peripherals -> metap-metadata`, `metap-control
    -> metap-peripherals`). Tách repo ngay bây giờ nghĩa là repo "platform public" không tự
    build được — thiếu `metap-control` là `metap-crud`/`metap-http` compile fail.
  - **Việc cần làm trước khi tách `metap-control`** (chưa bắt đầu, chờ đúng trigger ở trên): đưa
    `Router`/`PostgresPolicyStore` ra sau một trait mà `metap-crud`/`metap-http` tự định nghĩa
    (cùng pattern `EventBus`/`SecretStore` đã dùng) — platform public repo giữ một impl
    single-tenant "trivial" mặc định, repo multi-tenancy private cung cấp impl SaaS thật implement
    trait đó. Chi tiết Vault production-readiness (HA/unseal/backup — một mối quan tâm liên quan
    nhưng khác, thuộc về lúc chọn hạ tầng production) nằm ở
    `docs/architectures/07-deployment.md`'s "Vault production readiness — open questions".
  - Tách frontend (`packages/platform-react`/`apps/crm-fe`) ra repo riêng — chưa đánh giá coupling
    cụ thể (frontend chỉ giao tiếp qua HTTP, nên về nguyên tắc là sạch nhất trong 4 mảnh), để lại
    đánh giá chi tiết khi trigger thật sự xảy ra thay vì đoán trước.
