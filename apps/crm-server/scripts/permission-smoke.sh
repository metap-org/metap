#!/usr/bin/env bash
# Manual smoke test for the permission model (RBAC + ABAC, crates/metap-permission).
# Sibling to smoke.sh (entity CRUD/workflow) — this one exercises admin gating, context-level
# role restriction, field-level read/write gating, and record-level row filtering, using the
# real /admin/* HTTP surface, not unit tests. Cleans up every policy/role it creates so re-runs
# don't accumulate state — designed to also run clean against a tenant with zero pre-existing
# policies (found live, 2026-08-28: a field-scoped or record-scoped policy alone is never enough
# to unblock its action — PostgresPolicyStore::find_context_policies filters `field IS NULL` for
# the base entity-level "can this actor even read/update/etc. this entity" check, so every
# section below that layers a field- or record-scoped policy on top first creates a general
# (unscoped) allow policy for that action, and tears it down again after).
#
# Requires: crm-server running (pnpm dev:rs), docker compose postgres/rabbitmq up, jq.
# Run from anywhere — paths are absolute.
#
# Usage: ./apps/crm-server/scripts/permission-smoke.sh [BASE_URL]

set -uo pipefail

BASE_URL="${1:-http://localhost:3000}"
REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
# Same default tenant dev-tools/mint-token uses; a second, otherwise-unused user id in that
# tenant — has no roles until this script assigns them, and never had any before.
TENANT_ID="00000000-0000-0000-0000-000000000001"
USER_ID="11111111-1111-1111-1111-111111111111"

section() { printf '\n\033[1;36m=== %s ===\033[0m\n' "$1"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '\033[31m✗ %s\033[0m\n' "$1"; FAILED=1; }
FAILED=0

mint() { (cd "$REPO_ROOT" && pnpm mint-token "$@" 2>/dev/null | tail -1); }

section "mint tokens"
ADMIN_TOKEN=$(mint)
USER_TOKEN=$(mint "$TENANT_ID" "$USER_ID")
[ -n "$ADMIN_TOKEN" ] && [ -n "$USER_TOKEN" ] && ok "minted admin + plain-user tokens" || { fail "mint-token failed"; exit 1; }

echo "confirm the plain user really has no roles yet:"
curl -s "$BASE_URL/auth/me" -H "Authorization: Bearer $USER_TOKEN" | jq -c .data

admin_api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /tmp/resp_body -w '%{http_code}' -X "$method" "$BASE_URL$path" \
      -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" -d "$body"
  else
    curl -s -o /tmp/resp_body -w '%{http_code}' -X "$method" "$BASE_URL$path" -H "Authorization: Bearer $ADMIN_TOKEN"
  fi
}
user_api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /tmp/resp_body -w '%{http_code}' -X "$method" "$BASE_URL$path" \
      -H "Authorization: Bearer $USER_TOKEN" -H "Content-Type: application/json" -d "$body"
  else
    curl -s -o /tmp/resp_body -w '%{http_code}' -X "$method" "$BASE_URL$path" -H "Authorization: Bearer $USER_TOKEN"
  fi
}
noauth_api() {
  curl -s -o /tmp/resp_body -w '%{http_code}' "$BASE_URL$1"
}
expect_status() {
  local got="$1" want="$2" label="$3"
  if [ "$got" = "$want" ]; then ok "$label ($got)"; else fail "$label — expected $want, got $got: $(cat /tmp/resp_body)"; fi
}

# ---------------------------------------------------------------------------
section "1. Admin route gating (crates/metap-http/src/auth.rs AdminContext)"
# ---------------------------------------------------------------------------
STATUS=$(noauth_api /admin/users); expect_status "$STATUS" 401 "no token -> 401"
STATUS=$(user_api GET /admin/users); expect_status "$STATUS" 403 "non-admin token -> 403"
STATUS=$(admin_api GET /admin/users); expect_status "$STATUS" 200 "admin token -> 200"

# ---------------------------------------------------------------------------
section "2. Context-level policy: restrict sales.orders/transition to role 'sales_manager'"
# ---------------------------------------------------------------------------
# Gates workflow transitions specifically (crates/metap-permission's EntityAction::Transition ->
# action "transition", checked by CrudService::transition via can_transition_entity) — not
# "update", which only gates PATCH /api/:entity/:id and never applies to a transitions/:action
# call. Using "update" here would silently no-op this whole policy: PermissionService denies by
# default when no policy matches an (entity, action) pair, so the plain user would 403 both
# before *and* after the role grant, for the wrong reason (found live, 2026-08-28).
admin_api POST /admin/policies '{"entity":"sales.orders","action":"transition","roles":["sales_manager"]}' > /dev/null
POLICY1_ID=$(jq -r '.data.id' /tmp/resp_body)
ok "created policy $POLICY1_ID"

