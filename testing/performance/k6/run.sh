#!/usr/bin/env bash
# Thin orchestrator around k6 (testing/README.md): seed, wait for the rate-limit bucket, then one
# `docker compose run --rm k6` per scenario. No load generation or percentile math lives here.
#
# It targets an app that is already running (BASE_URL) and WRITES SEED_COUNT records of ENTITY into
# TENANT_ID: use a tenant and database made for the test.
#
# The token comes from TOKEN, or from `dev-tools mint-token`; set JWKS_KEYS_DIR (or
# AUTH_JWT_PRIVATE_KEY_PATH) to the target app's keys so that it accepts it. Without TOKEN,
# `dev-tools seed-admin` first makes USER_ID an admin of TENANT_ID in DATABASE_URL.
#
# Usage: ENTITY=... SEED_TEMPLATE='{"name":"load-{i}"}' TENANT_ID=... USER_ID=... ./testing/performance/k6/run.sh
# FILTER_QS changes the filter/sort scenario; it must use what the entity's list view declares.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

: "${ENTITY:?ENTITY is required (the entity to seed and read)}"
# Not a ${VAR:-default}: the braces of a JSON template confuse bash's own brace matching.
: "${SEED_TEMPLATE:?SEED_TEMPLATE is required: a JSON object for one record, {i} is made unique}"
BASE_URL="${BASE_URL:-http://host.docker.internal:3000}"
SEED_COUNT="${SEED_COUNT:-500}"
REQUESTS="${REQUESTS:-250}"
CONCURRENCY="${CONCURRENCY:-20}"
FILTER_QS="${FILTER_QS:-?limit=50&sort=-createdAt}"

if [ -z "${TOKEN:-}" ]; then
  : "${TENANT_ID:?TENANT_ID is required without TOKEN}"
  : "${USER_ID:?USER_ID is required without TOKEN}"
  echo "=== seed admin + mint dev token ==="
  go run ./cmd/dev-tools seed-admin "$TENANT_ID" "$USER_ID" >/dev/null
  TOKEN=$(go run ./cmd/dev-tools mint-token "$TENANT_ID" "$USER_ID" | tail -1)
  echo "✓ token minted"
fi

COMMON_ENV=(
  -e "BASE_URL=$BASE_URL"
  -e "ENTITY=$ENTITY"
  -e "TOKEN=$TOKEN"
  -e "CONCURRENCY=$CONCURRENCY"
  -e "K6_PROMETHEUS_RW_SERVER_URL=http://prometheus:9090/api/v1/write"
  # Default trend export is p99 only — the Grafana dashboard wants p50/p95/p99 like the other
  # two resource dashboards do (verified live 2026-08-23: `p50`/`p95` without the `p(...)` form
  # is rejected outright by k6's Prometheus output, not silently ignored).
  -e "K6_PROMETHEUS_RW_TREND_STATS=p(50),p(95),p(99),min,max"
)

await_full_bucket() {
  echo "waiting 65s for the per-IP rate-limit bucket to refill (burst 300 @ 5/sec)..."
  sleep 65
}

RUN_TAG=$(date +%s)
echo "=== seeding $SEED_COUNT $ENTITY rows (run tag $RUN_TAG) ==="
docker compose --profile observability run --rm "${COMMON_ENV[@]}" \
  -e "SEED_TEMPLATE=$SEED_TEMPLATE" -e "SEED_COUNT=$SEED_COUNT" -e "RUN_TAG=$RUN_TAG" \
  k6 run -o experimental-prometheus-rw --tag testrun="$RUN_TAG" /scripts/seed.js
echo "✓ seed done"

await_full_bucket

SCENARIOS=(
  "list:?limit=50"
  "filter+sort:$FILTER_QS"
  "cursor:?limit=50"
)
for scenario in "${SCENARIOS[@]}"; do
  label="${scenario%%:*}"
  qs="${scenario#*:}"
  echo "=== scenario: $label ==="
  docker compose --profile observability run --rm "${COMMON_ENV[@]}" \
    -e "QS=$qs" -e "LABEL=$label" -e "REQUESTS=$REQUESTS" \
    k6 run -o experimental-prometheus-rw --tag testrun="$RUN_TAG" /scripts/scenario.js
  await_full_bucket
done

echo "=== done ==="
echo "Grafana: http://localhost:3001 (dashboard \"Metap — Load Test Generator (k6)\", needs docker compose --profile observability up -d)"
