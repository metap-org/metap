# Nâng cấp Frontend Platform — vượt khỏi demo API

- **Trạng thái:** proposed
- **Người đề xuất:** phản hồi từ việc tự test `apps/crm-fe` sau khi Phase 7 xong (2026-08-10)
- **Track sở hữu:** Frontend Platform (có thể kéo theo Backend Core)
- **Phase roadmap liên quan:** chưa gắn — cần scope trước khi gắn vào một phase cụ thể

## Vấn đề / động lực

`apps/crm-fe` hiện chỉ đủ để chứng minh API hoạt động (login, list/form generic, admin kit tối
giản từ Phase 15) — không đủ để thực sự dùng thử 4 entity mới của Phase 7 một cách có ý nghĩa.
Tự test cho thấy UI quá đơn giản, chưa lộ ra được các khả năng thật của platform (list view thứ
hai, workflow nhiều nhánh, field kiểu Reference/Money hiển thị ra sao).

**Đây không phải "chưa có trigger" như 4 ý ở mục dưới** — có trigger thật (dogfooding vừa lộ ra
gap), chỉ là *chưa được scope* để biết chính xác cần làm gì.

## Phạm vi

**Chưa chốt — cần một vòng scoping riêng trước khi implement.** Ghi lại ở đây các gap cụ thể đã
biết tính đến 2026-08-10, để vòng scoping đó có điểm bắt đầu thay vì từ số 0:

- ~~List view thứ hai (`ledger` của `accounting.journal`) hiện không gọi được qua API list~~ —
  **Đã xong (2026-08-22)**: `plan_list` (`crates/metap-query/src/query_planner.rs`) nhận thêm
  `ListInput.list_view: Option<String>`, route `GET /api/:entity?listView=<name>`
  (`crates/metap-http/src/routes/records.rs`) truyền qua tham số query cùng tên. Không truyền
  giữ nguyên hành vi cũ (`list_views.first()`); tên không tồn tại trả `400 unknown_list_view`
  (không âm thầm fallback về view mặc định — đổi view mà không báo lỗi dễ khiến FE hiển thị nhầm
  cột/filter mà không biết). 2 test e2e mới
  (`crates/metap-query/tests/query_planner_postgres.rs`), verify sống qua HTTP thật trên
  `accounting.journal`'s `ledger`. 3 gap còn lại (pagination admin UI kit, render field
  Reference/Money/Date, điều hướng entity trong demo app) cần nhìn qua browser thật để scope —
  ngoài khả năng tự verify của backend track.
- Admin UI kit (Phase 15) chưa có pagination trên list nào, `PolicyCondition`/cron
  `targetConfig` là raw-JSON thay vì structured builder, chưa có bộ chuyển tenant.
- Chưa rõ `GeneratedList`/`GeneratedForm` hiện render field `Reference`/`Money`/`Date` (3 kind
  mới toanh sau Phase 7) đẹp tới đâu — chưa từng nhìn qua browser thật với 4 entity mới.
- Chưa có cách điều hướng giữa các entity dễ dàng trong demo app (`EntitiesPage` hiện có nav
  tĩnh, chưa rõ có tự động liệt kê entity mới hay không).

**Vì sao ghi chú "phase này chắc sẽ nặng, kéo theo core":** ít nhất gap đầu tiên (list view
selection) đã xác nhận cần sửa `crates/metap-query`/`crates/metap-http`, không chỉ
`packages/platform-react`/`apps/crm-fe`. Nhiều khả năng còn phát sinh thêm khi scoping kỹ hơn
các gap còn lại — không giả định trước khi thực sự scope.

## Tiêu chí chấp nhận

Chưa có — sẽ điền khi feature này chuyển `proposed` → `approved`, sau một vòng scoping riêng
(xem "Phạm vi" ở trên).

## Ranh giới kiến trúc bị đụng tới

Chưa xác định đầy đủ — ít nhất gap list-view-selection đụng `metap-query`/`metap-http`
(`crates/metap-query/src/query_planner.rs`'s `plan_list`, route `/api/:entity`), có thể cần
ADR nếu đổi shape `ListInput`/response.

## Rủi ro / phụ thuộc

Không phụ thuộc Phase 7 (đã đóng). Track sở hữu chéo Frontend Platform + Backend Core — nếu
approve, cần sign-off cả hai track theo `docs/team-charter.md`.
