# Architecture Review — 2026-08-07

Status: exploratory (review only — no code changed, no phase status changed)

**Superseded, 2026-08-07, same day:** khuyến nghị trọng tâm của review này (Part 1's Event
finding / Part 2 / Part 4 — trích xuất một `EventBus` interface như một refactor TS nhỏ) đã
bị vượt qua bởi một quyết định lớn hơn nhiều được đưa ra cùng ngày hôm đó: `packages/core`
chuyển hoàn toàn sang Rust (`docs/rust-core-viability.md`). Rust port đó, theo Migration
Order của nó, đã xây dựng `crates/metap-infra`'s `EventBus` trait như một phần của việc
triển khai lại toàn diện, chứ không phải như phần trích xuất TS độc lập mà review này đề
xuất — vì vậy hãy coi các *quan sát* của tài liệu này về codebase TS (vẫn thực, vẫn chính
xác như một snapshot của những gì thực sự đang được triển khai) là bản ghi lịch sử, còn các
*khuyến nghị* của nó thì đã bị supersede chứ không còn là một TODO list còn hiệu lực. Câu
hỏi về deployment-profile ở Part 3 và lập luận "đừng xây sáu SPI còn lại khi chưa có trigger"
ở Part 2 là hai điều duy nhất từ review này vẫn còn thực sự mở — xem
`docs/modular-spi-architecture.md` để biết chúng đã đi đến đâu.

## Purpose and Method

Đây là một review kiểu "lead architect" về kiến trúc hiện tại của Metap, được viết theo
brief trong `READ.md` ở gốc repo: hiểu những gì đang tồn tại, chỉ ra các điểm mạnh/nút thắt/
abstraction còn thiếu, và khuyến nghị các cải tiến *incremental* — không bao giờ là một cuộc
viết lại — mỗi khuyến nghị đều trả lời tại sao nó cần thiết, nó giải quyết vấn đề gì, có
backward compatible hay không, và tốn bao nhiêu công sức.

Tài liệu này được viết sau khi đọc lại toàn bộ tập `docs/architectures/` (các mục arc42
1-12), `docs/roadmap.md`, `docs/why.md`, `docs/vision.md`, `docs/low-code-platform-v1.md`, và
đối chiếu các khẳng định trong docs với source code thực tế (`container.ts`, các service
permission/workflow/outbox, RabbitMQ publisher). Khi khẳng định của một tài liệu và code mâu
thuẫn nhau, code là bên đúng, và sự mâu thuẫn đó được nêu ra bên dưới.

Tài liệu này không thay thế `docs/roadmap.md` với vai trò nguồn sự thật (source of truth) về
tình trạng các phase — nó là một input cho roadmap đó. Khi một khuyến nghị bên dưới kéo theo
công việc mới, công việc đó vẫn cần được thiết kế và ghi lại (trong file `docs/*.md` liên
quan và trong `docs/architectures/09-adr.md`) trước khi bắt tay vào xây dựng bất cứ thứ gì.

## Executive Summary

Phần lõi của Metap được xây dựng tốt và có kỷ luật hiếm thấy về việc tiến hóa dựa trên
trigger (trigger-based evolution) — phần lớn những gì một review "thêm các lớp abstraction"
kiểu chung chung sẽ đề xuất thì đã được chủ động *không* xây dựng, vì những lý do chính đáng
và đã được ghi lại (không có `BaseRepository`, không có report query path, không có GraphQL
gateway, không có gRPC). Kỷ luật đó nên được giữ nguyên, không nên bị review này ghi đè.

Nơi duy nhất mà kỷ luật đó gây ra một chi phí thực sự, trong ngắn hạn, là **event
publishing**: mọi dòng code liên quan đến outbox đều phụ thuộc vào một kiểu cụ thể là
`RabbitPublisher`, không có ranh giới interface nào cả — khác với việc lưu trữ permission,
vốn đã có sẵn đúng loại seam này (`PolicyStore`). Đây là khuyến nghị có đòn bẩy cao nhất
trong toàn bộ review này (Part 2).

