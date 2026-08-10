# Tầm nhìn

Ngày: 2026-08-02

Trạng thái: directional (định hướng) — không phải một mô tả as-built. Để biết những gì thực sự đã được triển khai, xem [`docs/architectures/01-introduction.md`](architectures/01-introduction.md) (arc42 Section 1), có đoạn "Tầm nhìn" ngắn gọn là phiên bản as-built súc tích đi kèm với phát biểu đầy đủ hơn này.

## Ý tưởng cốt lõi

Metap không có ý định dừng lại ở việc chỉ là một business application đơn lẻ, hay thậm chí chỉ là một metadata-driven CRM core.

Định hướng của nó là:

> một platform core có thể tái sử dụng để xây dựng các business application, với low-code là đích đến cao hơn.

Về mặt thực tế, điều đó có nghĩa là hai mục tiêu lồng nhau:

1. xây dựng một execution core metadata-driven vững chắc
2. phát triển core đó thành một low-code platform thực sự theo thời gian

## Những gì đã tồn tại

Metap đã có nền tảng của một platform, chứ không chỉ là một app đơn lẻ:

- các Entity được định nghĩa bằng metadata, được compile và validate như một runtime artifact (không phải config thụ động)
- CRUD tổng quát, query planning bị ràng buộc bởi metadata, và Workflow điều khiển bởi metadata
- permission được điều khiển bởi policy và thực thi phía server
- các frontend rendering primitive có thể tái sử dụng (`packages/platform-react`)
- ranh giới rõ ràng giữa reusable core và từng business module (`crates/metap-*` + `apps/<module>`, ví dụ `apps/crm-server`), và giữa reusable frontend với demo consumer của nó (`packages/platform-react` + `apps/crm-fe`) — một cấu trúc workspace được chọn riêng để giữ định hướng này ít tốn kém, không phải một sở thích kỹ thuật chung chung (core đã chuyển từ TypeScript sang Rust vào 2026-08-07, xem [`docs/architectures/09-adr.md`](architectures/09-adr.md); bản thân cấu trúc ranh giới không đổi)
- một contract được generate (không phải duy trì thủ công) giữa backend và frontend cho entity metadata, để hai bên không thể âm thầm lệch nhau theo cách được mô tả bên dưới trong phần "Điều này có ý nghĩa gì cho các quyết định hiện tại"

Điều này đã lớn hơn một CRM app đơn lẻ, nhưng về cơ bản vẫn là một platform core được author bởi developer: metadata sống trong code (các Rust module định nghĩa Entity, ví dụ `apps/crm-server/src/entities/customer_entity.rs`), không phải trong một database mà người không phải developer có thể chỉnh sửa.

## Đích đến cao hơn

Đích đến cao hơn không chỉ là "thêm module" hay "thêm CRUD."

Đó là:

> một low-code platform nơi operator hoặc admin nâng cao có thể định nghĩa, publish, và quản trị (govern) các business application từ metadata, mà không phụ thuộc vào việc chỉnh sửa source code cho luồng chuẩn (standard path).

Hệ thống tương lai đó phải giữ được các đảm bảo (guarantee) phía backend mà Metap đã có ngày nay:

- tenant isolation phía server
- permission enforcement phía server
- optimistic locking
- tính toàn vẹn của Workflow
- việc truyền business event một cách đáng tin cậy

Xem [`docs/low-code-platform-v1.md`](low-code-platform-v1.md) để biết một lộ trình cụ thể, theo từng phase, hướng tới phiên bản low-code platform thực sự đầu tiên — những gì còn thiếu, thứ tự xây dựng, và những gì cố ý chưa xây dựng quá sớm.

## Định hướng kiến trúc

Metap nên đạt được điều đó bằng cách phát triển *mô hình authoring và control-plane*, chứ không phải bằng cách thay thế runtime engine.

Lộ trình dự kiến:

- **trạng thái hiện tại** — metadata được author bằng code trên nền một reusable runtime core
- **trạng thái tiếp theo** — metadata được lưu trữ (persisted), có versioning, kèm validation và publish control
- **trạng thái cao hơn** — thiết kế và quản trị (governance) low-code application trên cùng một execution core

## Điều này có ý nghĩa gì cho các quyết định hiện tại

Khi đưa ra các lựa chọn kiến trúc trong dự án hiện tại, hãy ưu tiên những quyết định giữ cho lộ trình này luôn mở:

- ranh giới package và service rõ ràng
- các public contract dùng chung, được generate (không copy thủ công) giữa các package không thể thấy source code của nhau
- validation và versioning cho metadata
- ưu tiên runtime safety hơn là sự linh hoạt ad hoc
- governance rõ ràng cho các thay đổi schema, workflow, và permission

Tránh những quyết định khiến việc bổ sung một low-code control plane trong tương lai trở nên khó khăn hơn:

- gắn chặt (coupling) business behavior trực tiếp vào các code path đặc thù của app
- bỏ qua (bypass) metadata runtime phía server
- đưa vào user scripting không kiểm soát quá sớm

## Quan hệ với các tài liệu khác

- [`docs/architectures/01-introduction.md`](architectures/01-introduction.md) là phát biểu vision súc tích, as-built, nằm trong bộ tài liệu arc42 mô tả kiến trúc như nó tồn tại ngày nay — không phải một mục tiêu chưa được triển khai.
- [`docs/roadmap.md`](roadmap.md) theo dõi roadmap triển khai chính thức, theo từng phase, cho phạm vi dự án hiện tại.
- [`docs/low-code-platform-v1.md`](low-code-platform-v1.md) mô tả một lộ trình thực tế, theo từng phase, từ kiến trúc hiện tại hướng tới phiên bản low-code platform thực sự đầu tiên.

Tài liệu này cố ý ngắn hơn hai tài liệu kia. Nhiệm vụ của nó chỉ là phát biểu định hướng một cách rõ ràng:

> Đích đến cao hơn của Metap là low-code, được xây dựng trên nền metadata-driven core hiện đã tồn tại.
