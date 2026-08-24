## Phase 4: Query Planner V1

**Trạng thái: Đã xong**, được ship thành 3 sub-project, theo thứ tự sau:

1. **Chiến lược index cho hot field** — `EntityField.indexed`/`unique`
   (trước đây được khai báo nhưng chưa được đọc) giờ dẫn động
   `IndexReconciler` (`src/core/metadata/index-reconciler.ts`): partial
   expression index theo từng entity trên `records`, được reconcile tự động
   lúc boot (`CREATE INDEX CONCURRENTLY IF NOT EXISTS`, best-effort, không
   bao giờ chặn startup) và qua script thủ công `pnpm index:reconcile`. Đã
   bắt và fix một bug thật trong lúc implement: expression được index phải
   là `jsonb_extract_path_text(data, field)`, khớp byte-for-byte với chính
   expression filter/sort của `QueryPlanner` — một index được build trên
   dạng `data->>field` (tương đương về mặt ngữ nghĩa) sẽ âm thầm không bao
   giờ được planner của Postgres chọn.
2. **Chiến lược full-text search** — `EntityField.searchMode: "fts"` mới,
   opt-in (mặc định `"substring"`, tức hành vi ILIKE hiện tại không đổi),
   match qua `to_tsvector('simple', ...) @@ plainto_tsquery('simple', ...)`,
   được backing bởi một GIN index (loại index thứ ba của `IndexReconciler`,
   cùng kỷ luật khớp expression như trên).
3. **Keyset pagination** — cursor base64 mờ (opaque) (`src/core/query/cursor.ts`)
   được validate theo sort *đã resolve* (sau fallback); `QueryPlanner` build
   điều kiện `WHERE` của keyset dưới dạng OR hai mệnh đề tường minh (không
   phải một so sánh row-value đơn) vì tiebreaker `orderBy` hiện tại
   (`id ASC`) không đảo chiều theo hướng của field chính. `CrudService.list`
   thực thi với một lookahead `limit + 1` để tạo ra
   `page.nextCursor: string | null`; một cursor sai sort, hoặc malformed, sẽ
   trả về `400 invalid_cursor` sạch sẽ, không bao giờ là 500.

**Report query boundary — deferred, trigger-based** (chưa build), theo cùng
phong cách của Phase 9 chứ không phải ba item còn lại của phase này: chưa có
gap cụ thể nào thúc đẩy nó — chưa có UI/consumer reporting-analytics nào tồn
tại, và hệ thống hiện chỉ có đúng một entity (`crm.customers`). Xây dựng
`ReportService`/report-specific query path ngay bây giờ sẽ là hạ tầng cho
một workload chưa tồn tại, mâu thuẫn với chính triết lý tiến hóa
trigger-based của dự án (xem Phase 9, và mục Data Model Strategy của
`docs/architectures/05-building-blocks.md`: "none of it should be built
ahead of its trigger"). Trigger: xuất hiện nhu cầu export/aggregation cụ thể
(một UI hoặc consumer thật sự yêu cầu), hoặc một query trên OLTP path bị làm
chậm đáng kể bởi các access pattern kiểu report.

Mục tiêu ban đầu, để tham khảo:

- Hỗ trợ filter định nghĩa bằng metadata. (Phase 1/đã có sẵn.)
- Hỗ trợ safe sort field. (Phase 1/đã có sẵn.)
- Thêm keyset pagination. (Đã xong, sub-project 3 ở trên.)
- Thêm chiến lược full-text search. (Đã xong, sub-project 2 ở trên.)
- Thêm chiến lược generated column/index cho hot JSONB field. (Đã xong, sub-project 1 ở trên.)
- Thêm report query boundary. (Deferred, xem ở trên.)