Một phát hiện thứ hai là một **lỗi tài liệu bị lệch (documentation drift)**: phần mô tả
`WorkflowEngine` trong `CLAUDE.md` nói rằng logic transition/guard "chưa được triển khai,"
nhưng `docs/roadmap.md` (bộ theo dõi phase có thẩm quyền) lại đánh dấu Phase 5 (guard
conditions, atomic transitions, optimistic locking) là **Done**, và `POST
/api/:entity/:id/transitions/:action` đã tồn tại và đã được test. `CLAUDE.md` là tài liệu
đầu tiên được đọc trong mọi phiên làm việc trong tương lai (dù là người hay agent) — một dòng
đã lỗi thời ở đó có nguy cơ khiến ai đó xây lại logic guard đã được ship, hoặc né tránh không
dùng nó vì tưởng nhầm là nó chưa tồn tại. Cách sửa rất đơn giản (Part 4, item 1).

Mọi thứ còn lại — Repository abstraction, việc hoán đổi WorkflowRuntime, GraphQL, một
Scheduler, CacheProvider — đều đúng đắn khi *chưa* được xây dựng, và review này khuyến nghị
**không** xây dựng chúng lúc này — mỗi mục được đề cập bên dưới kèm trigger cụ thể sẽ là lý
do chính đáng để xây dựng nó.

Câu hỏi mở duy nhất mà review này nêu ra thay vì trả lời là một **quyết định về
deployment-profile**: `docs/architectures/02-constraints.md` ràng buộc Postgres và RabbitMQ
là datastore/broker duy nhất. Một profile "Tiny" (SQLite, single-binary) — kiểu mà prompt của
READ.md hỏi đến — sẽ đòi hỏi phải sửa đổi ràng buộc (binding constraint) đó. Đó là một quyết
định sản phẩm, không phải một lỗ hổng kỹ thuật, và review này chủ động dừng lại, không đưa ra
quyết định đó (Part 3).

---

## Part 1: Component-by-Component Review

### Module

**Quan sát:** `apps/<module>` cho mỗi pnpm workspace member; `apps/crm` là module duy
nhất hiện có; `packages/core` không có bất kỳ kiến thức nào về business-entity
(`buildApp(config, entities)` nhận entities như một tham số). Tên entity đã được đặt theo
kiểu dot-namespaced (`crm.customers`) như một điểm neo cho một ranh giới service theo
module trong tương lai.

