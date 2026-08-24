## Phase 18: Organization & Identity — P0 (2026-08-22)

`docs/features/03-organization-identity.md` P0 done — org structure vẫn không cần bảng lõi mới
(đúng thesis ban đầu: định nghĩa qua low-code builder như entity thường), gap thật duy nhất là
`RequestContext` chưa mang được attribute của caller ngoài role/tenant cho `PolicyCondition`'s
`fromContext`. Đóng gap đó:

- `RequestContext` (`crates/metap-permission/src/context.rs`) thêm
  `context_attributes: Option<serde_json::Map<String, Value>>`, `#[serde(flatten)]` — tự động
  lộ ra ở top-level của `to_value()`, `fromContext` đọc được ngay không cần sửa
  `evaluate_condition`.
- `AuthContext` (`crates/metap-http/src/auth.rs`) — opt-in qua `AUTH_CONTEXT_ENTITY` (tên một
  entity quy ước có field `userId`): sau khi role resolve xong, tra thêm record của caller trên
  entity đó, gán vào `context_attributes`. `None` (mặc định) là no-op tuyệt đối — không query
  thêm, không đổi hành vi cho deployment không bật tính năng.
- `context_attributes` **được cache** (`metap_http::cache::ContextAttributesCache`, mới, cùng
  mẫu `metap-control::RegistryCache`: `moka::future::Cache`, TTL `AUTH_CONTEXT_CACHE_TTL_SECONDS`
  mặc định 30s) — khác nguyên tắc "role không bao giờ cache" vì đây đọc một record nghiệp vụ
  thường, không phải role. Hai đường invalidate: đợi TTL, hoặc
  `POST /admin/users/{userId}/context/invalidate` (gate `AdminContext`) để ép hiệu lực ngay.
  Quyết định + đánh đổi ghi ở ADR mới (`docs/architectures/09-adr.md`).
- Verify: unit test `context_attributes` flatten đúng
  (`crates/metap-permission/src/context.rs`); e2e test tự động mới
  (`crates/metap-http/tests/http_server.rs`'s
  `auth_context_entity_enriches_org_scoped_policies_and_supports_explicit_cache_invalidation`) —
  dựng `test.profiles`/`test.tasks`, policy org-scoped qua `fromContext`, xác nhận 200/403 đúng
  theo phòng ban và hành vi cache-stale-rồi-invalidate. Verify sống thủ công thêm trên
  `crm-server` thật (`AUTH_CONTEXT_ENTITY=hr.employees`): `hr.departments`/`hr.employees` tạo qua
  chính low-code builder (`PUT /admin/lowcode/entities/{name}/draft` + `publish`), employee đọc
  record `hr.employees` cùng phòng ban → 200, khác phòng ban → 403. Toàn bộ e2e suite hiện có
  (không set `AUTH_CONTEXT_ENTITY`) chạy lại nguyên vẹn, không regression.

P1 (`hr.positions`/`hr.locations`, `managerId` self-reference + policy "chỉ manager trực tiếp"),
P2 (Legal Entity/Business Unit/Approval Authority...) vẫn `proposed`, chưa có trigger để code.

