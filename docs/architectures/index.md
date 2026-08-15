# Architecture

Thư mục này tài liệu hóa kiến trúc của Metap bằng ba framework bổ trợ cho nhau, không phải một:

- **[arc42](https://arc42.org)** là *khung sườn tài liệu* — 12 file được đánh số trong thư mục này, mỗi file ứng với một mục arc42, là câu trả lời của dự án cho câu hỏi "tại sao nó được xây dựng theo cách này, và đọc về X ở đâu."
- **[C4](https://c4model.com)** là *ký hiệu diagram* — được dùng bên trong các mục arc42 mà nó phù hợp một cách tự nhiên: System Context ([03](03-context.md)), Container + Component ([05](05-building-blocks.md)).
- **[4+1 View Model của Kruchten](https://en.wikipedia.org/wiki/4%2B1_architectural_view_model)** là *mô hình tư duy* — năm viewpoint của nó không phải một mục riêng; chúng được lồng vào bất kỳ mục arc42 nào bao phủ cùng phạm vi: Logical + Development → [05](05-building-blocks.md), Process + Scenarios ("+1") → [06](06-runtime.md), Physical → [07](07-deployment.md).

Không có framework nào cạnh tranh với nhau — arc42 tổ chức tài liệu, C4 vẽ hình, 4+1 đảm bảo không viewpoint nào (cấu trúc tĩnh, hành vi runtime, tổ chức source, deployment) bị bỏ sót.

## Các mục

1. [Introduction and Goals](01-introduction.md) — tầm nhìn, tổng quan yêu cầu, các bên liên quan, mục tiêu chất lượng hàng đầu
2. [Architecture Constraints](02-constraints.md) — ràng buộc kỹ thuật, tổ chức, và convention
3. [System Scope and Context](03-context.md) — bối cảnh nghiệp vụ/kỹ thuật, C4 System Context
4. [Solution Strategy](04-strategy.md) — các quyết định nền tảng và lý do
5. [Building Block View](05-building-blocks.md) — C4 Container + Component, Logical View, các core service, mô hình dữ liệu, thiết kế DB, ranh giới service, Development View
6. [Runtime View](06-runtime.md) — sequence diagram Process View, các scenario chính
7. [Deployment View](07-deployment.md) — Physical View, topology dev local
8. [Cross-cutting Concepts](08-cross-cutting.md) — các pattern trải rộng qua nhiều building block; nguyên tắc bảo mật và hiệu năng
9. [Architecture Decisions](09-adr.md) — sổ ghi quyết định (trước đây được đánh index tại `docs/superpowers/specs/`, đã xóa ngày 2026-08-07; nay ghi quyết định trực tiếp)
10. [Quality Requirements](10-quality.md) — cây chất lượng và các scenario cụ thể, kiểm chứng được
11. [Risks and Technical Debt](11-risks.md) — thẳng thắn, dựa trên trigger
12. [Glossary](12-glossary.md)

Để biết lộ trình build theo giai đoạn (cái gì đã xong, cái gì tiếp theo), xem `docs/roadmap.md`. Để biết lý do lựa chọn stack/công nghệ, xem `docs/why.md`. Để biết định hướng tương lai — hướng low-code, và một lộ trình cụ thể để có phiên bản đầu tiên của nó — xem `docs/vision.md` và `docs/low-code-platform-v1.md`; cả hai đều được chủ ý để ngoài bộ tài liệu arc42 này vì chúng mô tả một mục tiêu, không phải cái đã ship. `docs/multi-tenant-platform-design.md` mô tả một định hướng liên quan nhưng phạm vi hẹp hơn và cụ thể hơn: làm sao deploy Metap như một SaaS multi-tenant thật (tenant isolation, control plane, data plane storage evolution, reconciler DDL online) — cũng chỉ mang tính định hướng, chưa được xây dựng; các quyết định cốt lõi rút gọn từ nó nằm ở [09. Architecture Decisions](09-adr.md). `docs/modular-spi-architecture.md` mô tả một mục tiêu liên quan, vẫn chưa được quyết định: một ranh giới Capability SPI (Storage/EventBus/Scheduler/...) cho phép cùng một nguồn code chạy như một deployment "Tiny" dạng single-binary/SQLite hoặc một deployment doanh nghiệp phân tán — cũng chỉ mang tính định hướng, chưa được xây dựng. Để biết cách onboarding một contributor mới, quyền sở hữu module, và cách các phase roadmap còn lại được chia thành các luồng công việc song song, xem `docs/team-charter.md` và `docs/CONTRIBUTING.md` — cũng được chủ ý để ngoài bộ tài liệu arc42 này, vì chúng mô tả quy trình/cấu trúc đội nhóm, không phải bản thân hệ thống. `docs/agile-process.md` bao phủ nhịp độ làm việc hàng ngày (Definition of Ready/Done, chu kỳ review); `docs/features/` theo dõi từng feature nhỏ hơn một phase roadmap.
