Status: được viết ngày 2026-08-02. **Đã triển khai (2026-08-11), retarget sang Rust** — xem
`docs/roadmap.md` Phase 11 cho tình trạng thật/live-verify, và
`crates/metap-lowcode` (data model + service) cùng
`crates/metap-lowcode-http` (admin API, sub-project 4 — crate riêng, tách khỏi `metap-http`
core) cho code thật. Ba quyết
định phạm vi đã chốt bên dưới đều được giữ nguyên trong bản Rust; điểm khác duy nhất so với
spec: sub-project 2 (runtime loader) đi xa hơn — hot-reload thật qua `ArcSwap`, không chỉ
load lúc boot — và sub-project 3 (publish validation) được gộp thẳng vào
`metap_lowcode::publish`/`rollback` thay vì tách riêng. File này giữ nguyên làm tài liệu lịch
sử/tham khảo thiết kế gốc, không cập nhật theo code nữa. Được chuyển từ
`docs/superpowers/specs/` vào ngày 2026-08-07 khi thư mục đó bị xóa (xem
`docs/architectures/09-adr.md`) — đây là mục duy nhất trong `docs/superpowers/` chưa được
ship tại thời điểm đó, nên nội dung của nó được giữ lại ở đây thay vì bị bỏ đi.

**Có trước quyết định ngày 2026-08-07 về việc chuyển `packages/core` sang Rust**
(xem [09. Architecture Decisions](architectures/09-adr.md)). Thiết kế bên dưới (data model, service contract của
draft/publish/rollback, ba quyết định phạm vi đã chốt) vẫn còn hiệu lực — về bản chất không
có phần nào trong đó gắn riêng với TypeScript. Các đường dẫn file cụ thể và mẫu code Zod
trong mục "Data model"/"Service" là từ layout TS cũ và cần được nhắm lại (re-target) sang
Rust khi công việc này thực sự được lên kế hoạch và xây dựng, không nên
coi là các đường dẫn file cần tạo ra theo đúng nghĩa đen.

---

# Low-code Metadata Storage & Versioning Design

## Vấn đề

Đích đến cao hơn mà Metap đã tuyên bố (`docs/vision.md`) là trở thành một nền tảng low-code: người vận hành định nghĩa, publish, và quản trị các ứng dụng nghiệp vụ từ metadata, không cần sửa source code cho đường đi tiêu chuẩn. `docs/low-code-platform-v1.md` đã vạch ra một lộ trình cụ thể, chia theo phase — Phase A ("Metadata Control Plane Foundation"), Phase B ("Builder UI and Safe Runtime Rules"), Phase C ("Platform Hardening") — nhưng tài liệu đó cố tình mang tính định hướng ("Status: exploratory"), không phải một spec sẵn sàng để triển khai.

Spec này bao phủ lát cắt cụ thể đầu tiên của Phase A: **lưu trữ metadata bền vững với draft/published versioning**, được phân rã thành sub-project đầu tiên trong bốn sub-project có thứ tự, cùng nhau tạo nên Phase A:

1. **Lưu trữ metadata bền vững + draft/published versioning** (spec này).
2. Runtime loader — hiện thực hóa metadata đã published thông qua pipeline `MetadataCompiler`/`MetadataRegistry` sẵn có, chứng minh `CrudService`/`QueryPlanner` hoạt động không đổi với các entity được tạo từ DB (DB-authored).
3. Publish validation pipeline — validation ngữ nghĩa sâu hơn (kiểm tra tham chiếu xuyên entity dựa trên registry gộp code+DB) chặn trước mỗi lần publish/rollback, một khi registry gộp của sub-project 2 tồn tại để kiểm tra dựa vào.
4. Metadata admin API — bề mặt HTTP mà một builder UI trong tương lai sẽ gọi.

Mỗi sub-project có chu trình spec → plan → implementation riêng của nó, khớp với thông lệ đã thiết lập của dự án cho các nỗ lực nhiều phần (ví dụ: công việc về rủi ro DB-coupling, công việc về frontend-architecture, cả hai đều đã làm trước đó trong cùng dự án tổng thể).

## Các quyết định đã chốt (ghi lại ở đây để spec này không phải bàn lại)

