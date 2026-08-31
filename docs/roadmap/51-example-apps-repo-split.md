## Phase 51: Tách `apps/crm-server`/`crm-fe`/`jira-server`/`jira-fe` ra 2 repo riêng (2026-08-31)

Đúng việc Phase 47's ghi chú cuối để lại: "cố ý chưa làm: tách `apps/crm-server`/`jira-server`/
`crm-fe`/`jira-fe` ra repo riêng". Trigger: chủ dự án yêu cầu tạo 2 repo example tương tự
`../metap-demo-waf` (repo downstream-consumer thật đã tồn tại từ trước, xây trên `metap` qua
`path` dependency).

**Quyết định trước khi làm (hỏi chủ dự án, không tự đoán):**
- Gộp backend+frontend chung 1 repo/app (giống `metap-demo-waf/data-plane`), không tách backend
  và frontend thành 4 repo riêng.
- Xoá hẳn `apps/crm-server`/`apps/crm-fe`/`apps/jira-server`/`apps/jira-fe` khỏi `metap` sau khi
  copy — không giữ song song.
- Không giữ git history khi chuyển (copy file + init mới, giống cách `metap-demo-waf` được tạo
  từ `templates/metap-app`, không phải `git filter-repo`/subtree).
- Phát sinh thêm 1 quyết định giữa chừng (root `package.json`'s script nhóm `db:migrate`/
  `mint-token`/`seed:admin`/4 worker/`dev:rs`/`start` đều `cd apps/crm-server` để có `.env`/
  `keys/` — xoá `apps/crm-server` sẽ làm hỏng cả nhóm): chủ dự án chọn xoá luôn nhóm script đó
  khỏi `metap`, dev-tooling local giờ hoàn toàn thuộc về repo downstream.

**Kết quả:**
- 2 repo mới, `../metap-demo-crm` và `../metap-demo-jira`, cấu trúc phẳng (không có tầng
  `data-plane/` như `metap-demo-waf` — không có nhu cầu multi-plane): `Cargo.toml`+`src/` ở gốc
  repo, `web/` chứa frontend. `Cargo.toml`'s `metap`/`metap-reconciler`/`notification-worker` (crm)
  hoặc `metap-outbox-publisher`/`metap-storage` (jira) là `path` dependency vào `../metap/crates/*`
  (local dev, swap `git` cho CI/deploy — comment sẵn trong file, đúng tiền lệ `metap-demo-waf`).
  `web/package.json`'s `@metap/platform-ui`/`@metap/ui` là `link:../../platform-ui`/
  `link:../../design-system` (2 cấp, không phải 3 cấp như trong `metap` cũ vì bớt 1 tầng `apps/`).
- Toàn bộ file lấy từ `git ls-files` (không phải `find`) để không copy nhầm `.env`/`keys/*.pem`
  local đã gitignore. 1 file duy nhất không copy được: `reactRouterNavigationAdapter.tsx` — đã bị
  xoá khỏi `metap` từ phiên trước đó cùng ngày (gộp vào `platform-ui` làm
  `ReactRouterNavigationProvider` export sẵn, `git ls-files` vẫn liệt kê path cũ vì chưa commit).
  Không cần copy lại — cả 2 repo mới import thẳng từ `@metap/platform-ui`.
- Sửa lại toàn bộ path tương đối bị lệch do đổi cấu trúc (cấp `../../..` → `../..` cho `web/`,
  `../../` → `../` cho `Cargo.toml`/scripts/`.env.example`), đổi mọi lệnh `pnpm X:rs`/`pnpm
  mint-token`/... trong comment sang `cargo run --manifest-path ../metap/crates/<crate>/Cargo.toml
  -- <args>` tương ứng (nhóm script đó không còn tồn tại). `Dockerfile` viết lại theo mẫu
  `metap-demo-waf/data-plane/Dockerfile` (backend-only, cùng caveat: build context không với tới
  `../metap` nên chưa build được với `path` dependency — chỉ hoạt động sau khi đổi sang `git`
  dependency, y hệt gap đã có sẵn ở `metap-demo-waf`, không phải gap mới).
