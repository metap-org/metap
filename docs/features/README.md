# Feature Briefs

Nơi theo dõi tính năng ở mức nhỏ hơn một phase trong `docs/roadmap.md`. Ba tài liệu process hiện có
mỗi cái trả lời một câu hỏi khác nhau — thư mục này lấp đúng chỗ trống còn lại:

| Tài liệu | Trả lời câu hỏi |
|---|---|
| `docs/roadmap.md` | Đang ở phase lớn nào, phase đó xong chưa |
| `docs/architectures/09-adr.md` | Vì sao chọn giải pháp kiến trúc này (quyết định *kỹ thuật*) |
| `docs/features/*.md` (thư mục này) | Một tính năng cụ thể làm gì, phạm vi tới đâu, khi nào coi là xong (yêu cầu *sản phẩm*) |

Không phải việc nhỏ nào cũng cần một file ở đây — xem Definition of Ready trong
`docs/agile-process.md`: bugfix rõ ràng, sửa doc, refactor cục bộ thì không cần. File ở đây dành
cho tính năng đủ lớn để cần thống nhất phạm vi *trước khi* code, để tránh việc code xong rồi mới
tranh cãi nó có nên làm vậy không.

## Quy trình

1. Copy `TEMPLATE.md` thành `<slug-tinh-nang>.md` trong thư mục này.
2. Điền các mục, đặt `Trạng thái: proposed`.
3. Khi được duyệt (ai duyệt: track sở hữu theo `docs/team-charter.md`, hoặc tự quyết nếu chỉ có
   một người), đổi `Trạng thái: approved` và thêm vào bảng bên dưới.
4. Khi bắt đầu code, đổi `Trạng thái: in-progress`. Nếu tính năng đủ lớn để gắn với một phase
   trong `docs/roadmap.md`, ghi rõ phase đó trong file.
5. Khi xong, đổi `Trạng thái: done` và để nguyên file lại — đây là lịch sử, không xoá.
6. Nếu quyết định không làm nữa, đổi `Trạng thái: rejected` kèm lý do ngắn, không xoá file.

## Danh sách

*(chưa có tính năng nào được ghi nhận theo quy trình này — bảng sẽ được điền dần khi có đề xuất
mới)*

| Tính năng | Trạng thái | Track | Phase liên quan |
|---|---|---|---|
