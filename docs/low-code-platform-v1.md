# Định hướng Low-code Platform V1

Ngày: 2026-08-02

Trạng thái: exploratory (đang khám phá)

## Mục đích

Tài liệu này không phải là roadmap sản phẩm chính.

`docs/roadmap.md` theo dõi tiến độ xây dựng và trạng thái phase chính thức của core platform. Tài liệu này ghi lại một câu hỏi riêng biệt:

> Nếu Metap tiếp tục phát triển vượt ra ngoài một metadata-driven app core, thì một lộ trình thực tế hướng tới một low-code platform thực sự sẽ trông như thế nào?

Nên đọc cùng với `docs/vision.md`, tài liệu phát biểu định hướng rộng hơn một cách rõ ràng hơn:

> low-code là đích đến cao hơn, không chỉ là một nhánh phụ tùy chọn.

Mục tiêu ở đây là mô tả:

- những gì đã tồn tại hỗ trợ cho định hướng đó
- những gì còn thiếu
- một lộ trình 3 phase thực tế để đạt "low-code platform v1"

## Điểm xuất phát

Metap đã có sẵn một số nền tảng quan trọng thân thiện với low-code:

- các Entity được authored qua metadata
- CRUD tổng quát trên một runtime dùng chung
- query planning bị ràng buộc bởi metadata
- Workflow điều khiển bởi metadata
- policy-driven permission enforcement
- các primitive render frontend có thể tái sử dụng trong `packages/platform-react`

Điều đó có nghĩa là hệ thống hiện tại đã vượt xa một ứng dụng CRM đơn lẻ. Nó là một metadata-driven platform core.

Những gì nó **chưa** có:

- một self-serve builder
- một runtime chấp nhận metadata do người dùng tự tạo một cách an toàn
- một metadata control plane có versioning
- một platform mà người không phải lập trình viên có thể định nghĩa và publish app mà không cần viết TypeScript

Hiện tại, metadata vẫn **được authored bằng code**. Đó là ranh giới chính giữa hệ thống hiện tại và đích đến low-code cao hơn.

## Mục tiêu sản phẩm

Mục tiêu thực tế cho V1 nên là:

> Cho phép một operator nội bộ hoặc admin nâng cao định nghĩa và publish một business app cơ bản từ metadata, mà không cần chỉnh sửa application code, đồng thời vẫn giữ được các thuộc tính an toàn phía server mà Metap đã quan tâm từ đầu.

Điều này cố ý hẹp hơn so với "full Airtable + Retool + Salesforce builder".

Vì vậy tài liệu này mô tả một phiên bản platform đầu tiên có thể đạt được trên đường tới tầm nhìn rộng hơn, chứ không phải giới hạn cuối cùng của sản phẩm.

V1 nên hỗ trợ:

- entity modeling (mô hình hóa Entity)
- cấu hình field
- sinh list và form
- cấu hình Workflow
- thiết lập permission policy
- publish/lịch sử version

V1 **chưa nên** cố giải quyết những điều sau:

- visual workflow automation builder
- scripting tùy ý bởi end user
- hệ sinh thái marketplace/plugin
- bộ phân tích cross-app analytics
- drag-and-drop page builder cho mọi UI pattern

## Ràng buộc kiến trúc

Metap không nên vứt bỏ kiến trúc hiện tại để chạy theo low-code.

Hướng đi đúng là:

- giữ `packages/core` làm execution engine
- bổ sung một metadata control plane bao quanh nó
- chuyển từ metadata authored bằng code sang metadata được persist một cách an toàn
- compile metadata đã persist thành cùng loại runtime contract mà code path hiện tại đang dùng

Nói cách khác: tiến hóa authoring model, không phải toàn bộ runtime model.

## Những gì phải tồn tại trước khi V1 trở thành hiện thực

### 1. Metadata persistence và versioning

Trạng thái hiện tại:

- metadata nằm trong `*.entity.ts`
- việc đăng ký (registration) diễn ra lúc boot

Cần có:

- metadata được lưu trong database hoặc một configuration store riêng
- các phiên bản draft và published
- lịch sử revision
- hỗ trợ rollback
- validation trước khi publish

Nếu không có điều này, sẽ không có low-code platform thực sự, chỉ có một framework code-first kèm metadata.

### 2. Một metadata control plane

Trạng thái hiện tại:

- lập trình viên author metadata trực tiếp bằng TypeScript

Cần có:

- API để quản lý các định nghĩa metadata
- UI để tạo và chỉnh sửa Entity, field, list view, Workflow, và policy
- luồng review/publish

Đây là lớp "builder" tối thiểu.

### 3. Runtime compilation từ metadata đã persist

Trạng thái hiện tại:

- metadata đã được compile và validate lúc boot bởi `MetadataCompiler`

Cần có:

- một bước compile từ metadata đã lưu thành các định nghĩa nội bộ an toàn cho runtime
- các lỗi validation rõ ràng tại thời điểm publish
- bảo vệ chống lại metadata sai định dạng hoặc nguy hiểm
- deterministic version hash cho các snapshot metadata đã publish

Đây là nơi kiến trúc compiler hiện có giúp ích rất nhiều.

### 4. Ranh giới mở rộng an toàn (safe extension boundaries)

Trạng thái hiện tại:

- workflow guard là các hàm TypeScript thuần trong code

Cần có:

- một rule model an toàn cho V1, không yêu cầu thực thi user code tùy ý
- các điều kiện khai báo (declarative conditions) cho workflow guard và policy
- tùy chọn: một catalog các action built-in được kiểm soát chặt chẽ

Low-code V1 nên tránh scripting runtime tùy ý nếu platform muốn giữ được tính an toàn và khả năng vận hành có thể dự đoán được.

### 5. Mô hình quản trị hướng tenant

Trạng thái hiện tại:

- đã có admin API cho việc gán role và quản lý policy

Cần có:

- ai được phép thiết kế schema
- ai được phép publish thay đổi app
- các Tenant được cô lập với metadata và runtime của nhau như thế nào
- các thay đổi đã publish được audit như thế nào

Đây không chỉ là công việc UI. Nó là một phần của mô hình tin cậy (trust model) của platform.

## Lộ trình 3-Phase đề xuất

## Phase A: Nền tảng Metadata Control Plane

**Được chia thành 4 sub-project theo thứ tự** (xem trạng thái ở Phase 11 của `docs/roadmap.md`): (1) persisted metadata storage + draft/published versioning, (2) runtime loader hiện thực hóa (materialize) metadata đã publish thông qua pipeline `MetadataCompiler`/`MetadataRegistry` sẵn có, (3) một publish validation pipeline bổ sung các kiểm tra sâu hơn (cross-entity) lên trên phần shape validation của (1), (4) một metadata admin API. Sub-project 1 đã có spec viết sẵn: `docs/low-code-metadata-storage-design.md`. Các quyết định phạm vi (scoping) then chốt đã được chốt ở đó: metadata authored qua DB là global (không theo từng Tenant) cho Phase A, chưa hỗ trợ Workflow (cần công việc declarative-rule của Phase B trước), và `crm.customers` không được migrate ra khỏi `*.entity.ts` như một phần của Phase A — DB storage được chứng minh trên các Entity mới trước.

Mục tiêu:

Chuyển metadata từ source code sang một control plane có versioning, được persist, mà không thay đổi runtime execution model nhiều hơn mức cần thiết.

Deliverables:

- schema lưu trữ metadata
- các phiên bản metadata draft/published
- metadata admin API
- publish validation pipeline
- rollback về snapshot published trước đó
- runtime boot/load path có thể đọc metadata đã publish thay vì chỉ đọc static code

Nguyên tắc then chốt:

Giữ nguyên `MetadataCompiler`, `CrudService`, `QueryPlanner`, `WorkflowEngine`, và `PermissionService` hiện có làm execution core. Thay thế nguồn metadata, không thay thế engine.

Hình dạng thiết kế dự kiến:

- các bảng metadata mới
- `published_metadata_versions`
- `draft_metadata_changes`
- loader service hiện thực hóa các cấu trúc kiểu `EntityDefinition` cho runtime

