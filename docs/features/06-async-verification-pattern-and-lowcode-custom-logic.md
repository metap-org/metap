# Pattern xác minh bất đồng bộ (DNS/IP...) + gap logic tùy biến cho entity low-code

- **Trạng thái:** proposed (chỉ phần "Option B" bên dưới là đề xuất code thật — phần còn lại là
  pattern đã dùng được ngay với hạ tầng hiện có, ghi lại để tham chiếu sau)
- **Người đề xuất:** thảo luận với chủ dự án, 2026-08-26 (sau Phase 40)
- **Track sở hữu:** Backend Core (`metap-cron`) nếu làm Option B; không đụng track nào nếu chỉ
  dùng pattern có sẵn
- **Phase roadmap liên quan:** không thuộc phase nào (ghi chú tham khảo, chưa lên lịch)

## Vấn đề / động lực

Ví dụ cụ thể nêu ra: tạo 1 "website" (kiểu Cloudflare onboarding) cần 2 loại check trước khi coi
là `done`:
1. Check domain (format/ownership) — đồng bộ, tức thì.
2. Check DNS/IP đã resolve đúng chưa — **bất đồng bộ**, có thể mất thời gian (DNS propagate),
   cần thử lại nhiều lần cho tới khi resolve hoặc timeout.

Câu hỏi rộng hơn: pattern này wire vào metap kiểu gì (entity code-authored, vd `crm-server`), và
quan trọng hơn — **entity low-code** (định nghĩa qua admin UI, không có binary riêng để viết
Rust) thì làm sao gắn logic loại 2 vào, khi platform chủ trương "không hardcode business logic
vào `metap-*` lib"?

## Phạm vi

**Trong phạm vi** (pattern tham khảo, dùng ngay được với hạ tầng đã có — không cần code mới):

- **Check đồng bộ** (domain format/ownership): validate ở `metap-crud`'s field validation hoặc 1
  workflow guard (`PolicyCondition`) chặn transition đầu — không phải "add-on", là validate bình
  thường.
- **Check bất đồng bộ, entity code-authored** (vd `crm-server` tự viết entity `dns.websites`):
  - Bước 1 (thử ngay lúc tạo): `metap_infra::HandlerRegistry.on("<entity>.record.created", ...)`
    (Phase 40) — thử resolve 1 lần, nhiều case xong luôn ở bước này.
  - Bước 2 (poll định kỳ cho case chưa resolve): 1 `cron_jobs` row có sẵn (`triggerType:
    "schedule"`, `targetType: "webhook"` trỏ vào 1 endpoint nội bộ của chính binary đó, vd
    `POST /internal/websites/verify-pending`) — endpoint tự query record `pending_verification`,
    check lại, transition khi xong. Không cần tính năng `metap-cron` mới — `schedule` + `webhook`
    đã đủ, chỉ endpoint nội bộ là code nghiệp vụ thật.
- **Entity low-code, Option A (dùng ngay, không code mới)**: `cron_jobs`'s `webhook` target vốn
  data-driven — low-code admin tự cấu hình URL trỏ ra 1 service **ngoài platform** (tự viết, tuỳ
  ngôn ngữ) làm việc check DNS/IP, xong gọi ngược `POST /api/<entity>/{id}/transitions/verified`.
  Code nằm ngoài metap hoàn toàn — đúng UX low-code đã có từ Phase 13, không phải gap.

**Ngoài phạm vi** (đề xuất code, chưa làm):

- **Option B — `TargetType::DnsCheck` (hoặc tên khác) built-in, generic hoá**: giống hệt cách
  `TargetType::Email` được thêm ở Phase 39 — dev metap viết 1 lần
  (`target_config: {domainField, onResolved: {...}, onFailed: {...}}`), sau đó **mọi** entity
  low-code cấu hình dùng được qua `POST /admin/cron-jobs`, không cần code, giống UX cấu hình
  `webhook`/`email` hiện tại. Chỉ đáng làm nếu DNS/IP verification (hoặc pattern check tương tự:
  SSL cert, URL reachability...) là nhu cầu thật, không phải ví dụ minh hoạ.
- **Không đề xuất**: 1 engine script/expression sandbox để low-code tự viết logic tuỳ ý không
  cần deploy code (Option C, không nhắc ở phần Option A/B trên) — đây là feature lớn riêng biệt
  (sandbox execution, giới hạn tài nguyên, bảo mật), không nằm trong roadmap hiện tại, chỉ ghi
  nhận là hướng tồn tại về mặt lý thuyết.

## Tiêu chí chấp nhận

Chưa áp dụng — tài liệu này là ghi chú pattern/thảo luận, chưa phải commitment code. Nếu Option B
được duyệt triển khai thật, viết lại brief riêng theo tiêu chí cụ thể (dò theo cách Phase 39's
`TargetType::Email` đã làm: build → verify sống qua HTTP+RabbitMQ thật, không chỉ đọc code).

## Ranh giới kiến trúc bị đụng tới

Option B nếu làm sẽ đụng `metap-cron::model::TargetType` + `cron-scheduler::executor`'s
`dispatch()` — cùng ranh giới `TargetType::Email` đã đụng ở Phase 39, không cần ADR mới (đã có
tiền lệ).

## Rủi ro / phụ thuộc

Không phụ thuộc phase nào. Option B (nếu làm) nên tái dùng đúng hệ trigger `on_record_event`/
`schedule` đã có (Phase 17/38), không xây cơ chế trigger song song mới.
