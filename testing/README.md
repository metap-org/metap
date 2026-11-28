# testing/

Tài liệu và công cụ chạy tay. Test nằm cạnh code (`*_test.go`), không nằm ở đây.

| Thứ | Ở đâu |
|---|---|
| Bảo mật: tính chất nào có test nào giữ | [`security/checklist.md`](security/checklist.md) |
| Quét DAST (OWASP ZAP) | `security/zap/run.sh` |
| Tải HTTP (k6) | `performance/k6/` |

## Chạy test

```bash
go test ./...                                   # unit, không cần service
docker compose up -d postgres rabbitmq dragonfly vault
REDIS_URL=redis://127.0.0.1:6379 VAULT_ADDR=http://127.0.0.1:8200 \
VAULT_TOKEN=metap-dev-root-token RABBITMQ_URL=amqp://metap:metap@localhost:5672 \
  go test -tags integration ./...
```

Test tích hợp có `//go:build integration`, dùng `pkg/testkit` (mỗi test một database mới, mặc định
Postgres ở `localhost:5433`, đổi bằng `METAP_TEST_DATABASE_URL`) và **fail, không skip**, khi thiếu
service.

CI (`.github/workflows/`): `ci.yml` chạy build, vet, unit test, gofmt và lint, test tích hợp,
`govulncheck`, Semgrep; `codeql.yml` chạy CodeQL; `e2e-manual.yml` chạy tay.

## Tải và DAST

`metap` là thư viện, không có app mẫu. Cả hai công cụ chạy vào **một app đang chạy** (ví dụ `waap`),
do bạn chỉ định. **Cả hai ghi dữ liệu vào tenant được chỉ định** (k6 seed bản ghi, ZAP ở chế độ
`api` gửi payload tấn công): chỉ dùng tenant và database dựng riêng để thử, không dùng dữ liệu thật.

Token: truyền sẵn `TOKEN`, hoặc để script chạy `dev-tools mint-token`. Để app chấp nhận token đó,
đặt `JWKS_KEYS_DIR` trỏ tới thư mục khoá của app (hoặc `AUTH_JWT_PRIVATE_KEY_PATH` nếu app còn dùng
RSA).

```bash
# k6: seed rồi đo list, list có sort, trang keyset. Bộ lọc/sort phải là thứ list view của entity khai.
ENTITY=<entity> SEED_TEMPLATE='{"name":"load-{i}"}' TENANT_ID=<uuid> USER_ID=<uuid> \
  ./testing/performance/k6/run.sh

# ZAP: đọc GET /metadata/openapi.json để biết các route
TENANT_ID=<uuid> USER_ID=<uuid> ./testing/security/zap/run.sh      # quét chủ động
MODE=baseline TOKEN=<token> ./testing/security/zap/run.sh          # chỉ quét thụ động
```

k6 chạy qua Docker (`docker compose --profile observability`, kèm Prometheus và Grafana ở
`http://localhost:3001`). Dashboard tài nguyên app lọc theo job Prometheus tên `crm-server`
(`docker/prometheus/prometheus.yml`, mặc định `host.docker.internal:3000`); tên là di sản, đổi
target nếu app chạy chỗ khác. Rate limit theo IP không tắt được (mặc định burst 300, 5 request/giây),
nên script chờ 65 giây giữa các kịch bản.

## Hiện chưa có

Benchmark (`func Benchmark…`), test chaos (dừng Postgres/RabbitMQ giữa chừng), đo coverage, và
workflow tự động cho k6 hay ZAP. Các tài liệu và số đo của bản Rust nằm trong lịch sử git.
