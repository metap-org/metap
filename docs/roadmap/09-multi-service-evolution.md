## Phase 9: Multi-Service Evolution

Khác với Phase 1-8, phase này là trigger-based, không tuần tự — nó bắt đầu khi điều kiện trigger của nó xảy ra, không phải khi Phase 8 xong. Xem mục "Future Evolution: Multi-Service Split" của `docs/architectures/04-strategy.md` để biết toàn bộ lý do.

**Bản thân cấu trúc repo/package đã xong, trước cả khi trigger xảy ra.** Việc restructure monorepo ngày 2026-08-02 đã kéo việc split pnpm-workspace lên sớm hơn, bằng một lựa chọn tường minh, không phải vì điều kiện trigger đã xảy ra: `packages/core` và `apps/crm` đã là các package riêng biệt (`apps/crm` là một Fastify app mỏng, import `packages/core` qua `workspace:*`), khớp với hình dạng mà trigger này mô tả. Điều *chưa* xảy ra là phần thực chất của trigger — vẫn chỉ có đúng một module thật (`crm`); chưa module thứ hai nào cần được xây như một deployable unit riêng. Hãy coi việc split cấu trúc này là hạ tầng sẵn có, không phải bằng chứng rằng trigger multi-service nền tảng đã xảy ra.

Các trigger và transition mà mỗi cái mở khóa:

- **Một module thứ hai (CRM, sales, inventory, accounting, ...) thực sự cần được xây như một deployable unit riêng** → đã xong về mặt cấu trúc (xem ở trên); phần việc còn lại là xây chính module thứ hai đó — xem Phase 7.
- **Một màn hình frontend đơn cần aggregate dữ liệu từ ≥2 service** → xây một GraphQL gateway làm BFF phía trước các REST service. Chưa trigger — vẫn chỉ có một module, nên chưa có nhu cầu aggregate cross-service nào tồn tại. Readiness-note (2026-08-12, không phải implementation): đã kiểm tra và ghi lại ở `docs/architectures/04-strategy.md`'s "Sự sẵn sàng của backend cho GraphQL BFF tương lai" — `CrudService` đã đủ protocol-agnostic để một GraphQL resolver in-process gọi thẳng vào, không cần module `dispatch` trung gian; phần "local-vs-remote dispatch" (BFF gọi in-process hay remote tuỳ entity) cố tình chưa thiết kế, chờ đến khi trigger split-deploy bên dưới xảy ra thật.
- **Việc split repo/package ở trên đã thực sự xảy ra** → đánh giá gRPC cho các lệnh gọi service-to-service ở nơi overhead của REST đáng kể. Việc split đã xong về mặt cấu trúc, nhưng với chỉ một process đang chạy thì chưa có lệnh gọi service-to-service thật nào để tối ưu — đánh giá việc này khi một module thứ hai thực sự được deploy độc lập (Phase 7), không phải chỉ dựa trên việc split cấu trúc.

Cho đến khi một trigger xảy ra, transition của nó không được build. Việc duy nhất cần làm ngay bây giờ, trước mọi trigger: giữ tên mọi entity module mới theo domain-namespace (`<module>.<entity>`, ví dụ `crm.customers`) và không bao giờ để `QueryPlanner`/`CrudService` join dữ liệu giữa các entity khác nhau trong SQL — cả hai điều này đã đúng ngay hôm nay và không tốn gì để giữ đúng.

**Rà soát lại 2026-08-17:** kiểm tra lại cả 3 trigger ở trên — vẫn chỉ có một module thật
(`crm.customers` + 3 module demo của Phase 7, cùng chạy chung một process `crm-server`), chưa
màn hình FE nào cần aggregate ≥2 service, và việc split repo/package vẫn chỉ là hạ tầng sẵn có
chứ chưa có lệnh gọi service-to-service thật nào tồn tại. Không có gì để "làm nốt" ở phase này
— cố tình build trước trigger sẽ đi ngược triết lý trigger-based của chính phase này.

**Trigger GraphQL BFF/gRPC thực sự xảy ra 2026-08-29** — chủ dự án sắp xây "WAAP", sản phẩm thật
với nhiều microservice, không phải demo. Theo đúng tinh thần phase này (build khi trigger là thật,
không sớm hơn), đã xây nền tảng — `docs/roadmap.md` Phase 49 (`docs/roadmap/
49-graphql-grpc-jwks-foundation.md`): `metap-graphql`/`metap-graphql-http` (schema động, DataLoader,
complexity limit, field-mask) cho trigger GraphQL BFF; `metap-grpc` (`RecordService` generic
CRUD-over-gRPC) cho trigger service-to-service.

**Cả 2 transition mô tả ở trên giờ đã thực sự xảy ra, không chỉ còn là hạ tầng sẵn sàng-để-dùng —
Phase 50 cùng ngày (`docs/roadmap/50-graphql-gateway-real-bff.md`)**: `crates/graphql-gateway` là
1 binary GraphQL gateway BFF thật, đứng trước `apps/jira-server` + `apps/crm-server` (2 process
tách biệt thật, mỗi cái tự gRPC riêng), aggregate 1 schema xuyên cả 2 — verify sống bằng 1 test
e2e dựng 2 service độc lập thật, 1 query GraphQL duy nhất lấy dữ liệu từ cả 2. Vẫn còn đúng: chưa
có portal/entity WAAP thật (đây vẫn là gateway demo trên `jira-server`/`crm-server`, không phải
binary/repo WAAP) — nhưng cơ chế BFF gọi remote qua gRPC theo entity, đúng thứ trigger này mô tả,
không còn là suy đoán kiến trúc nữa.


## Tiêu chí thành công

Metap được coi là thành công nếu một developer có thể:

1. Định nghĩa một ERP entity với field và workflow.
2. Có được metadata CRUD/list/form mà không cần viết boilerplate.
3. Thêm policy mà không cần đụng vào HTTP route.
4. Có được event đáng tin cậy mà không cần publish RabbitMQ thủ công.
5. Tune một list view chậm thông qua metadata query/index.
6. Giữ việc enforce security ở phía server.