- **Các entity được tạo từ DB (DB-authored) cuối cùng sẽ thay thế hoàn toàn các file `*.entity.ts` được tạo từ code (code-authored)** — đây là hướng đi đã tuyên bố, không phải một kiến trúc dual-source vĩnh viễn. `crm.customers` không được migrate như một phần của Phase A; nó vẫn tiếp tục hoạt động như một code-authored entity trong khi đường DB-authored được xây dựng và chứng minh trên các entity mới trước. `MetadataRegistry` sẽ cần gộp cả hai nguồn trong một giai đoạn chuyển tiếp (thuộc phạm vi sub-project 2), nhưng việc gộp đó là scaffolding phục vụ cho quá trình migration, không phải một tính năng vĩnh viễn.
- **Metadata toàn cục, không phải theo từng tenant, cho Phase A.** Các DB-authored entity được quản lý trên toàn platform (một định nghĩa duy nhất, hiển thị với mọi tenant), khớp với cách các code-authored entity hoạt động hiện nay. Các định nghĩa entity tùy chỉnh theo từng tenant là một thay đổi kiến trúc lớn hơn đáng kể (một `MetadataRegistry` chỉ load một lần lúc boot sẽ cần trở thành dynamic/per-request) và được hoãn lại một cách tường minh sau Phase A.
- **Không hỗ trợ workflow cho các DB-authored entity trong Phase A** (spec gốc, viết cho TS —
  `WorkflowTransition.guard` khi đó là một hàm TypeScript; DB-authored entity không có code để
  viết một hàm như vậy, nên hình dạng lưu trữ của Phase A hoàn toàn không có key `workflow`).
  **Đã đóng ở Phase B (2026-08-17):** bản port Rust từ đầu đã biến `guard` thành dữ liệu khai
  báo (`PolicyCondition`, `metap-permission` — cùng type policy dùng), nhưng field đó vẫn bị
  `#[serde(skip)]` (chỉ để khớp hành vi loại trừ của bản TS cũ, không phải vì bản thân
  `PolicyCondition` không serialize được) — đây chính là gap thật khiến DB-authored entity chưa
  thể có workflow, không phải thiếu một "declarative rule engine" mới. Đã fix bằng cách bỏ
  `#[serde(skip)]` (`crates/metap-metadata/src/entity.rs`) và thêm `workflow: Option<EntityWorkflow>`
  vào `LowCodeEntityDefinition` (`crates/metap-lowcode/src/definition.rs`) — không cần migration
  DB mới (`definition` đã là cột `jsonb`, chấp nhận field mới tự nhiên qua serde).
  `metap_workflow::run_guard` vốn đã entity-agnostic từ trước (không có assumption nào về
  code-authored), nên toàn bộ `CrudService::transition` hoạt động cho DB-authored entity mà
  không cần đổi gì ở `metap-crud`/`metap-workflow`. Verify live qua HTTP thật: draft → publish
  → `GET /metadata/entities` phản ánh guard ngay không cần restart → tạo record thiếu field
  → transition bị `409 guard_failed` → tạo record đủ field → transition thành công — cùng path
  chính xác như một entity code-authored. Còn lại của Phase B (workflow editor UI, policy
  editor UI, publish preview/validation report) là công việc riêng, dùng nền tảng này.

## Phạm vi của sub-project này

**Trong phạm vi:** một storage/versioning service, DB schema của nó, và validation ở mức hình dạng (shape-level, dùng Zod) — có thể dùng được và test độc lập mà không cần đụng tới `MetadataRegistry`, `buildApp`, hay bất kỳ HTTP route nào.

**Ngoài phạm vi (hoãn lại tường minh sang các sub-project sau):**
- Đấu nối vào boot/`MetadataRegistry` (sub-project 2).
- Validation tham chiếu xuyên entity (ví dụ: `refEntity` của một field kiểu `"reference"` DB-authored trỏ tới một entity thật, đang tồn tại) — sub-project 3, một khi có một registry gộp để validate dựa vào.
- Bất kỳ HTTP endpoint nào (sub-project 4).
- Ngăn `name` của một draft trùng với tên một code-authored entity đã được đăng ký (ví dụ `"crm.customers"`) — điều này cần kiến thức về `MetadataRegistry` mà service này cố tình không có; hoãn lại cho sub-project 2 hoặc 4, tùy cái nào có đủ cả hai mảnh ghép trước.

