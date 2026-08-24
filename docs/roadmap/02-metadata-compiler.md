## Phase 2: Metadata Compiler

**Trạng thái: Đã xong.**

- `MetadataCompiler.validate` — validate lúc startup cho từng entity: duplicate field names, dangling listView field/filter/defaultSort reference, enum field không có `enumValues`, workflow shape sai định dạng, duplicate transition. Chạy bên trong `MetadataRegistry.register()`, nên một entity module lỗi sẽ fail ngay lúc boot, không đợi đến request đầu tiên.
- `MetadataRegistry.validateReferences()` — kiểm tra cross-entity rằng mọi field kiểu `reference` có `refEntity` trỏ đến một entity đã đăng ký; chạy một lần sau khi mọi entity đã được đăng ký (tách ra khỏi `container.ts` — xem ghi chú về entity-registration bên dưới).
- `MetadataCompiler.hash` — SHA-256 xác định (deterministic) trên một serialization đã sắp xếp canonical của shape entity (loại trừ các hàm `guard` của workflow transition, vì chúng không thể biểu diễn được và đã bị strip khi truyền qua wire). Được expose dưới dạng `version` trên `EntitySummary` (`GET /metadata/entities`) và trên type `EntitySummary` phía frontend.
- Bảng `metadata_versions` (migration `0005_condemned_cerise.sql`) + `MetadataDriftService` — so sánh hash hiện tại của mỗi entity với hash đã ghi nhận lần trước lúc boot, và cảnh báo (không bao giờ crash) khi có drift, theo cùng tinh thần graceful-degradation của `HealthService`. Được wire vào container dưới tên `container.metadataDrift`, gọi từ `buildApp`.
- OpenAPI generator (`openapi-generator.ts`) — expose tại `GET /metadata/openapi.json`, chỉ build từ projection an toàn `EntitySummary`.

Cũng đã fix trong lần này: `createContainer` (`src/core/container.ts`) trước đây import trực tiếp `customerEntity` và đăng ký nó inline — tức một file `core` với tay vào `modules`, điều mà layering (`modules -> metadata definitions`, không theo chiều ngược lại) không cho phép. Entity registration giờ là mối quan tâm ở tầng application: `createContainer` trả về một `MetadataRegistry` rỗng, và `registerEntities()` (`src/modules/registry.ts`) — nơi duy nhất biết danh sách entity của deployment — đăng ký chúng rồi gọi `validateReferences()` sau đó. Các caller (`buildApp`, outbox worker, test) gọi `registerEntities(container.metadata)` ngay sau `createContainer(config)`.

Mục tiêu:

- Validate entity definition lúc startup.
- Compile field definition thành:
  - validation schema
  - list view contract
  - OpenAPI schema
  - frontend metadata
  - index recommendation
- Thêm metadata version/hash.
- Thêm schema compatibility check.

Deliverables:

- `MetadataCompiler`
- `MetadataValidationError`
- OpenAPI được generate
- endpoint frontend metadata được generate

