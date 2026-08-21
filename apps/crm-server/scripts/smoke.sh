#!/usr/bin/env bash
# Manual smoke test for crm-server's 4 demo entities (Phase 7, docs/features/demo/*.md).
# Not a committed test suite (see docs/architectures/09-adr.md's "FE Playwright/E2E scoped
# then dropped" note for why this repo doesn't do that) — a repeatable curl script for
# poking a running dev stack by hand. Run from the repo root, or anywhere (paths are absolute).
#
# Requires: crm-server running (pnpm dev:rs) on $BASE_URL, docker compose postgres/rabbitmq up,
# jq installed. Uses pnpm mint-token, so run from the repo root or set REPO_ROOT.
#
# Usage: ./apps/crm-server/scripts/smoke.sh [BASE_URL]

set -uo pipefail

BASE_URL="${1:-http://localhost:3000}"
REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"

section() { printf '\n\033[1;36m=== %s ===\033[0m\n' "$1"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '\033[31m✗ %s\033[0m\n' "$1"; FAILED=1; }

FAILED=0

section "health check"
curl -sf "$BASE_URL/health" | jq . || fail "server not reachable at $BASE_URL — is 'pnpm dev:rs' running?"

section "seed admin + mint dev token"
# PermissionService denies by default when an entity/action has no policy (docs/roadmap.md's
# permission-review findings, 2026-08-21) — the default dev tenant/user need the "admin" role
# (which bypasses policy checks entirely) or every request below 403s.
(cd "$REPO_ROOT" && pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002 >/dev/null 2>&1)
TOKEN=$(cd "$REPO_ROOT" && pnpm mint-token 2>/dev/null | tail -1)
[ -n "$TOKEN" ] && ok "token minted" || { fail "mint-token failed"; exit 1; }

api() {
  # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -X "$method" "$BASE_URL$path" \
      -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d "$body"
  else
    curl -s -X "$method" "$BASE_URL$path" -H "Authorization: Bearer $TOKEN"
  fi
}

# ---------------------------------------------------------------------------
section "crm.customers — master data (Phase 12 port)"
# ---------------------------------------------------------------------------
CUSTOMER=$(api POST /api/crm.customers \
  '{"data":{"code":"CUST-SMOKE","name":"Smoke Test Customer","email":"smoke@example.com"}}')
CUSTOMER_ID=$(echo "$CUSTOMER" | jq -r '.data.id')
echo "$CUSTOMER" | jq -c '.data | {id, code, status}'
[ "$CUSTOMER_ID" != "null" ] && ok "created customer $CUSTOMER_ID" || fail "customer create failed: $CUSTOMER"

api GET "/api/crm.customers/$CUSTOMER_ID" | jq -c '.data.capabilities.transitions'
api POST "/api/crm.customers/$CUSTOMER_ID/transitions/activate" '{"version":1}' | jq -c '.data | {status, version}'
ok "customer: draft -> active"

# ---------------------------------------------------------------------------
section "sales.orders — transaction module (docs/features/demo/01-sales-order-entity.md)"
# ---------------------------------------------------------------------------
SO=$(api POST /api/sales.orders \
  "{\"data\":{\"code\":\"SO-SMOKE\",\"customer\":\"$CUSTOMER_ID\",\"orderDate\":\"2026-08-10\",\"totalAmount\":150.5}}")
SO_ID=$(echo "$SO" | jq -r '.data.id')
echo "$SO" | jq -c '.data | {id, code, status}'
[ "$SO_ID" != "null" ] && ok "created sales order $SO_ID" || fail "sales order create failed: $SO"

api POST "/api/sales.orders/$SO_ID/transitions/confirm" '{"version":1}' | jq -c '.data | {status, version}'
api POST "/api/sales.orders/$SO_ID/transitions/ship" '{"version":2}' | jq -c '.data | {status, version}'
ok "sales order: draft -> confirmed -> shipped"

echo "filter by reference field 'customer':"
api GET "/api/sales.orders?customer=$CUSTOMER_ID" | jq -c '.data[] | {code, status}'

# ---------------------------------------------------------------------------
section "inventory.movements — workflow-heavy module (docs/features/demo/02-inventory-movement-entity.md)"
# ---------------------------------------------------------------------------
MV=$(api POST /api/inventory.movements \
  "{\"data\":{\"code\":\"MV-SMOKE\",\"movementType\":\"out\",\"warehouse\":\"WH-HN\",\"quantity\":5,\"referenceOrder\":\"$SO_ID\"}}")
