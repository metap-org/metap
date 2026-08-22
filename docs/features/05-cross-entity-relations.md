# Cross-entity relations trong list view (3 mode)

- **Trạng thái:** Mode 2 done (2026-08-22); Mode 1/Mode 3 proposed, chưa có trigger
- **Người đề xuất:** chủ dự án, 2026-08-22 (phát hiện khi đánh giá Metap có đủ nghiêm túc để xây
  một app thật — dạng Jira — hay không)
- **Track sở hữu:** Backend Core
- **Phase roadmap liên quan:** không thuộc phase nào (fix + capability độc lập)

## Vấn đề / động lực

Một benchmark 500K record (`docs/roadmap.md`, cùng ngày) chứng minh filter/search/sort **trong
một entity** đủ nhanh (dưới 50ms) — nhưng nghiệp vụ thật không dừng ở một entity. Một list Issue
kiểu Jira cần hiển thị tên Project, tên Assignee, không chỉ ID thô. Rà code thật (không suy đoán)
xác nhận đây là gap có thật, không phải vấn đề hiệu năng:

- `metap-query/src/query_planner.rs`/`condition_to_sql.rs`: **không có JOIN nào cả** — mọi
  `plan_list` chỉ sinh `SELECT ... FROM records WHERE <điều kiện một bảng> ORDER BY ... LIMIT`.
- `CrudService::list()` **không hề enrich field `Reference`** — trả về `assigneeId: "uuid-xxx"`
  thô. Cơ chế enrich duy nhất có sẵn (`enrich_record_for_actions`) chỉ chạy cho single-record
  (`get`/`update`/`transition`/`delete`), và chỉ enrich field permission condition cần đọc —
  không phải để hiển thị, kết quả cũng **không** lộ ra response client (chỉ dùng nội bộ để
  evaluate `PolicyCondition`). `refDisplayField` có khai báo trong metadata nhưng trước đây chỉ
  được dùng ở FE cho form-picker (`ReferenceFieldInput.tsx`, autocomplete lúc nhập liệu) và
  `ReferenceFieldValue.tsx` (hiển thị 1 field — tự fetch `GET /api/:entity/:id` **cho từng field
  trên từng record**, N request cho N row).

3 hướng giải quyết, không loại trừ nhau — mỗi hướng nhắm một bài toán khác nhau:

## Mode 1 — Denormalize lúc ghi (proposed, chưa có trigger)

Copy giá trị `refDisplayField` vào ngay record lúc `create`/`update` (vd `assigneeId` +
`assigneeId__display` cùng nằm trên record). Chi phí đọc bằng 0 (không query thêm), nhưng lệch
dữ liệu nếu record được tham chiếu đổi giá trị display sau đó — cần chấp nhận "as of write time"
hoặc thêm job đồng bộ lại. Phù hợp khi nghiệp vụ **muốn** snapshot (vd hoá đơn giữ nguyên tên
khách hàng tại thời điểm xuất, không muốn cập nhật ngược nếu khách đổi tên sau).

**Thiết kế sơ bộ (chưa chốt):** một cờ metadata mới trên `EntityField` (vd
`denormalizeDisplay: true`, chỉ hợp lệ cùng `kind: reference` + `refDisplayField`) — khi bật,
`CrudService::create`/`update` copy giá trị `refDisplayField` vào một field ẩn cùng tên +
hậu tố quy ước, ghi thẳng vào `data` cùng transaction với record chính (không cần lock/job
riêng, đơn giản hơn Mode 3 nhiều). Trigger: một nghiệp vụ thật cần snapshot thay vì luôn-mới
(audit trail, hoá đơn, lịch sử) — chưa entity nào trong repo cần điều này.

## Mode 2 — Batch-hydrate sau khi list (done, 2026-08-22)

`CrudService::hydrate_related_display` (`crates/metap-crud/src/crud_service.rs`) — sau khi
`list()` đã filter/mask xong một trang record, với mỗi field `Reference` trên entity có khai báo
`refDisplayField`: gom mọi ID distinct trên trang đó, chạy **một** query `WHERE id = ANY($1)` cho
mỗi entity liên quan (không phải một query mỗi row), gắn kết quả vào `RecordDto.related_display`
(mới, `HashMap<String, String>`, `#[serde(skip_serializing_if = "Option::is_none")]` — vắng mặt
hoàn toàn ở mọi response khác ngoài `list`, và vắng mặt trên entity không có field nào khai báo
`refDisplayField` — đúng zero-cost khi không dùng, cùng nguyên tắc mọi cơ chế khác trong file này).

