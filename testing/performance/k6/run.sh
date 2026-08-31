#!/usr/bin/env bash
# Thin orchestrator around k6 (`testing/README.md`'s Performance section) — sequences
# seed -> rate-limit-bucket wait -> each scenario via `docker compose run --rm k6`. No load
# generation or percentile math lives here — that's k6's job (`scenario.js`/`seed.js`); this is
# only credential setup (`dev-tools seed-admin`/`mint-token`, already-correct logic, not
# reimplemented here) plus docker/sleep sequencing.
#
# Default `ENTITY=crm.customers` targets the entity that lives in `../metap-demo-crm` (its own
# repo since 2026-08-31 — see `docs/roadmap/51-example-apps-repo-split.md`), not this repo — the
# server being load-tested (`BASE_URL`) is that app's. `dev-tools mint-token` below always signs
# with *this* repo's own dev keypair (no `.env`/`keys/` here to load a different one from — see
# `REPO_ROOT`/`cd` below, always this repo's root regardless of caller's `PWD`); export
# `AUTH_JWT_PRIVATE_KEY_PATH`/`AUTH_JWT_PUBLIC_KEY_PATH` pointing at `../metap-demo-crm/keys/`
# before running this script if the token needs to verify against that app's server instead.
#
# Usage: ENTITY=... SEED_TEMPLATE='...' ./testing/performance/k6/run.sh
#   (defaults below reproduce the original crm.customers scenario set)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

ENTITY="${ENTITY:-crm.customers}"
# `${VAR:-default}` can't hold this default directly — its literal `{`/`}` characters confuse
# bash's own brace matching for the expansion (verified live 2026-08-23: the closing `}` of the
# first `{i}` placeholder was parsed as closing `${SEED_TEMPLATE:-...}` itself, corrupting the
# JSON before it ever reached k6). Plain conditional assignment sidesteps that entirely.
if [ -z "${SEED_TEMPLATE:-}" ]; then
  SEED_TEMPLATE='{"code":"LOAD-{i}","name":"Load Test Customer {i}","email":"load{i}@example.com"}'
fi
BASE_URL="${BASE_URL:-http://host.docker.internal:3000}"
SEED_COUNT="${SEED_COUNT:-500}"
REQUESTS="${REQUESTS:-250}"
CONCURRENCY="${CONCURRENCY:-20}"
TENANT_ID="${TENANT_ID:-00000000-0000-0000-0000-000000000001}"
USER_ID="${USER_ID:-00000000-0000-0000-0000-000000000002}"

echo "=== seed admin + mint dev token ==="
cargo run --manifest-path crates/dev-tools/Cargo.toml --quiet -- seed-admin "$TENANT_ID" "$USER_ID" >/dev/null
TOKEN=$(cargo run --manifest-path crates/dev-tools/Cargo.toml --quiet -- mint-token "$TENANT_ID" "$USER_ID" 2>/dev/null | tail -1)
echo "✓ token minted"

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
  "filter+sort:?limit=50&status=active&sort=-createdAt"
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