admin_api POST /api/crm.customers '{"data":{"code":"PERM-CUST","name":"Perm Test Customer"}}' > /dev/null
CUSTOMER_ID=$(jq -r '.data.id' /tmp/resp_body)
admin_api POST /api/sales.orders "{\"data\":{\"code\":\"PERM-SO\",\"customer\":\"$CUSTOMER_ID\",\"orderDate\":\"2026-08-11\",\"totalAmount\":10}}" > /dev/null
SO_ID=$(jq -r '.data.id' /tmp/resp_body)
ok "created sales order $SO_ID as admin"

STATUS=$(user_api POST "/api/sales.orders/$SO_ID/transitions/confirm" '{"version":1}')
expect_status "$STATUS" 403 "plain user without 'sales_manager' role -> 403 on confirm"

admin_api POST "/admin/users/$USER_ID/roles" '{"role":"sales_manager"}' > /dev/null
ok "granted role 'sales_manager' to test user"

STATUS=$(user_api POST "/api/sales.orders/$SO_ID/transitions/confirm" '{"version":1}')
expect_status "$STATUS" 200 "same user, now with 'sales_manager' -> 200"

admin_api DELETE "/admin/users/$USER_ID/roles/sales_manager" > /dev/null
admin_api DELETE "/admin/policies/$POLICY1_ID" > /dev/null
ok "cleaned up: revoked role, deleted policy"

# ---------------------------------------------------------------------------
section "3. Field-level read policy: mask crm.customers.email unless role 'hr'"
# ---------------------------------------------------------------------------
# A field-scoped policy (field != null) never counts toward the base "can this actor read this
# entity at all" decision — PostgresPolicyStore::find_context_policies filters `field IS NULL`
# for that check by design, masking is a separate, later pass over an already-allowed read. So a
# plain user needs a general read-allow policy first, or the base read 403s outright before
# masking ever gets a chance to apply (found live, 2026-08-28 — this section previously had no
# such policy and always 403'd, for both the masked and unmasked assertions).
admin_api POST /admin/policies '{"entity":"crm.customers","action":"read"}' > /dev/null
POLICY_BASE_READ_ID=$(jq -r '.data.id' /tmp/resp_body)

admin_api POST /admin/policies '{"entity":"crm.customers","action":"read","field":"email","roles":["hr"]}' > /dev/null
POLICY2_ID=$(jq -r '.data.id' /tmp/resp_body)

admin_api POST /api/crm.customers '{"data":{"code":"PERM-CUST-2","name":"Field Mask Test","email":"secret@example.com"}}' > /dev/null
CUSTOMER2_ID=$(jq -r '.data.id' /tmp/resp_body)

user_api GET "/api/crm.customers/$CUSTOMER2_ID" > /dev/null
HAS_EMAIL=$(jq -r '.data.data | has("email")' /tmp/resp_body)
[ "$HAS_EMAIL" = "false" ] && ok "email masked for user without 'hr' role" || fail "email NOT masked: $(cat /tmp/resp_body)"

admin_api POST "/admin/users/$USER_ID/roles" '{"role":"hr"}' > /dev/null
user_api GET "/api/crm.customers/$CUSTOMER2_ID" > /dev/null
HAS_EMAIL=$(jq -r '.data.data | has("email")' /tmp/resp_body)
[ "$HAS_EMAIL" = "true" ] && ok "email visible once user has 'hr' role" || fail "email still masked: $(cat /tmp/resp_body)"

admin_api DELETE "/admin/policies/$POLICY2_ID" > /dev/null
admin_api DELETE "/admin/policies/$POLICY_BASE_READ_ID" > /dev/null
ok "cleaned up policy (role 'hr' kept for section 4)"

# ---------------------------------------------------------------------------
section "4. Field-level write policy: only role 'hr' may write crm.customers.phone"
# ---------------------------------------------------------------------------
# Same reasoning as section 3's base-read policy, but for the base "can update this entity at
# all" decision (action "update", not "write" — "write" is the field-level action name only).
admin_api POST /admin/policies '{"entity":"crm.customers","action":"update"}' > /dev/null
POLICY_BASE_UPDATE_ID=$(jq -r '.data.id' /tmp/resp_body)

admin_api POST /admin/policies '{"entity":"crm.customers","action":"write","field":"phone","roles":["hr"]}' > /dev/null
POLICY3_ID=$(jq -r '.data.id' /tmp/resp_body)