Tiêu chí hoàn thành (exit criteria):

- một lập trình viên có thể định nghĩa một Entity mà không cần chỉnh sửa `*.entity.ts`
- server có thể validate và publish metadata đó một cách an toàn
- trải nghiệm CRUD/list/form được sinh tự động vẫn hoạt động từ metadata đã publish

## Phase B: Builder UI và Safe Runtime Rules

Mục tiêu:

Cung cấp cho operator một bề mặt authoring dùng được, và loại bỏ các điểm cấu hình chỉ-bằng-code còn lại đang cản trở việc áp dụng low-code.

Deliverables:

- entity builder UI
- field builder UI
- list view builder UI
- workflow editor UI
- policy editor UI
- declarative workflow guard model
- publish preview / validation report

Nguyên tắc then chốt:

Chưa đưa vào scripting tùy ý.

Thay vào đó:

- hỗ trợ condition builder
- hỗ trợ một catalog các field type và rule operator built-in
- hỗ trợ một tập nhỏ các action và transition built-in

Điều này giữ cho V1 an toàn, vận hành được, và kiểm thử được.

Tiêu chí hoàn thành:

- một admin nâng cao có thể tạo và publish một app kiểu CRM đơn giản từ UI
- không cần thay đổi source code cho luồng chuẩn (standard path)
- hành vi permission và Workflow vẫn được thực thi (enforced) phía server

## Phase C: Củng cố Platform cho việc sử dụng Low-code thực tế

Mục tiêu:

Làm cho hệ thống low-code đủ khả năng quản trị (governable) để chạy các app tenant-facing thực sự.

Deliverables:

- audit log cho metadata
- publish approval workflow nếu cần
- quy tắc cô lập schema ở cấp Tenant
- kiểm tra tác động migration cho các thay đổi metadata mang tính phá hủy (destructive)
- công cụ rollback và recovery mạnh mẽ hơn
- khả năng quan sát vận hành (operational visibility) cho các sự kiện publish metadata
- import/export định nghĩa app

Các ứng viên V1.5 tùy chọn, chỉ khi nhu cầu là thực sự:

- computed fields
- integration actions
- event-driven automations
- templated app starters

Tiêu chí hoàn thành:

- các thay đổi metadata có thể audit được
- các thao tác publish có thể đảo ngược (reversible)
- operator có thể hiểu và khôi phục sau các bản release metadata lỗi
- các Tenant có thể chạy an toàn các định nghĩa app khác nhau trên cùng một platform core

## Những điều không nên làm quá sớm

Để giữ cho nỗ lực này thực tế, cần tránh các cạm bẫy sau:

### 1. Không thêm scripting tùy ý cho end user trước tiên

Điều đó sẽ tạo ra:

- rủi ro bảo mật
- độ phức tạp khi debug
- vấn đề vận hành
- các đảm bảo thực thi (execution guarantee) không rõ ràng

Hãy bắt đầu với declarative rule và một action catalog có giới hạn.

### 2. Không xây dựng một generic drag-and-drop page builder trước tiên

Lợi thế thực sự của Metap hiện nay là:

- các business Entity điều khiển bởi metadata
- việc thực thi (enforcement) phía server
- UI nghiệp vụ được sinh tự động

Một hướng đi page-builder-first sẽ kéo sự chú ý ra khỏi phần core mạnh nhất của platform.

### 3. Không bỏ qua (bypass) execution engine phía server hiện có

Runtime hiện tại đã có sẵn những đảm bảo giá trị:

- Tenant scope
- permission enforcement
- optimistic locking
- các business event dựa trên Outbox pattern

Hướng đi low-code nên tái sử dụng những đảm bảo đó, không nên tái tạo lại chúng trong một stack song song.

## Khuyến nghị cụ thể trong ngắn hạn

Nếu dự án muốn giữ lại một cách "sạch" lựa chọn trở thành một low-code platform, các bước kiến trúc tiếp theo nên là:

1. ~~Siết chặt các permission default để control plane thừa hưởng một runtime an toàn hơn.~~ **Đã hoàn thành (2026-08-02)** — `PermissionService.scopedTenant` giờ đây báo lỗi rõ ràng (fail loudly) thay vì âm thầm mặc định một Tenant bị thiếu; xem `docs/architectures/08-cross-cutting.md#multi-tenancy`. Mô hình RBAC/ABAC "allow when no policy exists" (cho phép khi không có policy nào) rộng hơn là một lựa chọn thiết kế cố ý của Phase 3 (opt-in restriction, không phải default-deny), không phải là thứ mà fix này đụng tới — chỉ xem xét lại nếu control plane thực sự cần default-deny.
2. ~~Giới thiệu một shared public contract package cho các metadata DTO.~~ **Đã hoàn thành (2026-08-02), theo hình dạng khác so với đề xuất ở đây** — thay vì một package `packages/contracts` được bảo trì thủ công, backend tài liệu hóa wire contract của entity-metadata như một phần của tài liệu OpenAPI (`GET /metadata/openapi.json`), và các type của `packages/platform-react` được sinh từ đó thông qua `openapi-typescript` (`pnpm --filter @metap/platform-react generate:types`).
3. Thiết kế persisted metadata storage và publish semantics. **Đã viết spec, chưa triển khai** — `docs/low-code-metadata-storage-design.md` (Phase 11, sub-project 1). Spec đó có trước quyết định ngày 2026-08-07 về việc chuyển `packages/core` sang Rust (xem [09. Architecture Decisions](architectures/09-adr.md)); data model/service contract của nó vẫn còn áp dụng, nhưng phần triển khai nên nhắm tới Rust, không phải các đường dẫn file TS mà nó đề cập — xem ghi chú trạng thái của chính spec đó.
4. ~~Refactor workflow guard hướng tới hỗ trợ declarative rule, ngay cả khi các guard TypeScript vẫn còn tồn tại tạm thời.~~ **Đã hoàn thành, chỉ trong bản Rust port (2026-08-07)** — `WorkflowTransition.guard` của `crates/metap-metadata` là một `metap_permission::PolicyCondition`, không phải một hàm, ngay từ đầu (xem doc comment của `entity.rs` trong crate đó). Các guard của hệ thống TS đang được triển khai (`WorkflowTransition.guard: (data, context) => true | string` trong `entity.ts`) không thay đổi và vẫn dựa trên hàm — mục này chỉ được coi là giải quyết xong một khi/nếu Rust core thực sự thay thế hệ thống TS đang chạy, hiện vẫn chưa.
5. Tách riêng runtime app startup khỏi các mối quan tâm về bảo trì như index reconcile, ở những nơi hữu ích cho việc vận hành control-plane trong tương lai. Đã được theo dõi nhưng chưa thực hiện — xem `docs/architectures/11-risks.md`. Điều này vẫn đúng ở bản Rust port: boot sequence của `apps/crm-server` chạy `metadata_drift::check`/`index_reconciler::reconcile` inline trước khi serve, cùng hình dạng với `buildApp` của `app.ts`, chưa được tách riêng.

## Kết luận

Metap đã có sẵn nền tảng đúng đắn cho một low-code platform:

- metadata như nguồn gốc của hành vi (behavior)
- các runtime service tổng quát
- UI được sinh tự động có thể tái sử dụng
- các ranh giới service rõ ràng (explicit service boundaries)

Sự chuyển đổi thực sự không phải là "xây thêm nhiều CRUD hơn."

Mà là:

> chuyển từ metadata được authored bởi lập trình viên trong code sang metadata được authored bởi operator với khả năng persist, validate, publish, và governance an toàn.

Nếu quá trình chuyển đổi đó được thực hiện tốt, Metap có thể tiến hóa thành một low-code platform đáng tin cậy mà không cần từ bỏ kiến trúc hiện tại của nó.

Vì vậy, low-code nên được hiểu là đích đến cao hơn, nằm phía trên metadata-driven core hiện nay, và tài liệu này là một lộ trình thực tế hướng tới đích đến đó.
