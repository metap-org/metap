# Modular-First Capability SPI — Target Architecture

Date: 2026-08-07

Status: exploratory (mục tiêu định hướng, không phải cam kết xây dựng — xem
phần "Relationship to Current Architecture" bên dưới để biết điều gì đã và chưa được quyết định)

## Mục đích

Tài liệu này không phải là roadmap sản phẩm chính. `docs/roadmap.md` theo dõi tiến độ
xây dựng hiện tại của core platform và trạng thái phase chính thức; một architecture review
(2026-08-07, nay chỉ còn lưu tóm tắt ở [09. Architecture Decisions](architectures/09-adr.md))
đã rà từng thành phần một về những gì đang tồn tại tại thời điểm đó. Tài liệu này ghi lại một
câu hỏi riêng biệt, đặt ra trực tiếp dựa trên phần Runtime Abstraction và
Deployment Profiles của review đó:

> Nếu Metap muốn phục vụ mọi thứ từ một khách hàng SME tự host cho đến một triển khai
> enterprise phân tán, xuất phát từ cùng *một* nguồn mã, thì boundary hạ tầng cần có hình
> dạng mục tiêu như thế nào, và nên xây trước các trigger hiện tại bao xa?

Nên đọc cùng với `docs/vision.md` và `docs/low-code-platform-v1.md`, hai tài liệu mô tả
đích đến low-code theo cùng cách: mang tính định hướng, chưa phải hiện trạng đã xây.

## Canh cược cốt lõi

Phần lớn các nền tảng low-code/ERP thất bại với khách hàng SME không phải vì sản phẩm sai,
mà vì để chạy được nó cần triển khai Kafka, Redis, RabbitMQ, một workflow engine, IAM,
GraphQL gateway, Elasticsearch, Temporal, Prometheus — cả chục dịch vụ chỉ để chạy một
CRM đơn giản. Một khách hàng nhỏ sẽ không làm vậy.

Hướng đề xuất là **modular-first, không phải microservice-first**: cùng một nguồn mã chạy
như một binary duy nhất cho khách hàng nhỏ, và như một hệ thống phân tán cho khách hàng lớn,
chỉ chuyển đổi *cấu hình*, không bao giờ đổi code nghiệp vụ hay workflow. Đây không phải
triết lý mới đối với Metap — đó vẫn là lập trường trigger-based, evolution-over-rewrite đã
có sẵn trong `docs/architectures/04-strategy.md` và Phase 9 của `docs/roadmap.md` — nhưng
tài liệu này đặt tên cho một hình dạng mục tiêu cụ thể hơn cho boundary hạ tầng mà lập
trường đó cần phát triển tới.

## Mô hình ba tầng

```
Level 1 — Programming Model
  Entity, Workflow, Permission, Event, Repository, API
  (những gì tác giả entity/module nhìn thấy; không bao giờ tham chiếu tới Level 3)

Level 2 — Capability SPI
  Storage, EventBus, Scheduler, Identity, Cache, Search, WorkflowRuntime
  (các interface — `EventBus` giờ đã là một trait Rust thật, `crates/metap-infra/src/event_bus.rs`,
  không còn là giả thuyết — xem "The Rust question" bên dưới. Bản thân pattern này
  là ports-and-adapters không phụ thuộc ngôn ngữ, không bị trói vào một ngôn ngữ cụ thể theo thiết kế)

Level 3 — Providers
  Memory, SQLite, Postgres, MySQL, RabbitMQ, Kafka, NATS, Redis, OpenFGA, Casbin,
  Elasticsearch, S3, ...
```

