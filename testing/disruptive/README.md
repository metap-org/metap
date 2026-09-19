# Disruptive / chaos testing

Trụ test thứ 4, mới thêm 2026-09-19 — trước đó **không tồn tại**: 0 script, 0 test, 0 doc. Resilience
code thật (`metap_infra::run_resilient_consumer`, `metap-outbox-publisher`'s reconnect loop) đã có
từ lâu nhưng 0 test nào thật sự kill/pause 1 container để chứng minh nó hoạt động — mọi test
trước giờ chỉ *cần* Postgres/RabbitMQ đang chạy, không bao giờ *làm gián đoạn* nó giữa chừng.

## Triết lý

- **Chỉ nhắm vào container dev tự quản của workspace này** (`metap-postgres-1`, `metap-rabbitmq-1`
  — tên mặc định `docker compose` sinh ra khi không set `container_name:`, hoặc 1 container
  throwaway tự tạo riêng cho 1 test cụ thể). **Không bao giờ** trỏ biến môi trường
  `DATABASE_URL`/`RABBITMQ_URL` của test chaos vào bất kỳ thứ gì chia sẻ với môi trường khác hay
  production — không có cơ chế nào ở đây tự nhận diện/bảo vệ khỏi việc đó, tự kỷ luật khi chạy tay.
- **`#[ignore]`d như mọi e2e test khác**, cộng thêm: cần `docker` CLI trên PATH và quyền
  stop/start/pause container thật — precondition chặt hơn "chỉ cần DATABASE_URL", ghi rõ trong
  doc comment từng test.
- **Không phải bash chaos script tự chấm điểm** — code thật nằm trong `crates/*/tests/*.rs`
  (`#[ignore]`d Rust e2e test tự gọi `std::process::Command::new("docker")`), đúng convention
  "không có logic test/scan nằm trong file `.sh`" mà `performance/k6/run.sh`/`security/zap/run.sh`
  đã theo từ đầu.
- **v1: đúng 3 kịch bản**, nhắm 2 cơ chế resilience có code nhưng trước đây 0 test. Mở rộng sau,
  không hứa hẹn 1 chaos framework đầy đủ ngay — xem "Chưa làm" bên dưới.
- **Không report-only** (khác performance/pentest) — đây là assertion đúng/sai xác định (reconnect
  có hoạt động hay không), không phải con số cần phán đoán của người — `.github/workflows/
  disruptive.yml` thất bại thật khi 1 kịch bản fail, dù vẫn chỉ chạy tay/cron hàng tuần, không bao
  giờ trên push/PR.

## Kịch bản

| Test | Mục tiêu | Cách gây fault | Thời lượng | Kỳ vọng |
|---|---|---|---|---|
| `crates/metap-outbox-publisher/tests/chaos_postgres.rs`'s `outbox_publisher_recovers_after_rabbitmq_restarts_mid_run` | `outbox_publisher::run`'s reconnect-with-backoff (phía publish) | `docker stop metap-rabbitmq-1` 10s, rồi `docker start` | ~20-30s | Toàn bộ outbox row enqueue trước đó phải có `published_at` trong vòng 60s sau khi RabbitMQ sống lại — không cần restart process |
| `crates/metap-infra/tests/chaos_rabbitmq.rs`'s `resilient_consumer_reconnects_and_resumes_after_rabbitmq_restarts` | `run_resilient_consumer` (phía consume — nền tảng chung cho `notification-worker`/`cron-scheduler`'s dispatch+trigger) | `docker stop metap-rabbitmq-1` 10s, rồi `docker start` | ~50-60s | Consumer subscribe lại tự động, nhận được message publish *sau* khi RabbitMQ sống lại, không cần restart process |
| `crates/metap-control/tests/chaos_postgres.rs`'s `dedicated_tenant_request_fails_within_a_bounded_time_when_its_postgres_is_paused` | `Router::pool_for` với tenant `DedicatedDb` khi Postgres riêng của tenant đó không phản hồi | `docker pause` 1 container Postgres throwaway riêng cho test (không đụng `metap-postgres-1` — pause cái đó sẽ phá mọi test khác chạy song song) | ~35s | **Ghi nhận hành vi hiện tại**, không phải SLA đã xây: gọi phải trả lỗi rõ ràng trong khung thời gian hợp lý, không hang vô hạn. Chạy thật lần đầu (2026-09-19): fail sau ~30s với `pool timed out while waiting for an open connection` — sqlx's default acquire timeout, không phải cơ chế retry/circuit-breaker nào `Router` tự xây (không có) |

## Chạy tay

```bash
docker compose up -d postgres rabbitmq   # từ repo metap root
DATABASE_URL=postgres://metap:metap@localhost:5433/metap cargo run -p metap-db-migrate

DATABASE_URL=postgres://metap:metap@localhost:5433/metap \
RABBITMQ_URL=amqp://metap:metap@localhost:5672 \
  cargo test -p metap-outbox-publisher --test chaos_postgres -- --ignored --nocapture

RABBITMQ_URL=amqp://metap:metap@localhost:5672 \
  cargo test -p metap-infra --test chaos_rabbitmq -- --ignored --nocapture

DATABASE_URL=postgres://metap:metap@localhost:5433/metap \
  cargo test -p metap-control --test chaos_postgres -- --ignored --nocapture
```

Cả 3 đã chạy thật (không chỉ compile) lúc viết — xem kết quả ở bảng trên.

## CI

`.github/workflows/disruptive.yml` — `workflow_dispatch` + cron hàng tuần (Thứ 2, 06:00 UTC, sau
`performance.yml`/`pentest.yml` cùng ngày để tránh tranh tài nguyên). Dùng `docker compose up -d`
làm bước riêng (không dùng GH Actions' `services:` block) — 3 test chaos cần tên container thật
(`metap-postgres-1`/`metap-rabbitmq-1`) để `docker stop/start/pause` nhắm đúng target.

## Chưa làm (biết trước, không phải thiếu sót ẩn)

- Chỉ 3 kịch bản — chưa cover: kill app process giữa transaction (Postgres tự rollback, ít giá trị
  test thêm), network partition thật (khác hẳn stop/pause — TCP timeout khác connection-refused),
  disk-full, CPU/memory pressure.
- Kịch bản #3 mới **ghi nhận** hành vi hiện tại (fail sau ~30s), không assert 1 SLA cụ thể đã được
  xây — nếu sau này `Router` có retry/circuit-breaker thật, cập nhật assertion ở đây theo, đừng chỉ
  nới lỏng test để nó pass trở lại.
- Không đo được `notification-worker`/`cron-scheduler` mất bao nhiêu event thật trong lúc RabbitMQ
  down (chỉ chứng minh consumer tự reconnect và nhận được message publish *sau* khi sống lại) — độ
  bền của chính hàng đợi RabbitMQ (durable queue, không phải code app) là thứ đảm bảo message
  publish *trong lúc* down không mất, ngoài phạm vi 3 test này.
