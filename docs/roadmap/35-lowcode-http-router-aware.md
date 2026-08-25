## Phase 35: `metap-lowcode-http` Router-aware (2026-08-25)

2/3 hạng mục "làm tất luôn" — refactor crate đã được rà ở Phase 31 là hardcode `state.pool`.

**Phát hiện lại, chính xác hơn lúc rà sơ bộ ở Phase 31**: đọc kỹ file mới thấy rõ — entity low-
code vốn **cố ý thiết kế không có cột `tenant_id`** (ghi ngay trong doc comment đầu file, quyết
định Phase A đã chốt: "DB-authored entity metadata is global by design"). Vậy rủi ro không phải
"tenant A thấy entity của tenant B" theo nghĩa RBAC hàng-dữ-liệu, mà là **sai database**: mọi
handler đọc thẳng `state.pool` (pool nền tảng dùng chung, `AppState.pool` không bao giờ tự
`Router`-resolve) — một tenant `DedicatedDb` (như tenant của `jira-server`) publish 1 entity
low-code sẽ **lặng lẽ ghi vào DB nền tảng** thay vì DB riêng của tenant đó, và global-theo-pool sẽ
biến thành global-theo-toàn-platform, trộn lẫn với entity low-code của app khác dùng chung DB nền
tảng.

**Fix**: không cần đổi chữ ký hàm nào trong `metap-lowcode` (đã nhận `&PgPool` sẵn, không phải
generic `PgExecutor`) — chỉ cần thêm 1 hàm `resolve_pool(state, context)` gọi
`Router::pool_for(tenant_id)` (đã có sẵn, dùng cho `outbox-publisher`) ở **đầu mọi handler**, thay
`&state.pool` bằng pool vừa resolve. Với tenant `Schema` (crm-server hiện tại), `pool_for` trả về
đúng pool chung như trước — **hành vi không đổi**. Với tenant `DedicatedDb`, giờ trả đúng pool
riêng của tenant đó. `apply_registry` đổi chữ ký nhận `pool: &PgPool` tường minh thay vì tự đọc
`state.pool` bên trong (caller đã resolve sẵn, truyền vào).

12/12 handler sửa (`list_entities`, `set_enabled`, `save_draft`, `get_draft`, `publish`,
`preview_publish`, `rollback`, `get_published`, `list_versions`, `list_audit_events`,
`list_recent_audit_events`, `import_entities`) + `apply_registry`. Thêm dependency
`metap-permission`/`sqlx` vào `Cargo.toml` (trước đó không cần vì không tự gọi `Router`).

**Kiểm chứng sống đầy đủ qua HTTP thật trên `crm-server`** (không chỉ đọc code suy luận) —
`GET /admin/lowcode/entities` trả đúng 8 entity thật đã có; full cycle `save draft → get draft →
publish → audit trail → /metadata/entities` (xác nhận `apply_registry` hot-swap registry đúng,
không cần restart) — tất cả đúng như trước refactor, xác nhận tenant `Schema` mặc định của
crm-server không bị ảnh hưởng. Dữ liệu test (`test.lowcode_refactor_verify`) dọn sạch bằng SQL
trực tiếp sau khi verify (không có endpoint xoá entity low-code qua API, cùng tình huống 608-entity
cleanup ở Phase 19). `cargo build/fmt --check/clippy --workspace --all-targets -D warnings` +
`cargo test --workspace` (72 test suite) sạch.

**Chưa làm — wire router này vào `jira-server`**: refactor chỉ mở đường (crate giờ an toàn để
dùng cho tenant `DedicatedDb`), chưa merge `metap_lowcode_http::router()` vào
`apps/jira-server/src/main.rs`'s `build_router` — đó là bước riêng (thêm route, FE admin UI cho
custom field trên jira-fe) nếu chủ dự án muốn tiếp, không nằm trong 3 hạng mục "làm tất luôn" ban
đầu.

Diff chưa commit.
