#!/usr/bin/env bash
# Load test for the list/query path (Phase 8 Hardening, docs/roadmap.md). There is no separate
# "export" endpoint (Phase 4's report/export query boundary is deliberately deferred — see
# docs/roadmap.md's Phase 4 section — a report is just a second `listView` on the same GET
# /api/:entity path, e.g. accounting.journal's "ledger" view), so this exercises the one list/
# query path that both "list" and "export" actually go through: GET /api/:entity with paging,
# a filter, and a sort, all three concurrently against a seeded crm.customers dataset.
#
# Not a committed benchmark suite with pass/fail thresholds — a repeatable script to run by hand
# and read latency percentiles off of, same spirit as smoke.sh/permission-smoke.sh in this
# directory. No load-testing binary (k6/hey/wrk) is assumed to be installed; this uses plain
# curl + xargs -P for concurrency, matching what smoke.sh already assumes is available.
#
# Requires: crm-server running (pnpm dev:rs), docker compose postgres/rabbitmq up, jq installed.
# Run from the repo root, or anywhere (paths are absolute via REPO_ROOT).
#
# There is no count/total endpoint (`page` on GET /api/:entity only carries `limit`/
# `nextCursor`, see crates/metap-crud/src/result.rs's PageInfo — no row-count query is run,
# deliberately, to keep list() a single bounded query), so this can't detect "already seeded"
# and skip. Every run seeds SEED_COUNT fresh rows tagged with this run's timestamp (unique
# `code` per row) and leaves them in place — rerunning grows the table further, which is a
# reasonable enough approximation of "table keeps growing over the tenant's lifetime" for a
# manual load-test script. Rerunning against a table you want reset means restoring from a
# backup/restore drill (see backup-restore-drill.sh) or truncating `records` by hand first.
#
# Usage: ./apps/crm-server/scripts/load-test.sh [BASE_URL] [SEED_COUNT] [REQUESTS] [CONCURRENCY]
#   BASE_URL     default http://localhost:3000
#   SEED_COUNT   default 500   — fresh rows seeded this run
#   REQUESTS     default 250   — total requests fired per scenario (list / filter+sort); keep
#                                at or below the rate limiter's burst_size (300, see
#                                crates/metap-http/src/lib.rs) or every scenario will 429 no
#                                matter how long the bucket had to refill beforehand
#   CONCURRENCY  default 20    — in-flight requests at a time (xargs -P)

set -uo pipefail

BASE_URL="${1:-http://localhost:3000}"
SEED_COUNT="${2:-500}"
REQUESTS="${3:-250}"
CONCURRENCY="${4:-20}"
REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"

section() { printf '\n\033[1;36m=== %s ===\033[0m\n' "$1"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '\033[31m✗ %s\033[0m\n' "$1"; }

section "health check"
curl -sf "$BASE_URL/health" > /dev/null || { fail "server not reachable at $BASE_URL — is 'pnpm dev:rs' running?"; exit 1; }
ok "server reachable"

section "seed admin + mint dev token"
# PermissionService denies by default when an entity/action has no policy (docs/roadmap.md's
# permission-review findings, 2026-08-21) — the default dev tenant/user need the "admin" role
# (which bypasses policy checks entirely) or every request below 403s.
(cd "$REPO_ROOT" && pnpm seed:admin 00000000-0000-0000-0000-000000000001 00000000-0000-0000-0000-000000000002 >/dev/null 2>&1)
TOKEN=$(cd "$REPO_ROOT" && pnpm mint-token 2>/dev/null | tail -1)
[ -n "$TOKEN" ] && ok "token minted" || { fail "mint-token failed"; exit 1; }

RUN_TAG=$(date +%s)
section "seeding $SEED_COUNT crm.customers rows (tag LOAD-$RUN_TAG-*)"
seed_one() {
  local i="$1"
  curl -s -o /dev/null -X POST "$BASE_URL/api/crm.customers" \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"data\":{\"code\":\"LOAD-$RUN_TAG-$i\",\"name\":\"Load Test Customer $i\",\"email\":\"load$RUN_TAG-$i@example.com\"}}"
}
export -f seed_one
export BASE_URL TOKEN RUN_TAG
seq 1 "$SEED_COUNT" | xargs -P "$CONCURRENCY" -I{} bash -c 'seed_one "$@"' _ {}
ok "seed done"