- **Verify bằng build thật trước khi xoá gì ở `metap`** (thứ tự an toàn: tạo+verify bản mới trước,
  xoá bản cũ sau): `cargo build` sạch cho cả `metap-demo-crm`/`metap-demo-jira` (path dependency
  resolve đúng vào `../metap/crates/*`), `pnpm install` + `tsc -b --noEmit` sạch cho cả 2 `web/`
  (link resolve đúng 2 cấp).
- Xoá `apps/crm-server`/`apps/crm-fe`/`apps/jira-server`/`apps/jira-fe` khỏi `metap` (`git rm -r
  --cached` + xoá working tree — chỉ stage, không commit). Root `Cargo.toml`'s `members` bớt 2
  dòng. Root `package.json` rút còn 3 script (`gateway:rs`/`test:rs`/`test:rs:e2e` — thuần
  cargo-wrapping, không còn app nào để `cd` vào). `pnpm-workspace.yaml` giữ nguyên dù giờ
  `apps/*` cũng khớp rỗng như `packages/*` đã vậy từ trước — theo đúng tiền lệ "khớp rỗng, vẫn
  giữ" đã ghi trong `CLAUDE.md`. `.github/workflows/ci.yml`'s job `frontend` (typecheck/lint/
  format/test qua pnpm) xoá hẳn — không còn gì để chạy. `.github/workflows/e2e-manual.yml`'s
  bước "Apply migrations" bỏ `working-directory: apps/crm-server` (không cần — `DATABASE_URL` đã
  set qua `env:` cấp job, không phải file `.env`).
- `CLAUDE.md` sửa rộng: mục "Sample apps" cũ (2 bullet dài mô tả chi tiết `crm-server`/
  `jira-server`) rút gọn thành 1 đoạn trỏ sang README của 2 repo mới; mọi bullet crate khác có
  nhắc `apps/crm-server`/`apps/jira-server` (khoảng 15 chỗ — `metap`/`metap-control`/
  `metap-reconciler`/`metap-jwks`/`metap-grpc`/`metap-graphql`/`metap-storage`/`metap-cache`/
  `outbox-publisher`/`notification-worker`/`cron-scheduler`/`graphql-gateway`/"Architecture"/
  "Metadata-driven records"/"Table-per-entity"/"Frontend") đổi sang trỏ `../metap-demo-crm`/
  `../metap-demo-jira` hoặc diễn đạt tổng quát "a downstream binary". Section "Commands" viết lại
  hoàn toàn — chỉ còn lệnh build/test crate + `gateway:rs`/`loadtest:customers`, không còn lệnh
  nào cần `.env`/`keys/` trong `metap`. Section "Frontend" viết lại — không còn app nào trong repo
  này để mô tả, trỏ hẳn sang `platform-ui`/`design-system`/2 repo demo. `crates/graphql-gateway/
  README.md`'s walkthrough cũng đổi từ `cd apps/jira-server && pnpm dev:rs` sang `cd
  ../metap-demo-jira && cargo run`.
- **Cố ý chưa quét hết**: `docs/roadmap/*.md` (~50 file phase lịch sử) và rải rác doc-comment
  trong `crates/*/src/*.rs` vẫn còn nhắc `apps/crm-server`/`apps/jira-server` — giữ nguyên có chủ
  đích, vì đó là bản ghi lịch sử tại thời điểm quyết định (đúng như Phase 46 cũng từng ghi rõ: "các
  entry roadmap lịch sử... giữ nguyên làm bản ghi thời điểm quyết định"), không phải tài liệu sống
  cần khớp trạng thái hiện tại như `CLAUDE.md`.

Chưa làm (ngoài phạm vi yêu cầu lần này): git history thật cho 2 repo mới (chủ dự án chọn không
cần), Docker build thật cho `metap-demo-crm`/`metap-demo-jira` (cùng gap `metap-demo-waf` đã có,
cần đổi `path` → `git` dependency trước), quét/sửa hết tham chiếu trong `docs/roadmap/*.md`/
doc-comment rải rác (cố ý, xem trên).
