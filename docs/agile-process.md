# Nhịp làm việc (Agile Process)

Viết ngày 2026-08-10, cùng đợt với `docs/team-charter.md`. Tài liệu này trả lời "hôm nay/tuần này
làm việc theo nhịp nào" — khác với `docs/roadmap.md` (đang ở phase nào) và `docs/team-charter.md`
(ai/track nào làm phase đó).

## Vì sao không dùng sprint cố định

Team hiện rất nhỏ (đang chuẩn bị mở rộng, xem `docs/team-charter.md`) và công việc đã tự nhiên
chia theo **work-stream liên tục** (Stream A/B/C trong `team-charter.md`) chứ không phải một backlog
chung cắt lát theo sprint 1-2 tuần. Ép vào sprint cố định lúc này là tạo ceremony không tương xứng
với quy mô team. Nhịp dưới đây thay thế sprint bằng: cadence review tối thiểu + Definition of
Ready/Done rõ ràng cho từng đơn vị việc.

Sẽ xét lại quyết định này khi: (a) số người đủ đông để cần đồng bộ lịch, hoặc (b) một stream cần
cam kết ngày giao cụ thể với bên ngoài (khách hàng, launch date) — lúc đó sprint/milestone theo
ngày mới có ý nghĩa hơn work-stream mở.

## Cadence review tối thiểu

- **`docs/roadmap.md`**: cập nhật ngay trong PR làm đổi trạng thái một phase (đã ghi ở
  `docs/CONTRIBUTING.md`) — không có cadence cố định cho việc *sửa*. Nhưng nên **đọc lại toàn bộ**
  bảng Current Status tối thiểu **2 tuần/lần** dù không có gì đổi, để bắt kịp phase nào đang bị
  đứng im lâu hơn dự kiến (dấu hiệu: status không đổi qua 2 lần review liên tiếp).
- **`docs/features/`**: khi có tính năng mới được đề xuất/duyệt/đóng, cập nhật ngay trong file đó —
  không đợi tới kỳ review.
- Không có standup bắt buộc. Nếu một stream có từ 2 người trở lên, người trong stream tự thống nhất
  tần suất đồng bộ riêng — tài liệu này không áp đặt.

## Definition of Ready

Một task được coi là sẵn sàng để bắt đầu code khi:

1. Biết rõ nó thuộc phase nào trong `docs/roadmap.md`, hoặc ghi rõ là việc ngoài roadmap.
2. Biết rõ track nào sở hữu (bảng ownership trong `docs/team-charter.md`) — để biết ai review.
3. Nếu là tính năng đủ lớn để cần mô tả phạm vi/tiêu chí chấp nhận (không phải bugfix nhỏ), đã có
   một file trong `docs/features/` theo template ở đó, trạng thái ít nhất là `approved`.
4. Nếu đụng ranh giới kiến trúc (xem `docs/CONTRIBUTING.md`'s "Ranh giới reviewer phải kiểm soát"),
   đã biết sẽ cần ADR hay không.

Việc nhỏ (fix bug rõ ràng, sửa doc, refactor cục bộ) không cần qua bước 3 — Definition of Ready ở
đây để tránh code trước khi hiểu phạm vi cho việc *lớn*, không phải thủ tục cho mọi PR.

## Definition of Done

1. Các check bắt buộc trong `docs/CONTRIBUTING.md` đã chạy pass (typecheck/lint/test phù hợp).
2. `docs/roadmap.md` đã cập nhật nếu trạng thái phase đổi.
3. File tương ứng trong `docs/features/` (nếu có) đã cập nhật trạng thái (vd: `in-progress` →
   `done`).
4. Quyết định kiến trúc non-trivial đã có mục trong `docs/architectures/09-adr.md`.
5. Nếu có bug thật phát hiện trong lúc verify (không phải lỗi giả định) — ghi lại vào roadmap hoặc
   ADR liên quan, giống cách Phase 13/15 đã làm — để lần sau không lặp lại cùng lỗi.

## Retro tối giản

Không có buổi retro định kỳ riêng. Thay vào đó: khi một phase/feature lớn đóng lại, ghi thẳng vào
`docs/roadmap.md` (hoặc file feature tương ứng) những gì phát sinh ngoài dự tính — bug thật tìm
được lúc verify, quyết định đổi hướng giữa chừng, v.v. Đây chính là hình thức retro của dự án này:
ghi tại chỗ, gắn với việc vừa xong, không tách thành một tài liệu riêng dễ bị bỏ quên.

## Leo thang quyết định

Quyết định cross-track (đụng nhiều track sở hữu module khác nhau) hoặc thay đổi ranh giới kiến trúc
đi theo đúng flow đã có trong `docs/team-charter.md` và `docs/CONTRIBUTING.md`: cần sign-off từ các
track liên quan, và một mục ADR nếu là quyết định kiến trúc. Tài liệu này không định nghĩa thêm quy
trình leo thang mới.
