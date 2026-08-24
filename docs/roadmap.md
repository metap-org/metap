# Roadmap

Tài liệu này chỉ theo dõi trạng thái ở cấp độ phase — bảng dưới là tóm tắt, chi tiết đầy đủ từng
phase (bối cảnh, quyết định, verify sống) nằm ở [`docs/roadmap/`](roadmap/) (mỗi phase 1 file,
tách ra 2026-08-24 vì file gộp đã quá dài — trước đó là 1 file duy nhất ~2400 dòng). Với một
feature nhỏ hơn một phase, xem `docs/features/`; về ownership/process của team, xem
`docs/team-charter.md`, `docs/CONTRIBUTING.md`, và `docs/agile-process.md`; checklist chi tiết ở
mức UI/UX cho frontend, xem `docs/frontend-checklist.md`.

## Trạng thái hiện tại (cập nhật 2026-08-24)

| Phase | Status |
|---|---|
| [0. Skeleton](roadmap/00-skeleton.md) | Đã xong |
| [1. Production-shaped Platform Kernel](roadmap/01-production-kernel.md) | Đã xong |
| [2. Metadata Compiler](roadmap/02-metadata-compiler.md) | Đã xong |
| [3. Permission Engine](roadmap/03-permission-engine.md) | Đã xong |
| [4. Query Planner V1](roadmap/04-query-planner-v1.md) | Đã xong |
| [5. Workflow Engine V1](roadmap/05-workflow-engine-v1.md) | Đã xong |
| [6. Frontend Core](roadmap/06-frontend-core.md) | Đã xong (chưa verify trên browser) |
| [7. Module Migration Strategy](roadmap/07-module-migration-strategy.md) | Đã xong — 4/4 module (crm.customers, sales.orders, inventory.movements, accounting.journal) |
| [8. Hardening](roadmap/08-hardening.md) | Đang làm — chỉ còn "tích hợp secret manager" (design-only 2026-08-17, chờ chốt target production); load test + backup/restore drill xong 2026-08-17 |
| [9. Multi-Service Evolution](roadmap/09-multi-service-evolution.md) | Trigger-based, đã rà soát lại 2026-08-17 — vẫn chưa trigger nào xảy ra, không có việc để làm |
| [10. Monorepo, npm publish](roadmap/10-monorepo-npm-publish.md) | Làm một phần |
| [11. Low-code Platform Backbone Architecture](roadmap/11-lowcode-platform-backbone.md) | Phase A + Phase B xong 2026-08-17 (Phase B's "policy editor UI" hoá ra đã có sẵn từ Phase 15); Phase C bắt đầu 2026-08-20 — metadata audit log, migration-impact check, import/export, operational visibility (cross-entity audit feed) xong (2026-08-22); còn lại approval workflow ("nếu cần", chưa có trigger) và schema isolation cấp tenant (chặn bởi quyết định kiến trúc lớn hơn — xem `docs/team-charter.md`'s "Metadata low-code theo từng Tenant") |
| [12. Rust Core Migration](roadmap/12-rust-core-migration.md) | Đã quyết định; Migration Order (bước 1-9) đã xong trong `crates/`; chưa cut over sang production |
| [13. Dynamic Cron Jobs](roadmap/13-dynamic-cron-jobs.md) | Backend đã xong; admin UI đã xong (Phase 15) |
| [14. Multi-language (i18n)](roadmap/14-i18n.md) | UI chrome + locale storage đã xong; metadata-label translation hướng đã chốt (translation-key/override table, 2026-08-22), chưa code — chưa có trigger |
| [15. Shared App Shell (UI kit, real login, permission-aware components)](roadmap/15-shared-app-shell.md) | Đã xong |
| [16. Multi-tenant SaaS Control Plane & Data Plane](roadmap/16-multi-tenant-saas.md) | Hướng B đã chốt. Giai đoạn 1-3 xong (Router, `provision-tenant`+`DedicatedDb`, HTTP tenant provisioning + platform-superadmin — 2026-08-16 → 2026-08-17); Giai đoạn 4: `VaultStore` (token) xong 2026-08-17, AppRole auth + auto-renewal + role lookup/RBAC qua Router (đóng bug login vỡ cho `dedicated_db`) + delete/deprovision tenant xong 2026-08-20 → 2026-08-21; `schema`/trial vẫn chưa có isolation thật; dynamic Vault creds/data-plane/capabilities/FE onboarding/deployment còn lại |
| [17. Metadata-driven Workflow Engine](roadmap/17-metadata-workflow-engine.md) | Increment 1 (on-transition trigger cho `metap-cron`) xong 2026-08-21; Increment 2 (chuỗi activity, `workflow_runs`) và Increment 3 (`wait_event` durable pause) vẫn approved, chưa code — chờ Increment 1 chạy thật lộ ra nhu cầu cụ thể |
| [18. Organization & Identity — P0](roadmap/18-organization-identity-p0.md) | Done 2026-08-22 — `RequestContext.context_attributes` (opt-in `AUTH_CONTEXT_ENTITY`, cache + invalidate endpoint), entity mẫu `hr.departments`/`hr.employees` qua low-code, org-scoped policy verify sống qua HTTP thật. P1 (`hr.positions`/`hr.locations`, `managerId` self-reference)/P2 (Legal Entity, Approval Authority...) vẫn proposed, chưa có trigger |
| [19. Table-per-entity](roadmap/19-table-per-entity.md) | 5/5 bước code-complete + e2e (2026-08-23) — **chưa wire vào bất kỳ binary nào**, `records` chung vẫn là nơi `CrudService` đọc/ghi thật cho mọi entity `crm-server`. `apps/jira-server` (Phase 21) là nơi duy nhất thật sự dùng bảng riêng, chỉ 4 entity của app demo đó |
| [20. Backend test kit (regression/performance/security)](roadmap/20-backend-test-kit.md) | Done phần lớn — security (`cargo audit`+CI, tenant-isolation/JWT/RBAC-ABAC test, CodeQL+Semgrep) và performance (k6 qua Docker + Grafana) xong; **2026-08-24 thêm OWASP ZAP (DAST)**, `testing/security/zap/run.sh`, chạy tay không CI. Còn thiếu: regression baseline/nightly tự động, Semgrep chưa wire CI |
| [21. `apps/jira-server` — table-per-entity thật](roadmap/21-jira-server-table-per-entity.md) | Done 2026-08-23 — 2 entity ban đầu (`jira.projects`/`jira.issues`), lần đầu `reconcile()` chạy trong boot sequence thật, không phải orchestrator đa-tenant |
| [22. `metap-storage` (object storage)](roadmap/22-metap-storage.md) | Done 2026-08-23 — `ObjectStore`/`S3ObjectStore` (SeaweedFS backend), wire thật ở Phase 27 |
| [23. `metap-cache` (caching layer)](roadmap/23-metap-cache.md) | Done 2026-08-23 — `Cache`/`MokaCache`/`RedisCache` (DragonflyDB), consumer đầu tiên: `PermissionService::with_cache` (policy-row cache) |
| [24. Xây đầy `apps/jira-server` cho demo](roadmap/24-jira-server-demo-buildout.md) | 2026-08-23 → 08-24 — backend: sprint/comment/4-state kanban workflow xong; frontend `apps/jira-fe`: dashboard+kanban board xong; outbox-publisher gap cho tenant dedicated-db tìm được lúc demo + đã fix (`OUTBOX_WORKER_INLINE`). 2 gap ghi nhận ở đây đã đóng ở Phase 26 (`PasteTokenFallback`) — `dev-tools mint-token`/`create-user`/`seed-admin` không tenant-aware vẫn còn mở |
| [25. Tenant auth pluggable (Bearer + Basic + OIDC)](roadmap/25-tenant-auth-pluggable.md) | Done 2026-08-24 — cả 3/3 bước (crate `metap-auth`, bảng `tenant_auth_configs`, refactor local login; HTTP Basic per-request; OIDC redirect/callback + JIT provisioning + FE `OidcCallbackPage`/SSO button), verify sống đầy đủ kể cả full HTTP round-trip qua fake IdP |
| [26. Làm đầy jira-server/jira-fe — auth thật, issue detail+comment, backlog, attachment](roadmap/26-jira-server-fill-out.md) | Done 2026-08-24 — cả 4/4 bước (auth thật; issue detail+comment; sprint backlog + bug thật fix ở `metap-query`; attachment — consumer thật đầu tiên của `metap-storage`, sau đó generalize ở Phase 27), verify sống đầy đủ mọi bước |
| [27. Generalize attachment thành năng lực platform (`metap-http`)](roadmap/27-attachments-platform.md) | Done 2026-08-24 — crate `metap-attachments` (2 cơ chế: bảng chung + bảng riêng theo entity), route generic `/api/{entity}/{id}/attachments*`, xoá bespoke cũ khỏi jira-server. Đánh đổi: mất bảo vệ chặn-xoá-khi-còn-attachment (đã verify + ghi nhận). Phát hiện + **đã fix** 1 gap migration thật (`0019`'s backfill làm tenant jira kẹt ở migration 18 — sửa bằng cách tái tạo tạm `control.tenants` rồi chạy migrator thật, không sửa nội dung migration đã áp) |

## Định hướng chưa lên phase (chưa có trigger)

Tám ý nảy sinh từ thảo luận kiến trúc, hợp lý về sản phẩm nhưng chưa có trigger cụ thể nên chưa
được lên thành phase: workflow hai chế độ (in-process + cross-module), workflow
visualize/hướng BPM nhẹ, Tiny deployment profile (single binary, không RabbitMQ), migration
path generic-table-sang-bảng-riêng, computed/derived field, schema versioning cho entity, entity
variant kiểu polymorphic/discriminated-union, và metadata low-code theo từng Tenant (hướng dài
hạn cho Phase 11C's "quy tắc cô lập schema cấp Tenant", ghi lại 2026-08-22). Chi tiết và lý do
chưa lên phase ở `docs/team-charter.md`'s "Định hướng đang ghi nhận, chưa có trigger". Không bắt
đầu việc nào trong số này mà chưa có feature brief (`docs/features/`) nêu trigger cụ thể.