Một lời gọi `Order.emit("Paid")` ở Level 1 không bao giờ biết `EventBus` của Level 2
hiện đang được backing bởi một in-memory bus, RabbitMQ, hay Kafka. Repository của một
`Customer` entity không bao giờ biết nó đang đọc từ SQLite hay Postgres. Đây là phiên bản
tổng quát hóa của một pattern mà Metap đã có sẵn hai ví dụ hoạt động thực tế: `PolicyStore`
(ban đầu là `packages/core/src/core/permission/policy-store.ts` trong codebase TS, giờ là
trait `PolicyStore` trong `crates/metap-permission`) — `PermissionService` phụ thuộc vào
interface, `PostgresPolicyStore` là implementation duy nhất của nó hiện nay — và, kể từ khi
port sang Rust, `EventBus` (`crates/metap-infra`, `RabbitEventBus` là implementation duy
nhất của nó), được xây dựng như một trait ngay từ đầu chứ không phải bổ sung sau. Không seam
nào trong hai cái này tốn thêm chi phí gì ngoài việc định nghĩa một interface/trait duy
nhất. Đề xuất ở đây là mở rộng cùng hình dạng đó sang các infrastructure dependency khác
chưa có seam này.

### Câu hỏi về Rust — đã quyết định, không còn là giả thuyết

Capability SPI pattern của tài liệu này không phụ thuộc ngôn ngữ theo thiết kế; việc nó
được triển khai bằng TypeScript hay Rust ban đầu được đặt ra như một câu hỏi tách biệt khỏi
bản thân pattern. Câu hỏi đó đã được quyết định (xem [09. Architecture Decisions](architectures/09-adr.md)):
`packages/core` đang chuyển sang Rust (Option B, tất cả các profile), và Migration Order
được ghi lại ở đó giờ đã **hoàn thành** — trait `EventBus` của `crates/metap-infra`
(`trait EventBus { async fn publish(...); async fn close(...); }`) là thật, đã được xây
dựng, đã được test (unit + e2e), và chính là thứ mà `crates/metap-workflow`/`crates/metap-crud`
đang thực sự phụ thuộc vào ngày nay, khớp chính xác với bản phác thảo `trait EventBus { ... }`
của đề xuất gốc. Điều này vẫn không thay đổi số lượng SPI trong *sáu SPI còn lại* đáng để
xây dựng (vẫn là không cái nào — riêng `Storage` cố tình không được tách thành một trait
chính thức; mọi crate Rust chạm vào DB đều dùng trực tiếp `sqlx::PgPool`, theo đúng lý lẽ
gốc của review đó (xem [09. Architecture Decisions](architectures/09-adr.md)), mà quá trình port sang Rust
đã tuân theo chứ không đảo ngược) hay quyết định về deployment-profile ở phần Deployment
Profiles của review đó, vốn vẫn còn để ngỏ theo đúng nghĩa của nó.

## Deployment Profiles

| Profile | Storage | EventBus | Scheduler | Notes |
|---|---|---|---|---|
| **Tiny** | SQLite | Memory | Memory | `./metap` — một binary duy nhất, không có dịch vụ ngoài. Mục tiêu self-host/SME. |
| **Business** | Postgres | Memory | Background worker (in-process) | Xấp xỉ hình dạng triển khai thực tế hiện nay — xem lưu ý bên dưới. |
| **Enterprise** | Postgres | RabbitMQ | Distributed worker | Thêm Redis (cache), S3 (file storage), Elasticsearch (search) khi cần. |
| **Cloud** | Postgres (managed) | Kafka/RabbitMQ | Scheduler cluster | Tách biệt hoàn toàn: gateway, workflow cluster, notification, search, storage đều là các dịch vụ được scale độc lập. |

Cùng một nguồn mã. Chỉ có phần đấu nối Level 2→3 thay đổi, dẫn dắt bởi cấu hình:

```yaml
eventBus:
  provider: memory   # or: rabbit, kafka
storage:
  provider: sqlite    # or: postgres, mysql
```

**Lưu ý, nói thẳng:** profile "Business" thực tế của Metap hiện nay là Postgres + RabbitMQ
(không phải một in-process memory bus) — `docs/architectures/02-constraints.md` hiện đang
ràng buộc cả hai như *datastore/broker duy nhất*, chứ không phải như một profile trong số
nhiều profile. Bảng trên mô tả mục tiêu, không phải hiện trạng; để dung hòa hai điều này
cần có quyết định nêu trong phần "Relationship to Current Architecture" bên dưới.