# The rate limiter (Phase 8, `crates/metap-http/src/lib.rs`) is a single token bucket keyed by
# peer IP, shared across every route — burst 300, refilling 5/sec (~300/min sustained). Seeding
# above and every scenario below all come from this one IP, so back-to-back bursts drain a
# bucket the *previous* phase already spent, producing 429s that are really "single dev machine
# exceeded its own IP's shared budget," not a query-path problem. Wait for a full bucket before
# each read scenario so the numbers reported actually measure query latency, not the rate
# limiter (which is already known-good — see docs/roadmap.md's Phase 8 rate-limiting entry).
await_full_bucket() {
  local secs=65
  printf 'waiting %ss for the per-IP rate-limit bucket to refill (burst 300 @ 5/sec)...\n' "$secs"
  sleep "$secs"
}
await_full_bucket

# fire REQUESTS GETs at $1 (a full querystring, including leading '?'), CONCURRENCY at a time,
# appending "status_code time_total" per request to $2.
run_scenario() {
  local qs="$1" outfile="$2"
  : > "$outfile"
  export BASE_URL TOKEN qs outfile
  fire_one() {
    curl -s -o /dev/null -w '%{http_code} %{time_total}\n' "$BASE_URL/api/crm.customers$qs" \
      -H "Authorization: Bearer $TOKEN" >> "$outfile"
  }
  export -f fire_one
  seq 1 "$REQUESTS" | xargs -P "$CONCURRENCY" -I{} bash -c 'fire_one' _
}

# percentiles over the "time_total" column (seconds), plus non-200 count, from a results file.
report() {
  local outfile="$1"
  local n p50 p95 p99 max errors
  n=$(wc -l < "$outfile")
  errors=$(awk '$1 != "200"' "$outfile" | wc -l)
  p50=$(awk '{print $2}' "$outfile" | sort -n | awk -v n="$n" 'NR==int(n*0.50)+1{print; exit}')
  p95=$(awk '{print $2}' "$outfile" | sort -n | awk -v n="$n" 'NR==int(n*0.95)+1{print; exit}')
  p99=$(awk '{print $2}' "$outfile" | sort -n | awk -v n="$n" 'NR==int(n*0.99)+1{print; exit}')
  max=$(awk '{print $2}' "$outfile" | sort -n | tail -1)
  printf 'n=%s errors=%s p50=%ss p95=%ss p99=%ss max=%ss\n' "$n" "$errors" "$p50" "$p95" "$p99" "$max"
  [ "$errors" -eq 0 ] && ok "all $n requests returned 200" || fail "$errors/$n requests were non-200"
}

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

section "scenario: list (default page, limit=50)"
run_scenario "?limit=50" "$TMPDIR/list.txt"
report "$TMPDIR/list.txt"

await_full_bucket
section "scenario: filter + sort (status=active, sort=-createdAt)"
run_scenario "?limit=50&status=active&sort=-createdAt" "$TMPDIR/filter.txt"
report "$TMPDIR/filter.txt"

await_full_bucket
section "scenario: keyset pagination (page 2 via nextCursor)"
FIRST_PAGE=$(curl -s "$BASE_URL/api/crm.customers?limit=50" -H "Authorization: Bearer $TOKEN")
CURSOR=$(echo "$FIRST_PAGE" | jq -r '.page.nextCursor // empty')
if [ -n "$CURSOR" ]; then
  ENCODED_CURSOR=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$CURSOR" 2>/dev/null || printf '%s' "$CURSOR")
  run_scenario "?limit=50&cursor=$ENCODED_CURSOR" "$TMPDIR/cursor.txt"
  report "$TMPDIR/cursor.txt"
else
  echo "no nextCursor returned (dataset smaller than one page?) — skipping"
fi

section "done"
echo "record these numbers in docs/roadmap.md's Phase 8 load-test entry along with the date/machine."