## Data model

Tái sử dụng `EntityFieldSchema`/`EntityListViewSchema` (đã được định nghĩa trong `packages/core/src/core/metadata/entity-wire-schema.ts`, vốn đã là source of truth cho những gì đi qua wire) làm hình dạng cho `fields`/`listViews` của một định nghĩa được lưu trữ — không mô hình hóa một field-shape song song, mới.

**File mới `packages/core/src/core/metadata/low-code-entity-schema.ts`:**

```ts
import { z } from "zod";
import { EntityFieldSchema, EntityListViewSchema } from "./entity-wire-schema";

export const LowCodeEntityDefinitionSchema = z.object({
  name: z.string().min(1),
  label: z.string().min(1),
  fields: z.array(EntityFieldSchema),
  listViews: z.array(EntityListViewSchema),
});

export type LowCodeEntityDefinition = z.infer<typeof LowCodeEntityDefinitionSchema>;
```

**Hai bảng mới trong `packages/core/src/infra/db/schema.ts`** (tên được đặt cố tình khác biệt với bảng `metadata_versions` hiện có, vốn là một cache drift-detection lúc boot không liên quan, dành cho các code-authored entity — xem `MetadataDriftService` — không phải một kho versioning):

- **`low_code_entity_drafts`** — một dòng cho mỗi tên entity đang được biên soạn. Bản sao có thể thay đổi, đang dang dở; bị ghi đè ở mỗi lần save.
  - `entityName` (`varchar`, primary key)
  - `definition` (`jsonb`, not null) — một `LowCodeEntityDefinition`
  - `updatedAt` (`timestamp with time zone`, not null, default now)

- **`low_code_entity_versions`** — lịch sử publish chỉ-thêm-vào (append-only). Các dòng không bao giờ bị update hay xóa; rollback tạo ra một dòng mới thay vì ghi đè lên một dòng cũ.
  - `id` (`uuid`, primary key, default random)
  - `entityName` (`varchar`, not null, indexed)
  - `definition` (`jsonb`, not null) — một snapshot của `LowCodeEntityDefinition` tại thời điểm publish
  - `versionNumber` (`integer`, not null) — tăng dần theo từng `entityName`, bắt đầu từ 1
  - `publishedAt` (`timestamp with time zone`, not null, default now)
  - `restoredFromVersion` (`integer`, nullable) — được set khi version này được tạo ra bởi một lần rollback, ghi tên version number mà nó khôi phục; `null` với một lần publish thông thường
  - Ràng buộc unique trên `(entityName, versionNumber)`

Một lần rollback khôi phục version 3 không xóa version 4-5 cũng không hồi sinh lại dòng của version 3 — nó tạo ra version 6 với nội dung của version 3 và `restoredFromVersion: 3`, giữ cho lịch sử luôn nghiêm ngặt chỉ-thêm-vào và thân thiện với audit (cùng bản năng "không bao giờ sửa đổi quá khứ" như audit log `workflow_events` append-only sẵn có của dự án).

## Service

**File mới `packages/core/src/core/metadata/metadata-draft-service.ts`:**

```ts
export class MetadataDraftNotFoundError extends Error {}

export class MetadataDraftService {
  constructor(private readonly db: Database) {}

  async saveDraft(entityName: string, definition: LowCodeEntityDefinition): Promise<void>;

  async getDraft(entityName: string): Promise<LowCodeEntityDefinition | undefined>;

  async publish(entityName: string): Promise<{ versionNumber: number }>;

  async rollback(entityName: string, toVersionNumber: number): Promise<{ versionNumber: number }>;

  async getPublished(
    entityName: string,
  ): Promise<{ versionNumber: number; definition: LowCodeEntityDefinition } | undefined>;

  async listVersions(
    entityName: string,
  ): Promise<
    { versionNumber: number; publishedAt: Date; restoredFromVersion: number | null }[]
  >;
}
```

Hành vi:

