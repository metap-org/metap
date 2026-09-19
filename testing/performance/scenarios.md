# Performance scenarios — automated (`.github/workflows/performance.yml`)

Sống — cập nhật mỗi khi thêm/đổi scenario. Mô tả từng bài `testing/performance/k6/run.sh` chạy
khi được gọi từ CI: mất bao lâu, môi trường lúc chạy ra sao, report sinh ra gì. Không diễn giải
lại logic k6 script (đọc `k6/scenario.js`/`k6/seed.js` là nguồn thật) — bảng này chỉ trả lời
"khi CI chạy, cái gì thực sự xảy ra".

**Không phải baseline tuyệt đối** — GitHub-hosted runner chia sẻ tài nguyên với runner khác cùng
lúc, số liệu dao động giữa các lần chạy vì lý do ngoài code (noisy neighbor). Dùng để phát hiện
**lệch tương đối** so với lần chạy trước (`report.sh`'s cột Δ), không dùng để so với
[`baseline.md`](baseline.md)'s con số đo trên máy dev/perf thật cố định phần cứng.

## App target hiện tại

`testing/apps/jira.env` (`metap-demo-jira`, `jira.projects`) — xem file đó's comment cho lý do
chọn app này làm tham chiếu đầu tiên. Thêm app khác: 1 file `testing/apps/<name>.env` mới, không
sửa workflow.

## Kịch bản

| Scenario | Script | Thời lượng ước tính | Môi trường lúc chạy | Report |
|---|---|---|---|---|
| Seed | `k6/seed.js` | ~10-30s (500 row, 20 VU song song, `shared-iterations` executor) | DB rỗng (tenant mới provision riêng cho lần chạy CI này) trước khi seed | Không tự report riêng — chỉ tạo dữ liệu cho 3 scenario dưới đọc |
| `list` | `k6/scenario.js` (`QS=?limit=50`) | ~15-20s (250 request qua rate-limit thật, không tắt được) | 500 row `jira.projects` đã seed, rate-limit bucket vừa đầy lại sau seed (`await_full_bucket`'s 65s sleep) | p50/p95/p99 + throughput qua `report.sh`, cột Δ so lần chạy CI trước cùng target |
| `filter+sort` | `k6/scenario.js` (`QS=?limit=50&status=active&sort=-createdAt`) | ~15-20s | Giống `list`, cách nhau 65s (rate-limit refill) | Giống `list` |
| `cursor` | `k6/scenario.js` (`QS=?limit=50`, keyset 2 bước) | ~15-20s | Giống `list`, cách nhau 65s | Giống `list` |

**Tổng thời gian 1 lần chạy workflow**: ~6-7 phút (3× (request+65s sleep) + seed + boot app +
provision tenant + build) — đúng lý do đây là cron hàng tuần + `workflow_dispatch`, không chạy mỗi
push/PR.

## Tài nguyên đo (exporter đã có sẵn, không cần build mới)

- **App**: `process_cpu_seconds_total`/`process_resident_memory_bytes`/`process_open_fds`/
  `process_threads` — route `/metrics` (`crates/metap-http/src/routes/metrics.rs`), tự động có ở
  mọi binary dùng `metap_http::build_router`, không cần cấu hình gì thêm cho app target mới.
- **Postgres**: `pg_stat_database_numbackends`/`pg_stat_database_xact_commit`/
  `pg_stat_database_blks_hit`/... — `postgres-exporter` (`docker-compose.yml`'s `observability`
  profile), đã scrape sẵn trong `docker/prometheus/prometheus.yml`.
- **k6 tự thân**: `k6_http_req_duration_p50/95/99`, `k6_http_reqs_total`,
  `k6_http_req_failed_rate` — remote-write thẳng vào Prometheus, xem `k6/run.sh`'s
  `K6_PROMETHEUS_RW_*` env.

CI không dựng Grafana (không cần browser để đọc report — `report.sh` curl thẳng Prometheus's
`/api/v1/query` HTTP API) — Grafana + 3 dashboard có sẵn (`docker/grafana/dashboards/*.json`) vẫn
là công cụ xem trực quan khi chạy tay ở máy dev, không phải phần CI này phụ thuộc vào.

## Chưa làm (biết trước, không phải thiếu sót ẩn)

- Chỉ 1 app target (`jira`) — thêm `waf`/khác là việc làm dần, xem `testing/apps/`'s comment.
- Trend so sánh chỉ 1 lần chạy trước (không phải rolling median nhiều lần) — đơn giản cho v1, xem
  `report.sh`'s doc comment.
- Không có alert/notify khi lệch mạnh — report-only đúng theo quyết định ban đầu, đọc thủ công
  qua Actions summary mỗi lần cron chạy.
