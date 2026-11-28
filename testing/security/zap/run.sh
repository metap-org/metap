#!/usr/bin/env bash
# OWASP ZAP scan of a running app built on metap, via Docker. Run by hand.
#
#   MODE=api      (default) imports GET /metadata/openapi.json (public) and attacks every entity
#                 route in it. The router is driven by metadata, so there is no fixed route list.
#                 It WRITES: use a tenant and database made for the test.
#   MODE=baseline passive spider and scan only, much faster.
#
# Neither understands multi-tenant permissions or workflows; checklist.md lists the tests that do.
#
# The token comes from TOKEN, or from `dev-tools mint-token <TENANT_ID> <USER_ID>`; set
# JWKS_KEYS_DIR (or AUTH_JWT_PRIVATE_KEY_PATH) to the target app's keys so that it accepts it.
#
# Usage:
#   TENANT_ID=<uuid> USER_ID=<uuid> ./testing/security/zap/run.sh
#   MODE=baseline TOKEN=<token> BASE_URL=http://host.docker.internal:3000 ./testing/security/zap/run.sh
#
# Reports land in testing/security/zap/reports/ (gitignored, timestamped).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

MODE="${MODE:-api}"
BASE_URL="${BASE_URL:-http://host.docker.internal:3000}"
OUT_DIR="$REPO_ROOT/testing/security/zap/reports"
mkdir -p "$OUT_DIR"

if [ -z "${TOKEN:-}" ]; then
  : "${TENANT_ID:?TENANT_ID is required without TOKEN}"
  : "${USER_ID:?USER_ID is required without TOKEN}"
  TOKEN=$(go run ./cmd/dev-tools mint-token "$TENANT_ID" "$USER_ID" | tail -1)
fi

OPENAPI_URL="${OPENAPI_URL:-$BASE_URL/metadata/openapi.json}"
STAMP=$(date +%s)
REPORT_NAME="${MODE}-${STAMP}.html"
# The same report as JSON.
JSON_REPORT_NAME="${MODE}-${STAMP}.json"

# Inject the bearer token on every request ZAP makes, via a ZAP "replacer" rule — the documented
# way to auth a ZAP scan against a JWT-protected API (zap-api-scan.py/zap-baseline.py have no
# built-in --auth-header flag). Without this, every route ZAP finds behind AuthContext 401s
# immediately and the scan finds nothing but "missing auth" noise.
AUTH_OPTS="-config replacer.full_list(0).description=auth \
  -config replacer.full_list(0).enabled=true \
  -config replacer.full_list(0).matchtype=REQ_HEADER \
  -config replacer.full_list(0).matchstr=Authorization \
  -config replacer.full_list(0).regex=false \
  -config replacer.full_list(0).replacement=Bearer\\ $TOKEN"

# host.docker.internal only resolves inside the container with this mapping on Linux (Docker
# Desktop on Mac/Windows provides it for free) — same requirement docker-compose.yml's k6 service
# already documents via extra_hosts.
DOCKER_OPTS=(--rm --add-host=host.docker.internal:host-gateway -v "$OUT_DIR:/zap/wrk:rw" -t zaproxy/zap-stable)

case "$MODE" in
  api)
    echo "=== zap-api-scan (OpenAPI import: $OPENAPI_URL) ==="
    docker run "${DOCKER_OPTS[@]}" \
      zap-api-scan.py -t "$OPENAPI_URL" -f openapi -r "$REPORT_NAME" -J "$JSON_REPORT_NAME" -z "$AUTH_OPTS" || true
    ;;
  baseline)
    echo "=== zap-baseline (passive spider only, $BASE_URL) ==="
    docker run "${DOCKER_OPTS[@]}" \
      zap-baseline.py -t "$BASE_URL" -r "$REPORT_NAME" -J "$JSON_REPORT_NAME" -z "$AUTH_OPTS" || true
    ;;
  *)
    echo "unknown MODE=$MODE (expected api|baseline)" >&2
    exit 1
    ;;
esac

# zap-api-scan.py/zap-baseline.py exit non-zero whenever they find WARN/FAIL alerts (their
# designed behavior, not a script failure) — `|| true` above keeps this script's own exit clean
# so the report is always produced; read the report itself for pass/fail, not this script's $?.
echo "=== done — report: $OUT_DIR/$REPORT_NAME (JSON: $OUT_DIR/$JSON_REPORT_NAME) ==="