- **`saveDraft`** — validate `definition` dựa trên `LowCodeEntityDefinitionSchema` (ném ra lỗi gốc của Zod khi thất bại, không cần một wrapper tùy chỉnh ở tầng này), sau đó upsert vào `low_code_entity_drafts` (insert-or-update theo `entityName`).
- **`getDraft`** — trả về `definition` của dòng draft hiện tại, hoặc `undefined` nếu chưa có (một entity hoàn toàn mới chưa được lưu gì).
- **`publish`** — đọc draft hiện tại; ném ra `MetadataDraftNotFoundError` nếu không có draft nào (không có gì để publish). Validate lại dựa trên `LowCodeEntityDefinitionSchema` (defense in depth — về nguyên tắc một draft có thể đã được ghi trước khi schema bị siết chặt hơn). Tính `versionNumber` tiếp theo là `1 + (versionNumber lớn nhất hiện có cho entityName này, hoặc 0)`, chèn một dòng `low_code_entity_versions` mới với `restoredFromVersion: null`, và trả về version number mới. Không xóa hay sửa dòng draft — draft và version vừa được publish có nội dung giống hệt nhau ngay sau khi publish, đây chính là trạng thái "không có thay đổi đang chờ" đúng đắn; các chỉnh sửa tiếp theo sẽ tự nhiên phân kỳ từ đó.
- **`rollback`** — đọc dòng version đích `(entityName, toVersionNumber)`; ném ra `MetadataDraftNotFoundError` nếu nó không tồn tại. Upsert `definition` của dòng đó vào dòng draft (để một builder UI trong tương lai hiển thị nội dung đã khôi phục như draft đang sống), tính `versionNumber` tiếp theo theo đúng cách `publish` làm (tiếp tục chuỗi tăng dần đơn điệu — rollback không bao giờ dùng lại hay tua lùi một version number), và chèn một dòng version mới với `restoredFromVersion: toVersionNumber`.
- **`getPublished`** — dòng có `versionNumber` cao nhất cho `entityName`, hoặc `undefined` nếu chưa từng được publish.
- **`listVersions`** — tất cả các version của `entityName`, sắp xếp mới nhất trước, phục vụ một UI lịch sử/rollback trong tương lai.

Không có validation xuyên entity nào (ví dụ kiểm tra `refEntity` của một field kiểu `"reference"` có đặt tên một entity thật) diễn ra ở đây — `saveDraft`/`publish` chỉ validate hình dạng của `definition` đang được lưu một cách độc lập. Sub-project 3 sẽ đặt lớp validation sâu hơn, có nhận biết registry, lên trên `publish`/`rollback` một khi có một registry gộp để validate dựa vào (sub-project 2).

## Testing

TDD, theo đúng kỷ luật backend đã thiết lập của dự án (viết test thất bại trước, xác nhận đỏ, rồi mới implement, xác nhận xanh) và các quy ước integration-test của nó (dùng Postgres thật qua test DB setup sẵn có, không dùng mock — các test của dự án này luôn nhất quán chạm vào một database thật).

`packages/core/src/core/metadata/metadata-draft-service.test.ts` — file mới, bao phủ:
- `saveDraft` rồi `getDraft` round-trip đúng `definition`.
- `saveDraft` với một hình dạng không hợp lệ (ví dụ một field thiếu `kind`) bị từ chối bởi Zod schema.
- `publish` khi không có draft ném ra `MetadataDraftNotFoundError`.
- `publish` sau `saveDraft` tạo ra version 1; một lần `publish` thứ hai sau một `saveDraft` khác tạo ra version 2.
- `getPublished` trả về nội dung và số hiệu của version mới nhất.
- `listVersions` trả về tất cả các version, mới nhất trước.
- `rollback` tới một version không tồn tại ném ra `MetadataDraftNotFoundError`.
- `rollback` tới một version cũ hơn đang tồn tại tạo ra một version mới với `restoredFromVersion` được set thành version đích, và cập nhật draft khớp với nó.

## File summary

- Create: `packages/core/src/core/metadata/low-code-entity-schema.ts`
- Create: `packages/core/src/core/metadata/metadata-draft-service.ts`
- Create: `packages/core/src/core/metadata/metadata-draft-service.test.ts`
- Modify: `packages/core/src/infra/db/schema.ts` (thêm các bảng `lowCodeEntityDrafts`, `lowCodeEntityVersions`)
- Generated: một migration Drizzle mới (`pnpm db:generate` + `pnpm db:migrate`, quy trình đã thiết lập của `packages/core`)
