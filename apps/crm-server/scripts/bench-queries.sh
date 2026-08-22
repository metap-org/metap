#!/usr/bin/env bash
# Benchmarks the query patterns that actually matter for a list view — exact filter (indexed
# field), substring search (`pg_trgm` GIN), full-text search (`tsvector` GIN), and a plain
# write — against whatever `seed-bulk.sh` already loaded. Not a committed pass/fail suite, same
# spirit as smoke.sh/load-test.sh — run by hand, read the numbers, decide for yourself whether
# they're good enough for what you're about to build.
#
# Reports both the raw SQL plan/timing (`EXPLAIN ANALYZE`, via psql — shows exactly which index
# Postgres picked) and real end-to-end HTTP latency (via curl, against the actual running
# crm-server) for each pattern, so a slow HTTP number and a fast SQL number tell you the gap is
# somewhere other than the query itself (auth, JSON serialization, network).
#
# Requires: crm-server running (pnpm dev:rs), seed-bulk.sh already run, psql, jq.
# Usage: ./apps/crm-server/scripts/bench-queries.sh [BASE_URL] [ENTITY]
#   BASE_URL  default http://localhost:3000
#   ENTITY    default bench.issues

set -uo pipefail

BASE_URL="${1:-http://localhost:3000}"
ENTITY="${2:-bench.issues}"
REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
TENANT_ID="00000000-0000-0000-0000-000000000001"

section() { printf '\n\033[1;36m=== %s ===\033[0m\n' "$1"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '\033[31m✗ %s\033[0m\n' "$1"; exit 1; }

curl -sf "$BASE_URL/health" > /dev/null || fail "server not reachable at $BASE_URL — is 'pnpm dev:rs' running?"
TOKEN=$(cd "$REPO_ROOT" && pnpm mint-token 2>/dev/null | tail -1)
[ -n "$TOKEN" ] || fail "mint-token failed"

ROWS=$(docker exec metap-postgres-1 psql -U metap -d metap -tA -c "SELECT count(*) FROM records WHERE entity = '$ENTITY' AND tenant_id = '$TENANT_ID'")
[ "$ROWS" -gt 0 ] 2>/dev/null || fail "no $ENTITY rows found for tenant $TENANT_ID — run seed-bulk.sh first"
section "benchmarking against $ROWS rows of $ENTITY"

explain() {
  # explain SQL_CONDITION
  docker exec metap-postgres-1 psql -U metap -d metap -c \
    "EXPLAIN (ANALYZE, BUFFERS) SELECT id FROM records WHERE tenant_id = '$TENANT_ID' AND entity = '$ENTITY' AND deleted = false AND $1 ORDER BY created_at DESC LIMIT 101;" \
    | grep -E "Index|Bitmap|Seq Scan|Execution Time"
}

http_timings() {
  # http_timings QUERYSTRING — fires 5 requests, prints time_total for each
  local qs="$1"
  for _ in 1 2 3 4 5; do
    curl -s -o /dev/null -w "%{time_total}s " "$BASE_URL/api/$ENTITY?$qs" -H "Authorization: Bearer $TOKEN"
  done
  echo
}

section "1. exact filter on indexed field (status=in_progress)"
explain "jsonb_extract_path_text(data, 'status') = 'in_progress'"
printf 'HTTP (5 runs): '; http_timings "status=in_progress&limit=50"

section "2. substring search on title (pg_trgm GIN)"
explain "jsonb_extract_path_text(data, 'title') ILIKE '%widget-render-timeout%'"
printf 'HTTP (5 runs): '; http_timings "title=widget-render-timeout&limit=50"

section "3. full-text search on description (tsvector GIN)"
explain "to_tsvector('simple', jsonb_extract_path_text(data, 'description')) @@ plainto_tsquery('simple', 'payment timeout')"
printf 'HTTP (5 runs): '; http_timings "description=payment%20timeout&limit=50"

section "4. write latency (POST, with 4 indexes to maintain)"
for _ in 1 2 3; do
  curl -s -o /dev/null -w "%{time_total}s " -X POST "$BASE_URL/api/$ENTITY" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d '{"data":{"title":"bench write","description":"bench","status":"todo","assignee":"user1","priority":"medium"}}'
done
echo

ok "done — see docs/local-benchmarking.md for the Grafana dashboard to watch resource usage while this runs"
