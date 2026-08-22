# Local Benchmarking

Công cụ chạy tay để trả lời "kiến trúc hiện tại (bảng `records` chung + JSONB expression index)
có đủ nhanh cho một app thật ở quy mô thật không" — thay vì đoán. Không phải một test suite được
commit chạy trong CI (cùng tinh thần `smoke.sh`/`load-test.sh` — xem doc comment của các file đó).
Ra đời 2026-08-22 từ benchmark 500K record thủ công (`docs/roadmap.md`), giờ đóng gói lại thành
script tái sử dụng được.

## Thành phần

- **`apps/crm-server/scripts/seed-bulk.sh`** — seed một lượng record lớn (mặc định 300K) trực
  tiếp qua SQL (`generate_series`, không qua HTTP — nhanh hơn nhiều bậc so với `load-test.sh`'s
  cách seed qua POST từng row) cho một entity mô phỏng Jira Issue (`title` substring-searchable,
  `description` FTS-searchable, `status`/`assignee` indexed). Publish entity qua low-code admin
  API trước (idempotent), nên index (kể cả `pg_trgm` trigram cho `title`) được `IndexReconciler`
  build thật trước khi seed.
- **`apps/crm-server/scripts/bench-queries.sh`** — chạy 4 dạng query thực tế (filter chính xác
  trên field indexed, substring search, full-text search, ghi record mới) trên dữ liệu đã seed,
  báo cáo cả `EXPLAIN ANALYZE` (SQL plan + timing thật, thấy rõ index nào được chọn) lẫn latency
  HTTP thật (qua `crm-server` đang chạy).

Usage:

```bash
pnpm dev:rs                                  # terminal 1
./apps/crm-server/scripts/seed-bulk.sh       # terminal 2 — mặc định 300K row
./apps/crm-server/scripts/bench-queries.sh
```

Dọn dữ liệu sau khi xong (script không tự xoá, giống quy ước `load-test.sh`):

```bash
docker exec metap-postgres-1 psql -U metap -d metap -c "DELETE FROM records WHERE entity = 'bench.issues'"
```

## `crm-server` (BE) — request + process metrics

`GET /metrics` (public, không cần auth, cùng quy ước `/health`) — do `metap-http` tự expose,
không phải cấu hình thêm gì ở `crm-server`. Hai loại:

- **Request-level** (`crates/metap-http/src/metrics.rs`, `axum-prometheus`): số request/latency/
  in-flight theo từng route (`axum_http_requests_total`, `axum_http_requests_duration_seconds`
  histogram, `axum_http_requests_pending`).
- **Process-level** (`metrics-process`): CPU/RSS memory/open fd/OS thread của chính process
  `crm-server` (`process_cpu_seconds_total`, `process_resident_memory_bytes`,
  `process_open_fds`, `process_threads`).

Dashboard "Metap — crm-server Resource Metrics (benchmarking)" (cũng tự động provision) — request
rate theo endpoint, p50/p95/p99 latency, in-flight requests, CPU/RAM/fd/thread. Prometheus scrape
`host.docker.internal:3000` (`crm-server` chạy trên host qua `pnpm dev:rs`, không nằm trong
`docker-compose.yml` — cần `extra_hosts: host-gateway` để container Prometheus reach được host,
đã cấu hình sẵn).

**Chỉ `crm-server`, không phải mọi binary.** `outbox-publisher`/`notification-worker`/
`cron-scheduler` không có axum router để `axum-prometheus` instrument (chúng là poll loop, không
serve HTTP) — chưa làm, chỉ thêm khi có nhu cầu thật quan sát riêng các worker đó.

**FE (`crm-fe`) cố tình không đo** — là SPA chạy trong browser người dùng, không phải service dài
hạn có CPU/RAM để Prometheus scrape theo kiểu thông thường; đo request/latency phía client cần
real-user-monitoring (JS gửi metric ngược về), phức tạp hơn hẳn scrape thường, chưa có trigger.

## Grafana — xem tài nguyên Postgres trong lúc benchmark

Opt-in, **chỉ bật khi cần benchmark** — không phải một phần của stack dev mặc định
(`docker compose up -d postgres rabbitmq`):

```bash
docker compose --profile observability up -d
```

Lên 3 service mới: `postgres-exporter` (expose `/metrics` cho Postgres, cổng 9187),
`prometheus` (scrape Postgres mỗi 5s + `crm-server` trên host qua `host.docker.internal:3000`,
cổng 9090), `grafana` (cổng **3001** — không phải 3000, để không đụng `crm-server`). 2 dashboard
tự động provision sẵn, không cần setup tay — mở `http://localhost:3001`, không cần đăng nhập
(anonymous admin — chỉ an toàn vì đây là stack local-only, không bao giờ expose ra ngoài):

- **"Metap — Postgres Resource Metrics"** — 8 panel: uptime, active connections/max connections,
  cache hit ratio, transactions/sec, row throughput/sec, kích thước DB, deadlock (đếm dồn — xem
  `docs/architectures/11-risks.md`'s hàng `IndexReconciler` deadlock, dùng panel này để phát
  hiện nếu nó xảy ra thật), temp file spill (sort/hash tràn ra đĩa — dấu hiệu `work_mem` không
  đủ cho một query cụ thể).
- **"Metap — crm-server Resource Metrics"** — 7 panel: request rate theo endpoint, in-flight
  requests, p50/p95/p99 latency, CPU/RAM/fd/thread của process (xem mục riêng bên dưới).

Tắt lại khi xong (dừng cả 3 service observability, giữ nguyên postgres/rabbitmq):

```bash
docker compose --profile observability down
```

**`pg_stat_statements` đã bật sẵn** trên service `postgres` (`docker-compose.yml`'s `command:`)
— dùng trực tiếp qua `psql` để xem query nào chậm/chạy nhiều nhất, không cần đợi có panel riêng
trong Grafana cho việc này:

```sql
SELECT query, calls, mean_exec_time, total_exec_time
FROM pg_stat_statements
ORDER BY mean_exec_time DESC LIMIT 20;
```

**Lưu ý khi bật lần đầu trên một container Postgres đã có data từ trước** (không phải
`docker compose up` lần đầu): `shared_preload_libraries` chỉ đọc lúc container khởi động, và
script init `docker/postgres-init/*.sql` chỉ chạy lúc volume trống lần đầu. Nếu Postgres đã chạy
từ trước khi đổi `docker-compose.yml`, cần `docker compose up -d postgres` (Docker tự phát hiện
`command:` đổi và recreate container — data trong volume `metap-postgres` không mất) rồi tạo
extension tay một lần: `docker exec metap-postgres-1 psql -U metap -d metap -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;"`.

## Kết quả tham chiếu (2026-08-22, 100K-500K row, debug build, một máy dev — không phải production)

Xem `docs/roadmap.md`'s ghi chú benchmark cùng ngày cho số liệu đầy đủ. Tóm tắt: mọi dạng query
thực tế (exact/substring/FTS/sort/write) dưới 50ms ở 500K row/entity — không cần table-per-entity
ở quy mô này (trigger `@10M/entity`, `docs/architectures/09-adr.md`, giữ nguyên).