## Module Deployment: `deployment: remote`

Cơ chế biến "modular monolith → distributed monolith → selective microservices" (thay vì
viết lại từ đầu) thành thứ cụ thể: một module boundary là một công tắc cấu hình, không phải
một nhánh code.

```yaml
module:
  order:
    deployment: remote   # was: local (the default — same process as everything else)
```

Khi một module là `local`, các lời gọi `EventBus`/`Repository`/workflow của nó được giải
quyết trong cùng process. Khi là `remote`, cùng những lời gọi đó được giải quyết tới một
network client đứng sau cùng các interface của Level 2 — business logic, định nghĩa
workflow, và permission rule của module hoàn toàn không đổi trong cả hai trường hợp. Đây
chính là phần "how" cụ thể đứng sau mục Multi-Service Split của
`docs/architectures/04-strategy.md` và trigger Phase 9 của `docs/roadmap.md` ("một module
thứ hai thực sự cần được xây thành một deployable unit riêng") — mục đó hiện chỉ nói *rằng*
điều này sẽ xảy ra; tài liệu này bổ sung *cách* nó sẽ xảy ra mà không cần viết lại.

Failure mode mà cách làm này tránh được, nêu tên rõ ràng vì đây là bug pattern có thật ở
rất nhiều engine: một bước workflow được viết trực tiếp dưới dạng `publishToRabbit(...)`.
Ngay khi bạn muốn bước đó chạy in-process thay vì vậy (hoặc ngược lại), bạn phải viết lại
nó. Một bước workflow được viết dưới dạng `emit(event)` dựa trên interface `EventBus` thì
không quan tâm chuyện đó.

## Per-Module Migration

Mỗi module sở hữu `metadata.yaml` riêng của nó (hoặc code-authored entity, theo mô hình
hiện tại) cộng với `migration/` + `workflow/` + `permission/`:

```
crm/
  migration/001.sql
  migration/002.sql
inventory/
  migration/001.sql
```

Platform tính toán kế hoạch migration đã gộp và sắp thứ tự trên tất cả các module tại thời
điểm deploy (`crm` cần 003-005, `inventory` cần 003 → chạy theo thứ tự phụ thuộc) thay vì
để một developer thủ công theo dõi thứ tự migration xuyên module.

Điều này ánh xạ vào Data Model Strategy Step 3 đã được ghi lại trong
`docs/architectures/05-building-blocks.md` ("dedicated tables cho các đường xử lý trọng
yếu về accounting/inventory") — hiện tại, một bảng `records` JSONB dùng chung phục vụ mọi
entity/module, nên các thư mục migration theo từng module chưa có schema riêng để migrate.
Cơ chế này chỉ trở nên cụ thể một khi Step 3 thực sự được kích hoạt, chứ không phải trước đó.

## Relationship to Current Architecture

Đây là phần quan trọng nhất — tài liệu này thay đổi gì và không thay đổi gì.

**Tài liệu này là:** một hình dạng mục tiêu đã được đặt tên, để các quyết định ngắn hạn
(bắt đầu từ việc tách `EventBus` — đã xong, xem "Câu hỏi về Rust" ở trên) nhắm tới một đích
đến mạch lạc thay vì bị quyết định từng cái một mà không có bức tranh chung.

**Tài liệu này không phải là:**
- **Không phải một cam kết xây dựng sáu Capability SPI còn lại** (Storage, Scheduler,
  Identity, Cache, Search, WorkflowRuntime). Review kiến trúc 2026-08-07 (tóm tắt ở
  [09. Architecture Decisions](architectures/09-adr.md))
  đã đánh giá từng cái dựa trên một trigger thực tế và không tìm thấy trigger nào ngoài
  `EventBus`. Kết luận đó không đổi bởi tài liệu này. Xây cả bảy SPI ngay bây giờ sẽ đúng
  là kiểu build-ahead-of-trigger mà dự án này đã nhiều lần và dứt khoát từ chối làm (Phase 1
  từ chối `BaseRepository` vì lý do này; Phase 4 từ chối một report query boundary vì lý do
  này).
- **Không phải một thay đổi đối với `docs/architectures/02-constraints.md`.** File đó hiện
  đang ràng buộc Postgres và RabbitMQ là datastore/broker *duy nhất* — dòng SQLite/Memory
  của profile Tiny trong bảng trên trực tiếp mâu thuẫn với ngôn ngữ ràng buộc đó. Chấp nhận
  Tiny như một mục tiêu thật đòi hỏi một quyết định riêng, tường minh để sửa đổi constraint
  đó (đây chính xác là "Option 2" của phần Deployment Profiles trong review kiến trúc 2026-08-07) — tài liệu này chỉ
  đặt tên cho hình dạng mục tiêu mà quyết định đó sẽ tạo ra, chứ không tự đưa ra quyết định
  đó.
- **Không phải bằng chứng để bắt đầu Phase 9 sớm.** Các trigger của Phase 9 (một module
  thứ hai cần triển khai độc lập, tổng hợp xuyên module, các lời gọi service-to-service
  thực sự) không đổi. `deployment: remote` là tài liệu hóa *cách* Phase 9 sẽ hoạt động khi
  được kích hoạt, không phải một lý do để kích hoạt nó ngay bây giờ.

**Sự căng thẳng thành thật:** hình dạng mục tiêu này đòi hỏi trừu tượng hóa hạ tầng *trước*
phần lớn các trigger của nó, đặt cược rằng một phân khúc khách hàng self-host/SME cuối cùng
sẽ cần đến nó. Track record của dự án cho tới nay (mọi phase trong `docs/roadmap.md`) đã
nhất quán từ chối canh cược đó để thiên về xây đúng những gì được kích hoạt. Việc chấp nhận
tài liệu này như một mục tiêu thật không giải quyết sự căng thẳng đó — nó chỉ có nghĩa là
sự căng thẳng giờ được đặt tên và hiển thị rõ ràng thay vì ngầm định, và mỗi SPI trong
tương lai vẫn sẽ được đánh giá dựa trên một trigger thực tế trước khi được xây, theo trình
tự bên dưới.

## Sequencing

Không phải một roadmap phase mới — ánh xạ vào Phase 9 (Multi-Service Evolution) và quyết
định deployment-profile còn để ngỏ được nêu trong phần Deployment Profiles của review kiến
trúc 2026-08-07 (xem [09. Architecture Decisions](architectures/09-adr.md)). Mỗi bước dưới
đây có giá trị độc lập; không bước nào ràng buộc phải làm bước tiếp theo.

1. **`EventBus` SPI — đã xong.** Được xây dựng thành trait `EventBus` của
   `crates/metap-infra` + implementation `RabbitEventBus`, như một phần của toàn bộ Rust
   Migration Order, không phải như một lần tách riêng lẻ trên nền TS.
2. **Ghi lại `deployment: remote`** trong mục Future Evolution của
   `docs/architectures/04-strategy.md` — không có code, chỉ đặt tên cho cơ chế mà Phase 9
   sẽ dùng.
3. **Quyết định câu hỏi về profile Tiny / SQLite một cách tường minh** (architecture review
   Part 3) — chỉ khi đã quyết định thì một `Storage` SPI + SQLite provider mới đáng để xây.
4. **Các SPI còn lại** (Scheduler, Identity, Cache, Search, WorkflowRuntime) — mỗi cái được
   đánh giá độc lập dựa trên trigger thực tế của riêng nó khi nó xuất hiện, không bao giờ
   được xây như một gói.
5. **Gộp migration theo từng module** — chờ trigger của Step 3 trong Data Model Strategy
   (một module thực sự cần các dedicated table), giống như hiện tại.
