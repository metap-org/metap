# RUN.md — Verify cross-record permission condition (#3)

Test nhanh cho tính năng: policy record-level với dotted attribute path
(vd `"referredBy.status"`) — chỉ áp dụng cho `get`/`update`/`transition`/`delete`,
không áp dụng cho `list()`.

## 1. Chạy hạ tầng + server

```bash
docker compose up -d postgres rabbitmq
pnpm db:migrate
pnpm dev:rs   # port 3000
```

## 2. Provision tenant + admin

```bash
TENANT_ID="33333333-3333-3333-3333-333333333333"
ADMIN_ID="44444444-4444-4444-4444-444444444444"

pnpm provision:tenant "$TENANT_ID" schema admin@test.com AdminPass123!
pnpm seed:admin "$TENANT_ID" "$ADMIN_ID"

cd apps/crm-server
ADMIN_TOKEN=$(cargo run -p dev-tools --quiet -- mint-token "$TENANT_ID" "$ADMIN_ID" | tail -1)
cd ..
```

## 3. Tạo user bị giới hạn quyền + gán role

```bash
cd apps/crm-server
RESTRICTED_ID=$(cargo run -p dev-tools --quiet -- create-user "$TENANT_ID" restricted@test.com Pass123! | grep -oE '[0-9a-f-]{36}' | head -1)
cd ..

curl -s -X POST "http://localhost:3000/admin/users/$RESTRICTED_ID/roles" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"role":"restricted"}'
```

## 4. Seed policy (entity-level allow + record-level cross-record condition)

```bash
# entity-level: role "restricted" được read + update crm.customers
curl -s -X POST "http://localhost:3000/admin/policies/seed-defaults" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"entity":"crm.customers","roles":["restricted"],"actions":["read","update"]}'

# record-level: chỉ đọc/sửa được nếu referredBy (record khác) có status = active
curl -s -X POST "http://localhost:3000/admin/policies" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{
    "entity":"crm.customers","action":"read","roles":["restricted"],
    "subject":"record","effect":"allow",
    "condition":{"attribute":"referredBy.status","op":"eq","value":{"literal":"active"}}
  }'
curl -s -X POST "http://localhost:3000/admin/policies" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{
    "entity":"crm.customers","action":"update","roles":["restricted"],
    "subject":"record","effect":"allow",
    "condition":{"attribute":"referredBy.status","op":"eq","value":{"literal":"active"}}
  }'
```

## 5. Tạo record test

```bash
# A: người giới thiệu, activate để status=active
A_ID=$(curl -s -X POST "http://localhost:3000/api/crm.customers" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"REF-A","name":"A","email":"a@x.com"}}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['id'])")
curl -s -X POST "http://localhost:3000/api/crm.customers/$A_ID/transitions/activate" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" -d '{"version":1}'

# B: referredBy = A (A.status=active -> phải ĐƯỢC PHÉP)
B_ID=$(curl -s -X POST "http://localhost:3000/api/crm.customers" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d "{\"data\":{\"code\":\"REF-B\",\"name\":\"B\",\"referredBy\":\"$A_ID\"}}" | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['id'])")

# C: không có referredBy -> phải BỊ TỪ CHỐI
C_ID=$(curl -s -X POST "http://localhost:3000/api/crm.customers" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"data":{"code":"REF-C","name":"C"}}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['id'])")
```

## 6. Verify bằng token của restricted user

```bash
cd apps/crm-server
RESTRICTED_TOKEN=$(cargo run -p dev-tools --quiet -- mint-token "$TENANT_ID" "$RESTRICTED_ID" | tail -1)
cd ..

echo "GET B (kỳ vọng 200):"
curl -s -o /dev/null -w "%{http_code}\n" "http://localhost:3000/api/crm.customers/$B_ID" -H "Authorization: Bearer $RESTRICTED_TOKEN"

echo "GET C (kỳ vọng 403):"
curl -s -o /dev/null -w "%{http_code}\n" "http://localhost:3000/api/crm.customers/$C_ID" -H "Authorization: Bearer $RESTRICTED_TOKEN"

echo "PATCH B (kỳ vọng 200):"
curl -s -o /dev/null -w "%{http_code}\n" -X PATCH "http://localhost:3000/api/crm.customers/$B_ID" \
  -H "Authorization: Bearer $RESTRICTED_TOKEN" -H "Content-Type: application/json" \
  -d '{"version":1,"data":{"name":"B updated"}}'

echo "PATCH C (kỳ vọng 403):"
curl -s -o /dev/null -w "%{http_code}\n" -X PATCH "http://localhost:3000/api/crm.customers/$C_ID" \
  -H "Authorization: Bearer $RESTRICTED_TOKEN" -H "Content-Type: application/json" \
  -d '{"version":1,"data":{"name":"C updated"}}'
```

## 7. Dọn dẹp

```bash
docker compose exec postgres psql -U metap -d metap -c "
DELETE FROM records WHERE tenant_id = '$TENANT_ID';
DELETE FROM policies WHERE tenant_id = '$TENANT_ID';
DELETE FROM user_roles WHERE tenant_id = '$TENANT_ID';
DELETE FROM users WHERE tenant_id = '$TENANT_ID';
DELETE FROM control.tenants WHERE id = '$TENANT_ID';
"
```

## Test tự động (không cần chạy tay)

```bash
cargo test -p metap-permission   # dotted-path resolve, required_relation_fields
cargo test -p metap-query        # list() reject dotted attribute rõ ràng
```