**Vấn đề:** Không có gì chặn. Phase 7 (Module Migration Strategy) chưa bắt đầu, nên pattern
`apps/<module>` chưa được kiểm chứng với một module thứ hai thực sự — các unknown unknowns
như các mối quan tâm frontend dùng chung giữa nhiều module (ví dụ: dev harness của
`apps/demo` có scale gọn gàng lên nhiều module's entities hay không?) sẽ không lộ ra cho đến
khi đó.

**Khuyến nghị:** Không thay đổi gì lúc này. Khi module thứ hai (Phase 7) được xây dựng,
hãy để ý phần boilerplate lặp lại giữa các `apps/<module>/{main.ts,.env.example,workers/}` —
chỉ nên đưa vào một generator/template khi một module *thứ ba* xác nhận pattern đó lặp lại
(nhất quán với lập trường trigger-based của dự án này, không đi trước nó).

**Tác động:** Không (deferred).
**Khả năng tương thích:** N/A.
**Tiến hóa trong tương lai:** Chuẩn bị trực tiếp cho trigger tách multi-service của Phase 9.

---

### Entity

**Quan sát:** `EntityDefinition` = Zod schema + fields + list views + workflow, được viết
bằng code trong `*.entity.ts`, được validate và hash bởi `MetadataCompiler` tại thời điểm
`MetadataRegistry.register()` (lỗi phát hiện lúc boot, không phải lúc request đầu tiên).

**Vấn đề:** 100% được viết bằng code ngày nay — đây *chính là* khoảng trống của Phase 11,
không phải một bug. Không tìm thấy vấn đề nào khác.

**Khuyến nghị:** Tiến hành Phase A sub-project 1 như đã được scope sẵn trong
`docs/low-code-metadata-storage-design.md` (spec đã được viết, nhưng chưa có plan — xem Part
4). Giữ `crm.customers` là code-authored; chứng minh đường đi DB-authored trên một entity mới
trước, đúng như spec đó đã chốt sẵn.

**Tác động:** Trung bình — thêm một nguồn metadata mới; là bổ sung (additive) vào
`MetadataRegistry`, không thay thế đường đi code-authored.
**Khả năng tương thích:** Hoàn toàn backward compatible theo chính thiết kế của spec.
**Tiến hóa trong tương lai:** Đây là con đường dẫn vào toàn bộ low-code control plane (Phase 11).

---

### Repository

**Quan sát:** Không có interface `Repository`/`StorageProvider` nào ở bất cứ đâu. Kiểu
`Database` được backing bởi Drizzle được inject trực tiếp, như một kiểu cụ thể, vào ~9
service (`CrudService`, `OutboxService`, `PostgresPolicyStore`,
`QueryPlanner`/`condition-to-sql.ts`, `IndexReconciler`, `MetadataDriftService`,
`HealthService`, `RoleAssignmentService`, `container.ts`). Phase 1 của roadmap đã chủ động
chọn **không** xây dựng `BaseRepository`/`TransactionManager`, mà dùng thẳng
`db.client.transaction()` của chính Drizzle — một quyết định YAGNI có chủ đích, không phải
một thiếu sót.

**Vấn đề:** Không có vấn đề gì *hiện tại*. Đây chỉ thực sự là vấn đề nếu (a) một datastore
thứ hai thực sự cần thiết, hoặc (b) một deployment profile Tiny/SQLite trở thành mục tiêu
thực sự (Part 3). Chưa điều nào trong hai điều đó xảy ra.

**Khuyến nghị:** **Không** nên đưa vào một abstraction Repository/StorageProvider lúc
này — không có trigger nào, và làm vậy sẽ trực tiếp mâu thuẫn với lập luận đã từng loại bỏ
`BaseRepository` ở Phase 1. Nếu quyết định về Tiny-profile ở Part 3 sau này được đưa ra theo
hướng khẳng định, cần lưu ý rằng seam thực sự không phải là "các động từ CRUD" — mà là SQL
được sinh ra bởi `QueryPlanner`, vốn mang tính đặc thù Postgres-dialect ở nhiều chỗ
(`jsonb_extract_path_text`, `plainto_tsquery('simple', ...)`, và phần dựng `WHERE` cho
keyset-pagination). Một abstraction `StorageProvider` trong tương lai nên được scope quanh
bề mặt đó, không phải một interface repository theo từng entity kiểu chung chung.

**Tác động:** N/A trừ khi có trigger.
**Khả năng tương thích:** N/A.
**Tiến hóa trong tương lai:** Gắn liền với quyết định Tiny-profile (Part 3), không tách rời khỏi nó.

---

### API

**Quan sát:** REST qua Fastify, một họ route generic duy nhất `/api/:entity`, OpenAPI
được sinh tự động tại `/metadata/openapi.json`. Không có GraphQL.

**Vấn đề:** Không có. Chỉ có một frontend duy nhất (`apps/demo`), chưa có nhu cầu tổng hợp
dữ liệu xuyên service nào tồn tại.

**Khuyến nghị:** Giữ REST. GraphQL BFF vẫn giữ nguyên tính trigger-based như
`docs/architectures/04-strategy.md` đã nêu (≥2 module mà dữ liệu của chúng cần được một màn
hình frontend tổng hợp lại) — review này không tìm ra lý do gì để đẩy trigger đó sớm hơn.

**Tác động / Khả năng tương thích / Tiến hóa trong tương lai:** Tái khẳng định quyết định hiện có; không
khuyến nghị thay đổi.

---

### Workflow

**Quan sát:** `WorkflowEngine` là một state machine điều khiển bởi metadata — state
field, initial state, terminal states, transitions, guard là predicate TypeScript, atomic
transitions với optimistic locking, một audit log `workflow_events` chỉ-ghi-thêm
(append-only), và một sự kiện outbox post-commit. `docs/roadmap.md` Phase 5 đánh dấu tất cả
những điều này là **Done**, đã test, và được expose tại `POST
/api/:entity/:id/transitions/:action`.

**Vấn đề (doc bị lệch so với thực tế — phát hiện có thật):** Phần "Core services and their fixed
boundaries" trong `CLAUDE.md` vẫn mô tả `WorkflowEngine` là "hiện chỉ gán initial status và
emit một sự kiện outbox `<entity>.record.created` khi create; logic transition/guard chưa
được triển khai." Điều đó đã lỗi thời so với trạng thái thực tế, đã test, đã ship của Phase
5. Vì `CLAUDE.md` là tài liệu đầu tiên được nạp vào mọi phiên làm việc trong tương lai (dù
người hay agent), sự lệch pha này có nguy cơ khiến ai đó hoặc triển khai lại logic guard đã
ship, hoặc né tránh xây dựng dựa trên nó vì tin nhầm rằng nó không tồn tại.

**Vấn đề (thuộc kiến trúc, không khẩn cấp):** Guard hiện là các hàm predicate TypeScript thuần.
Phase B của Phase 11 (Builder UI + Safe Runtime Rules) cần một mô hình condition khai báo
(declarative) thay vì vậy — "không thực thi mã tùy ý do user viết" là một ràng buộc V1 tường
minh trong `docs/low-code-platform-v1.md`. Chưa cấp bách: Phase B chưa bắt đầu.

**Khuyến nghị:**
1. Sửa dòng đó trong `CLAUDE.md` ngay bây giờ (Part 4, item 1) — chỉ là sửa tài liệu, không
   cần thiết kế gì thêm.
2. Khi Phase B bắt đầu, tái sử dụng `PolicyCondition`
   (`src/core/permission/policy-condition.ts`) làm hình dạng khai báo cho workflow guard
   thay vì phát minh ra một ngôn ngữ condition thứ hai. Nó đã giải quyết đúng vấn đề này —
   "condition khai báo, không scripting" — cho policies rồi; một union
   `guard: WorkflowGuardFn | PolicyCondition` cho phép các guard dạng hàm thuần vẫn hoạt
   động trong quá trình migrate, nên đây là bổ sung (additive), không phải một thay đổi phá
   vỡ (breaking change), bất kể khi nào nó được thực hiện.

**Tác động:** Item 1 là việc nhỏ. Item 2 (deferred) sẽ chạm vào type của `WorkflowTransition`
và `WorkflowEngine.runGuard`, nhưng chỉ theo hướng bổ sung.
**Khả năng tương thích:** Hoàn toàn backward compatible ở cả hai.
**Tiến hóa trong tương lai:** Item 2 trực tiếp mở đường cho Phase B; việc tái sử dụng
`PolicyCondition` cũng có nghĩa là khả năng debug guard theo kiểu `PolicyExplainer` gần như
có sẵn miễn phí sau này.

---

### Permission

**Quan sát:** RBAC + ABAC, gán role động dựa trên DB, thực thi ở mức field/record,
`PolicyExplainer` phục vụ debug. `PolicyStore` là một interface thực sự, được triển khai bởi
`PostgresPolicyStore` — **seam duy nhất hiện có** trong toàn bộ codebase tách một service
khỏi phần lưu trữ cụ thể của nó.

**Vấn đề:** Không tìm thấy vấn đề gì. Bản thân `PermissionService` là một class cụ thể,
không nằm sau một interface — nhưng chỉ có đúng một implementation và không có implementation
thứ hai khả dĩ nào, nên việc không abstraction hóa nó là đúng đắn.

**Khuyến nghị:** Không thay đổi. Thành phần này là mô hình nên được noi theo ở nơi khác
(xem Part 2), không phải thứ cần chỉnh sửa.

**Tác động / Khả năng tương thích / Tiến hóa trong tương lai:** N/A — không đề xuất thay đổi.

---

### Event (Outbox + RabbitMQ)

**Quan sát:** `OutboxService` ghi vào `outbox_events` trong cùng transaction với business
write; publisher worker poll mỗi 1s, claim các row bằng `FOR UPDATE SKIP LOCKED` (đã được
sửa để tránh double-publish), và publish qua một `RabbitPublisher` cụ thể
(`packages/core/src/infra/messaging/rabbitmq.ts`, tên exchange hardcode `"metap.events"`,
import trực tiếp `amqp`). Constructor của `OutboxService` nhận `RabbitPublisher` theo kiểu
cụ thể. Không có interface `EventBus`/`MessagePublisher` nào tồn tại ở bất cứ đâu.

**Vấn đề 1 (đã được theo dõi):** Outbox worker giữ transaction DB của nó mở trong suốt cuộc
gọi publish RabbitMQ — `docs/architectures/11-risks.md` đã nêu vấn đề này rồi, trích dẫn đề
xuất của một review bên ngoài (claim-short-tx / publish-outside / lease-reclaim khi thất
bại) là chưa được thực hiện, do chưa đo được contention thực tế. Review này không có gì thêm
ngoài việc xác nhận vấn đề này là thực và đúng đắn khi vẫn chưa được kích hoạt (untriggered).

**Problem 2 (phát hiện mới — khuyến nghị chính của review này):** Không có interface
`EventBus` nào tồn tại, khác với `PolicyStore`. Điều này quan trọng cụ thể vì trigger
multi-service của Phase 9 và bất kỳ thay đổi broker nào trong tương lai (ví dụ: Kafka một
khi một module thứ hai tồn tại và throughput thực sự trở thành vấn đề) hiện *không có gì* để
dựa vào — mọi call site sẽ phải thay đổi, chứ không chỉ một điểm inject duy nhất.

**Khuyến nghị:** Trích xuất một interface `EventBus` ngay bây giờ, theo đúng tiền lệ của
`PolicyStore`: hình dạng `publish(topic, payload)` hiện có của `RabbitPublisher` đã đúng
rồi — chỉ cần nâng cấp kiểu của nó thành một interface (`EventBus`) và đổi tham số
constructor của `OutboxService` từ `RabbitPublisher` sang `EventBus`. Đây là một refactor
thuần túy: hành vi runtime giữ nguyên, một call site duy nhất (`container.ts`) được nối vào
cùng một implementation `RabbitPublisher` cụ thể. Điều này trả lời trực tiếp yêu cầu của
READ.md — "framework nên expose các interface ổn định để các nhà cung cấp hạ tầng có thể
được hoán đổi với ít thay đổi code nhất" — bằng phiên bản rẻ nhất có thể của điều đó: làm
việc này khi vẫn chỉ có đúng một call site, trước khi Phase 9 nhân nó lên nhiều lần.

**Tác động:** Bán kính ảnh hưởng thấp: một file interface mới, `container.ts`, và signature
constructor của `outbox-service.ts`. Không thay đổi schema, không migration, không thay đổi
API, không thay đổi hành vi.
**Khả năng tương thích:** Hoàn toàn backward compatible.
**Tiến hóa trong tương lai:** Biến việc hoán đổi Kafka/NATS/Redis-Streams trong tương lai (Phase 9,
một khi một module thứ hai thực sự được triển khai độc lập) thành việc thêm một
implementation `EventBus` mới cộng với một thay đổi wiring trong `container.ts` — không
phải một cuộc viết lại `CrudService`/`WorkflowEngine`/`OutboxService`.

---

### Metadata

**Quan sát:** `MetadataRegistry` + `MetadataCompiler` — validate lúc boot, kiểm tra
dangling reference, hashing tất định (deterministic), phát hiện drift, sinh OpenAPI. Đây là
thành phần vững chắc nhất, được tái sử dụng nhiều nhất trong codebase.

**Vấn đề:** Không tìm thấy vấn đề gì.

**Khuyến nghị:** Không thay đổi. Đây chính xác là nền tảng mà Phase A sub-project 2
(runtime loader cho metadata đã được persist) cần để xây dựng dựa trên — tái sử dụng nguyên
trạng thay vì xây song song một compiler path thứ hai cho metadata DB-authored.

**Tác động / Khả năng tương thích / Tiến hóa trong tương lai:** N/A — không đề xuất thay đổi.

---

### Scheduler / GraphQL

**Quan sát:** Cả hai đều chưa tồn tại. `docs/architectures/04-strategy.md` đã nêu sẵn
trigger của GraphQL (≥2 module có dữ liệu được tổng hợp trong một màn hình frontend); không
có tài liệu hay code nào ở bất cứ đâu nhắc đến một workflow action điều khiển bởi
scheduler/timer, dù `WorkflowEngine` về mặt khái niệm có thể hỗ trợ một action kiểu `Timer`
sau này (theo chính khung "Event / Command / Action / Timer" của READ.md).

**Khuyến nghị:** Đúng đắn khi bị hoãn lại ở cả hai trường hợp — không có trigger nào cho
cả hai tồn tại ngày nay. Không hành động.

---

## Part 2: Runtime Abstraction — What to Actually Build

READ.md đặt câu hỏi về `EventBus`, `WorkflowRuntime`, `PermissionProvider`,
`StorageProvider`, `CacheProvider`. Được xem xét dựa trên các trigger thực tế, không phải
giả định:

| Interface | Recommend now? | Why |
|---|---|---|
| **EventBus** | **Yes** | Interface duy nhất có sự bất đối xứng chi phí "rẻ-bây-giờ/đắt-về-sau" trong ngắn hạn. Xem phần Event của Part 1. |
| StorageProvider | No | Không có nhu cầu datastore thứ hai; sẽ mâu thuẫn với quyết định `BaseRepository` đã được chốt ở Phase 1. Chỉ xem xét lại nếu quyết định Tiny-profile (Part 3) đi theo hướng khẳng định. |
| WorkflowRuntime | No | Không có yêu cầu distributed-workflow nào tồn tại ở bất cứ đâu trong docs hay roadmap. Khuyến nghị tường minh **phản đối** việc đánh giá các engine Temporal/Camunda/BPMN trong tương lai gần — chúng giải quyết một bài toán distributed-orchestration mà hệ thống single-process này không có, và sẽ đi ngược với định hướng đã nêu của Phase B (guard khai báo đơn giản, không phải một cuộc hoán đổi workflow runtime tổng quát). |
| PermissionProvider | No | `PolicyStore` đã là seam có kích thước đúng (lưu trữ, không phải service). Bọc thêm một lớp quanh chính `PermissionService` sẽ thêm một layer mà không có implementation thứ hai nào để biện minh cho nó. |
| CacheProvider | No | Không có vấn đề độ trễ nào được đo lường thực tế. Thiết kế per-call (không phải cross-request) của `PermissionSnapshot` là một lựa chọn chủ động, đã được ghi lại — đưa Redis vào đây là giải quyết một vấn đề chưa ai đo lường được. |

---

## Part 3: Deployment Profiles — An Open Decision, Not a Recommendation

`docs/architectures/07-deployment.md` chỉ ghi lại topology dev cục bộ (docker compose, hai
bare process, không có orchestrator, không có LB, không có secrets manager — khoảng trống đó
đã thuộc về Phase 8 Hardening, chưa bắt đầu). `docs/architectures/02-constraints.md` ràng
buộc Postgres là "datastore duy nhất" và RabbitMQ là "message broker duy nhất" như những
**ràng buộc kỹ thuật (technical constraints)**, không phải các giá trị mặc định.

Khung deployment-profile của READ.md (Tiny / Business / Enterprise / Cloud, "một khách hàng
nhỏ vẫn nên chạy được Single Binary + SQLite + Memory EventBus") kéo theo trực tiếp việc phải
sửa đổi ràng buộc binding đó. Review này chủ động không đưa ra quyết định đó — đây là một
quyết định thuộc phạm vi sản phẩm (Metap có nhắm tới các khách hàng low-code self-hosted/
on-prem không thể chạy Postgres+RabbitMQ hay không?), không phải một khoảng trống kỹ thuật
cần âm thầm vá lại.

**Option 1 — giữ một triết lý deployment duy nhất (mặc định được khuyến nghị):** Các profile
Business/Enterprise/Cloud khác nhau về scale và HA (số lượng replica, secrets backend,
autoscaling — tất cả đã được scope sẵn dưới Phase 8 Hardening), không bao giờ khác nhau bằng
cách hoán đổi Postgres/RabbitMQ. Không phát sinh công việc mới nào ngoài việc hoàn thành
Phase 8 như đã lên kế hoạch.

**Option 2 — thêm một profile Tiny thực sự (SQLite + in-memory bus, single binary):** Đòi
hỏi, theo thứ tự: (a) sửa đổi chính thức ngôn ngữ ràng buộc trong `02-constraints.md`, (b)
interface `EventBus` từ Part 2 (nên xây dù thế nào đi nữa — đã được khuyến nghị bất kể) cộng
với một implementation `EventBus` in-memory rẻ tiền, (c) một cuộc kiểm toán dialect của
`QueryPlanner` — đây là phần thực sự tốn kém, vì `jsonb_extract_path_text`,
`plainto_tsquery('simple', ...)`, và SQL keyset-pagination đều đặc thù Postgres, không chỉ
là driver, (d) abstraction `StorageProvider` mà Part 2 vừa lập luận phản đối việc xây dựng
khi chưa có trigger.

**Khuyến nghị:** Option 1 cho lúc này. Một profile Tiny là một hướng đi sản phẩm hợp lý
trong tương lai (hữu ích một khi low-code platform của Phase 11 có một kịch bản khách hàng
self-host cụ thể), nhưng theo đúng triết lý trigger-based của chính dự án này, nó không nên
được quyết định một cách suy đoán. Nếu muốn cam kết theo hướng này, hãy sắp xếp trình tự như
sau: interface EventBus (xây dù thế nào đi nữa) → implementation EventBus in-memory (rẻ) →
kiểm toán dialect của QueryPlanner (chi phí thực sự, làm việc này trước khi động vào phần
storage) → StorageProvider → implementation SQLite — mỗi bước đều có thể ship riêng và có
giá trị riêng, không có bước nào bị lãng phí nếu bước SQLite cuối cùng không bao giờ xảy ra.

---

## Part 4: Migration Strategy

**Trạng thái hiện tại:** Postgres + RabbitMQ cụ thể ở khắp mọi nơi trừ `PolicyStore`. Phase 11
(low-code control plane) có một sub-project đã được spec nhưng chưa triển khai.

**Intermediate state (recommended next, all low-risk, all independently shippable):**
1. Sửa mô tả đã lỗi thời về `WorkflowEngine` trong `CLAUDE.md` (phát hiện Workflow của Part
   1).
2. Trích xuất interface `EventBus` (phát hiện Event của Part 1 / Part 2).
3. Viết implementation plan cho Phase A sub-project 1 (đã được spec sẵn — cần
   `writing-plans`, không cần thiết kế mới).

**Target state (where Phase 9 and Phase 11 converge):** Một module thứ hai thực sự được
triển khai độc lập (Phase 7), thực sự kích hoạt các trigger multi-service của Phase 9; toàn
bộ low-code control plane của Phase 11 (các sub-project A đến C) hoàn thành; quyết định
deployment-profile (Part 3) đã được đưa ra tường minh và ghi lại, chứ không phải bị mặc định
theo một cách tình cờ.

---

## Part 5: Technology Recommendations

Được giữ theo đúng thước đo của chính READ.md: chỉ khuyến nghị khi có một vấn đề thực sự,
hiện hữu, không phải vì thứ gì đó đang phổ biến:

- **Tái sử dụng `PolicyCondition` cho workflow guard** (Part 1, Workflow) — không phải công
  nghệ mới, mà là tái sử dụng một thứ đã được xây dựng sẵn. Là mục có giá trị-trên-công-sức
  cao nhất trong toàn bộ review này, chỉ sau việc trích xuất `EventBus`.
- **Không có message broker mới, không có orchestrator, không có GraphQL federation, không
  có Temporal/Camunda, không có OpenFGA/Casbin.** RBAC/ABAC tự xây dựng trong nhà kết hợp
  với `PolicyExplainer` đã bao phủ mọi nhu cầu permission đã được ghi lại; mô hình
  relationship-graph của OpenFGA giải quyết bài toán authorization dựa trên quan hệ/phân cấp
  sâu, một vấn đề mà hệ thống này chưa gặp phải.
- **Một điều cần theo dõi, không cần hành động:** nếu profile Tiny của Part 3 sau này được
  chọn, Drizzle đã có sẵn một driver SQLite — nên tái sử dụng ORM hiện có thay vì đưa vào một
  ORM thứ hai. Không phải là một khuyến nghị để hành động ngay bây giờ.

---

## Closing: Prioritized Action List

Theo thứ tự, mỗi mục đều có giá trị độc lập và nhỏ gọn:

1. Sửa dòng đã lỗi thời về `WorkflowEngine` trong `CLAUDE.md` cho khớp với trạng thái thực
   tế (đã hoàn thành) của Phase 5 trong `docs/roadmap.md`. Chỉ sửa tài liệu.
2. Trích xuất một interface `EventBus` đặt trước `RabbitPublisher`, theo đúng tiền lệ của
   `PolicyStore`. Là một refactor thuần túy, hiện chỉ có một call site.
3. Viết implementation plan cho Phase A sub-project 1 (metadata storage & versioning) — spec
   đã tồn tại sẵn.
4. Quyết định hướng đi cho deployment-profile (Part 3) một cách tường minh, và ghi lại quyết
   định đó (dưới dạng một mục kiểu ADR được index từ `docs/architectures/09-adr.md`) một khi
   đã quyết định.
5. Mọi thứ khác được nêu ra trong review này (Repository, WorkflowRuntime,
   PermissionProvider, CacheProvider, GraphQL, Scheduler, bản thân profile Tiny) đều đúng
   đắn khi chưa-được-kích-hoạt — cứ để nguyên cho đến khi trigger đã nêu của nó xảy ra.