admin_api DELETE "/admin/users/$USER_ID/roles/hr" > /dev/null
STATUS=$(user_api PATCH "/api/crm.customers/$CUSTOMER2_ID" '{"version":1,"data":{"name":"Field Mask Test","phone":"0900000000"}}')
expect_status "$STATUS" 403 "writing 'phone' without 'hr' role -> 403"

STATUS=$(user_api PATCH "/api/crm.customers/$CUSTOMER2_ID" '{"version":1,"data":{"name":"Field Mask Test Renamed"}}')
expect_status "$STATUS" 200 "same user, omitting 'phone' from payload -> 200 (other fields unaffected)"

admin_api DELETE "/admin/policies/$POLICY3_ID" > /dev/null
admin_api DELETE "/admin/policies/$POLICY_BASE_UPDATE_ID" > /dev/null
ok "cleaned up policy"

# ---------------------------------------------------------------------------
section "5. Record-level policy: only show sales.orders with status=shipped (pure condition, roles=null)"
# ---------------------------------------------------------------------------
# Same reasoning as sections 3/4's base policies: a record-scoped condition policy still needs a
# general context-level read-allow policy first, or the base read on sales.orders 403s before the
# record filter is ever evaluated.
admin_api POST /admin/policies '{"entity":"sales.orders","action":"read"}' > /dev/null
POLICY_BASE_READ2_ID=$(jq -r '.data.id' /tmp/resp_body)

admin_api POST /api/sales.orders "{\"data\":{\"code\":\"PERM-SO-SHIPPED\",\"customer\":\"$CUSTOMER_ID\",\"orderDate\":\"2026-08-11\",\"totalAmount\":10}}" > /dev/null
SHIPPED_ID=$(jq -r '.data.id' /tmp/resp_body)
admin_api POST "/api/sales.orders/$SHIPPED_ID/transitions/confirm" '{"version":1}' > /dev/null
admin_api POST "/api/sales.orders/$SHIPPED_ID/transitions/ship" '{"version":2}' > /dev/null
admin_api POST /api/sales.orders "{\"data\":{\"code\":\"PERM-SO-DRAFT\",\"customer\":\"$CUSTOMER_ID\",\"orderDate\":\"2026-08-11\",\"totalAmount\":10}}" > /dev/null
DRAFT_ID=$(jq -r '.data.id' /tmp/resp_body)
ok "created one shipped order ($SHIPPED_ID) and one left in draft ($DRAFT_ID)"

admin_api POST /admin/policies '{"entity":"sales.orders","action":"read","subject":"record","condition":{"attribute":"status","op":"eq","value":{"literal":"shipped"}}}' > /dev/null
POLICY4_ID=$(jq -r '.data.id' /tmp/resp_body)

user_api GET "/api/sales.orders?code=PERM-SO" > /dev/null
STATUSES=$(jq -c '[.data[].status]' /tmp/resp_body)
echo "visible to record-scoped user: $STATUSES"
SEES_SHIPPED=$(jq -r --arg id "$SHIPPED_ID" '[.data[].id] | index($id) != null' /tmp/resp_body)
SEES_DRAFT=$(jq -r --arg id "$DRAFT_ID" '[.data[].id] | index($id) != null' /tmp/resp_body)
if [ "$SEES_SHIPPED" = "true" ] && [ "$SEES_DRAFT" = "false" ]; then
  ok "record policy correctly shows shipped, hides draft"
else
  fail "record policy filtering wrong (sees_shipped=$SEES_SHIPPED sees_draft=$SEES_DRAFT)"
fi

admin_api DELETE "/admin/policies/$POLICY4_ID" > /dev/null
admin_api DELETE "/admin/policies/$POLICY_BASE_READ2_ID" > /dev/null
ok "cleaned up policy"

# ---------------------------------------------------------------------------
section "6. PolicyExplainer — why would a hypothetical request be allowed/denied?"
# ---------------------------------------------------------------------------
admin_api POST /admin/policies '{"entity":"sales.orders","action":"update","roles":["sales_manager"]}' > /dev/null
POLICY5_ID=$(jq -r '.data.id' /tmp/resp_body)
admin_api POST /admin/policies/explain '{"entity":"sales.orders","action":"update"}' > /dev/null
jq '.data' /tmp/resp_body
admin_api DELETE "/admin/policies/$POLICY5_ID" > /dev/null

section "done"
if [ "$FAILED" -eq 0 ]; then
  ok "all checks passed"
else
  fail "one or more checks failed — see ✗ above"
  exit 1
fi