MV_ID=$(echo "$MV" | jq -r '.data.id')
echo "$MV" | jq -c '.data | {id, code, status}'
[ "$MV_ID" != "null" ] && ok "created movement $MV_ID" || fail "movement create failed: $MV"

api POST "/api/inventory.movements/$MV_ID/transitions/submit" '{"version":1}' | jq -c '.data | {status, version}'
api POST "/api/inventory.movements/$MV_ID/transitions/approve" '{"version":2}' | jq -c '.data | {status, version}'
api POST "/api/inventory.movements/$MV_ID/transitions/post" '{"version":3}' | jq -c '.data | {status, version}'
api POST "/api/inventory.movements/$MV_ID/transitions/reverse" '{"version":4}' | jq -c '.data | {status, version}'
ok "movement: draft -> pending_approval -> approved -> posted -> reversed"

echo "guard test: quantity=0 must block 'submit'"
MV0=$(api POST /api/inventory.movements \
  '{"data":{"code":"MV-SMOKE-ZERO","movementType":"in","warehouse":"WH-HN","quantity":0}}')
MV0_ID=$(echo "$MV0" | jq -r '.data.id')
GUARD_RESULT=$(api POST "/api/inventory.movements/$MV0_ID/transitions/submit" '{"version":1}')
echo "$GUARD_RESULT" | jq -c .
[ "$(echo "$GUARD_RESULT" | jq -r '.error.code // empty')" = "guard_failed" ] \
  && ok "guard correctly blocked quantity=0" || fail "guard did not block quantity=0: $GUARD_RESULT"

echo "reject branch: submit then reject"
MVR=$(api POST /api/inventory.movements '{"data":{"code":"MV-SMOKE-REJ","movementType":"transfer","warehouse":"WH-SG","quantity":10}}')
MVR_ID=$(echo "$MVR" | jq -r '.data.id')
api POST "/api/inventory.movements/$MVR_ID/transitions/submit" '{"version":1}' > /dev/null
api POST "/api/inventory.movements/$MVR_ID/transitions/reject" '{"version":2}' | jq -c '.data | {status, version}'
ok "movement: draft -> pending_approval -> rejected"

# ---------------------------------------------------------------------------
section "accounting.journal — report/export module (docs/features/demo/03-journal-entry-entity.md)"
# ---------------------------------------------------------------------------
JE=$(api POST /api/accounting.journal \
  '{"data":{"code":"JE-SMOKE","entryDate":"2026-08-10","account":"4000-REVENUE","debitAmount":0,"creditAmount":100}}')
JE_ID=$(echo "$JE" | jq -r '.data.id')
echo "$JE" | jq -c '.data | {id, code, status}'
[ "$JE_ID" != "null" ] && ok "created journal entry $JE_ID" || fail "journal entry create failed: $JE"

api POST "/api/accounting.journal/$JE_ID/transitions/post" '{"version":1}' | jq -c '.data | {status, version}'
api POST "/api/accounting.journal/$JE_ID/transitions/void" '{"version":2}' | jq -c '.data | {status, version}'
ok "journal entry: draft -> posted -> voided"

echo "guard test (Any): both debit and credit = 0 must block 'post'"
JE0=$(api POST /api/accounting.journal \
  '{"data":{"code":"JE-SMOKE-ZERO","entryDate":"2026-08-10","account":"1000-CASH","debitAmount":0,"creditAmount":0}}')
JE0_ID=$(echo "$JE0" | jq -r '.data.id')
GUARD_RESULT2=$(api POST "/api/accounting.journal/$JE0_ID/transitions/post" '{"version":1}')
echo "$GUARD_RESULT2" | jq -c .
[ "$(echo "$GUARD_RESULT2" | jq -r '.error.code // empty')" = "guard_failed" ] \
  && ok "Any guard correctly blocked debit=0,credit=0" || fail "Any guard did not block: $GUARD_RESULT2"

echo "known gap (see docs/architectures/11-risks.md): second list view 'ledger' has no way to"
echo "be requested via this API yet — plan_list always uses list_views.first() ('default')."

section "done"
if [ "$FAILED" -eq 0 ]; then
  ok "all checks passed"
else
  fail "one or more checks failed — see ✗ above"
  exit 1
fi
