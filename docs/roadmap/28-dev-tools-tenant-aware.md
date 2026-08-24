## Phase 28: `dev-tools` — `seed-admin`/`create-user` tenant-aware qua `Router`, `mint-token` cảnh báo default (2026-08-24)

Gap ghi nhận từ Phase 24/26 ("dev-tools mint-token/create-user/seed-admin không tenant-aware") đã
gây nhầm lẫn thật 2 lần trong các phase trước (tạo user `loginverify@example.com` nhầm vào DB
platform thay vì `metap_myjira`, phải xoá + tạo lại tay). Chủ dự án chọn fix cái này sau khi được
hỏi "tiếp theo làm gì" (loại bỏ 2 lựa chọn khác — Semgrep CI, table-per-entity cho crm-server —
chưa cần làm).

**`seed_admin`/`create_user`, done:**
- Cả 2 trước đây connect thẳng qua `DATABASE_URL` (đọc qua `dotenvy::dotenv()` từ thư mục chạy
  lệnh) — với tenant `dedicated_db`, ghi nhầm vào DB platform thay vì DB riêng của tenant đó,
  không lỗi gì cả (chỉ là 1 row không ai query tới).
- Thêm `router_for(pool)` (helper dùng chung) dựng `Router` giống hệt cách
  `apps/*-server/src/main.rs` làm lúc boot (`PostgresTenantRegistry` + `RegistryCache` +
  `EnvStore` — không replicate nhánh AppRole/Vault đầy đủ của `crm-server`, tenant dùng Vault vẫn
  seed DSN tay qua `vault-put-dsn` có sẵn trước, đúng luồng cũ). Cả 2 hàm giờ
  `router.begin(tenant_id.into())` rồi mới `assign_role`/`create_user`, commit tx — không thể
  ghi nhầm DB nữa vì đi đúng đường `Router` mọi request thật đi qua.
- **Verify sống đúng kịch bản đã gây bug trước đó**: chạy `create-user` từ thư mục
  `apps/jira-server` (không override `DATABASE_URL`) cho tenant `dedicated_db` — xác nhận user
  giờ vào đúng `metap_myjira`, DB platform vẫn 0 row.

**`mint_token`, done (gap khác — không phải sai DB, mà default tenant chưa provision):**
- Không đọc DB (chỉ ký JWT bằng keypair) nên không áp dụng fix qua `Router` được — thay vào đó
  in cảnh báo rõ ra stderr khi gọi thiếu `tenantId`, thay vì âm thầm default về
  `00000000-0000-0000-0000-000000000001` như trước (chính là nguyên nhân bug `relation
  "entities.jira_issues" does not exist` đã gặp ở Phase 24). Verify sống: `dev-tools mint-token`
  không tham số in đúng cảnh báo ra stderr.

**Kiểm chứng**: `cargo build/fmt --check/clippy --workspace --all-targets -D warnings` +
`cargo test --workspace` sạch. Diff chưa commit.