**An toàn quyền hạn:** chạy SAU `mask_record_for_read` (field bị field-level mask thì không được
hydrate), và bỏ qua toàn bộ field nếu caller không có quyền đọc entity đích
(`can_read_entity`, cùng gate thô mà `list`/`get` đã áp dụng cho chính entity đang list) — một
tiện ích hiển thị không được trở thành đường vòng đọc dữ liệu bị cấm. Không evaluate record-level
policy condition trên record liên quan (cần đúng JOIN mà Mode 3 chưa xây) — chỉ là gate entity-level
thô, không phải per-row.

**Không đổi**: `QueryPlanner`, không đổi shape SQL của `plan_list`, không filter/sort được theo
field của entity liên quan (chỉ hiển thị).

Verify: 1 e2e test mới (`crates/metap-crud/tests/crud_service_postgres.rs`'s
`list_hydrates_related_display_for_reference_fields_with_display_field` — 2 parent, 3 con trỏ
đúng cha khác nhau + 1 con không có tham chiếu, xác nhận resolve đúng giá trị theo từng row, batch
đúng khi nhiều row cùng trỏ một cha, record không có tham chiếu thì không có `related_display`).
Verify sống qua HTTP thật trên `crm-server`: entity `demo.projects`/`demo.tickets` tạo qua
low-code, `GET /api/demo.tickets` trả `relatedDisplay.projectId` đúng tên project cho từng ticket,
ticket không gán project thì không có `relatedDisplay`.

## Mode 3 — JOIN thật trong `QueryPlanner` (proposed, chưa có trigger)

Duy nhất trong 3 mode cho phép **filter/sort** theo field của entity liên quan (vd "Issue thuộc
Project đang active", "sort theo tên Assignee"). Đây là thay đổi kiến trúc nặng nhất, đụng tới
nhiều hơn một crate:

- `metap-query::plan_list`/`condition_to_sql.rs`: cần sinh SQL JOIN thật, không chỉ
  `jsonb_extract_path_text` trên một bảng — thay đổi nền tảng của module được chính ADR gọi là
  "Postgres-dialect seam rủi ro cao nhất" (`docs/architectures/09-adr.md`).
  - **Tiền đề bắt buộc**: mọi entity tham gia JOIN phải là `relationMode: referenced` (đã là mặc
    định — xem `docs/multi-tenant-platform-design.md` §3.3), và ở bảng `records` chung hiện tại,
    JOIN nghĩa là tự-join (`records AS a JOIN records AS b ON ...`) lọc theo 2 giá trị `entity`
    khác nhau — chạy được về mặt SQL nhưng không có FK thật (chưa tách bảng), planner khó tối ưu
    hơn JOIN giữa 2 bảng riêng thật (đợi table-per-entity, §3.3, mới có FK thật).
- **Permission record-level**: `docs/architectures/09-adr.md`'s "Cross-record permission
  condition" đã cố tình giới hạn dotted-path condition ở single-record method, nói rõ lý do
  "`list()` cần `QueryPlanner` JOIN, chưa xây" — Mode 3 chính là thứ mở khoá đó, nhưng đồng thời
  phải thiết kế lại record-level condition cho `list()` cùng lúc (WHERE clause phải phản ánh
  đúng permission trên CẢ record chính lẫn record liên quan) — không tách rời được.
- **Keyset pagination**: cursor hiện dựa trên `(field, id)` của MỘT bảng — sort theo field của
  bảng liên quan cần cursor phản ánh đúng giá trị đã JOIN, không chỉ giá trị trên bảng chính.

**Trigger**: một nghiệp vụ thật cần filter/sort theo field của entity liên quan (không chỉ hiển
thị) — Mode 2 đã đủ cho hiển thị; Mode 3 chỉ đáng bắt đầu thiết kế khi Mode 2 không đủ, kèm ADR
riêng vì đụng 3 mối quan tâm (query, permission, pagination) cùng lúc.

## Ranh giới kiến trúc bị đụng tới

- Mode 2: `RecordDto` (`metap-crud`) thêm field mới, `CrudService::list()` thêm một bước —
  additive thuần, không ADR.
- Mode 1: một cờ metadata mới trên `EntityField` — cần rà `MetadataCompiler`/OpenAPI
  generator/generated-types.ts khi thực sự làm (chưa làm).
- Mode 3: cần ADR riêng khi có trigger — đụng `metap-query` (nền tảng nhất), `metap-permission`
  (record-level condition), keyset pagination cursor.

## Rủi ro / phụ thuộc

- Mode 2 tăng số query cho `list()` từ 1 lên `1 + số field Reference có refDisplayField` — chấp
  nhận được vì batch theo trang (không theo row), nhưng đáng theo dõi nếu một entity có nhiều
  field Reference cùng lúc trong một list view.
- Mode 3 phụ thuộc gián tiếp table-per-entity (§3.3, FK thật) để JOIN hiệu quả hơn tự-join trên
  bảng `records` chung — không bắt buộc, nhưng đáng cân nhắc cùng lúc nếu cả hai đều có trigger
  gần nhau.
