#!/usr/bin/env bash
# OWASP ZAP scan against a running metap backend (crm-server or jira-server), via Docker.
# Manual tool only (`docker run`, no docker-compose service, no CI wiring) — this repo's ZAP
# invocation lives here as a reusable script/template, not a gate.
#
# Two modes:
#   MODE=api      (default) — imports GET /metadata/openapi.json (public, no auth — see
#                 CLAUDE.md's "Metadata types stay generated" section for why that route has no
#                 auth) and active-attacks every entity route ZAP finds there (injection payloads
#                 per field, param fuzzing, etc). Broad OWASP-Top-10-style coverage without
#                 hand-listing routes — the router is metadata-driven (`/api/:entity*`), so there
#                 is no fixed route list to hand ZAP in the first place.
#   MODE=baseline — passive spider + baseline scan only (no active attack payloads), much
#                 faster, closer to a pre-push sanity pass than a real pentest.
#
# Neither mode understands this app's actual attack surface (multi-tenant ABAC, workflow
# guards) — see testing/security/checklist.md for the hand-written tests that do. This is a
# broad, generic complementary layer, not a replacement.
#
# Usage:
#   ./testing/security/zap/run.sh                                       # crm-server, api mode
#   MODE=baseline ./testing/security/zap/run.sh                         # crm-server, baseline mode
#   APP=jira TENANT_ID=<uuid> USER_ID=<uuid> ./testing/security/zap/run.sh   # jira-server
#
# Reports land in testing/security/zap/reports/ (gitignored, timestamped, never overwritten).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$REPO_ROOT"

APP="${APP:-crm}"
MODE="${MODE:-api}"
OUT_DIR="$REPO_ROOT/testing/security/zap/reports"
mkdir -p "$OUT_DIR"

# `crm-server`/`jira-server` each moved to their own repo (2026-08-31, `../metap-demo-crm`/
# `../metap-demo-jira`) — `dev-tools mint-token` below always signs with *this* repo's own dev
# keypair by default (`REPO_ROOT`/`cd` above always puts CWD at this repo's root, which has no
# `.env`/`keys/` of its own to load a different one from). Export
# `AUTH_JWT_PRIVATE_KEY_PATH`/`AUTH_JWT_PUBLIC_KEY_PATH` pointing at the target app's own
# `keys/` before running this script so the minted token actually verifies against it.
if [ "$APP" = "jira" ]; then
  BASE_URL="${BASE_URL:-http://host.docker.internal:3100}"
  # jira-server's tenant is a real provisioned DedicatedDb tenant, not a fixed dev id — no safe
  # default to fall back to (see ../metap-demo-jira/.env's JIRA_TENANT_ID).
  : "${TENANT_ID:?jira mode needs TENANT_ID (the provisioned jira tenant, see ../metap-demo-jira/.env)}"
  : "${USER_ID:?jira mode needs USER_ID (see ../metap-demo-jira/.env)}"
  TOKEN="${TOKEN:-$(cargo run --manifest-path crates/metap-dev-tools/Cargo.toml --quiet -- mint-token "$TENANT_ID" "$USER_ID" 2>/dev/null | tail -1)}"
elif [ "$APP" = "crm" ]; then
  BASE_URL="${BASE_URL:-http://host.docker.internal:3000}"
  TENANT_ID="${TENANT_ID:-00000000-0000-0000-0000-000000000001}"
  USER_ID="${USER_ID:-00000000-0000-0000-0000-000000000002}"
  TOKEN="${TOKEN:-$(cargo run --manifest-path crates/metap-dev-tools/Cargo.toml --quiet -- mint-token "$TENANT_ID" "$USER_ID" 2>/dev/null | tail -1)}"
else
  echo "unknown APP=$APP (expected crm|jira)" >&2
  exit 1
fi
echo "✓ token minted for $APP (tenant=$TENANT_ID)"

OPENAPI_URL="${OPENAPI_URL:-$BASE_URL/metadata/openapi.json}"
STAMP=$(date +%s)
REPORT_NAME="${APP}-${MODE}-${STAMP}.html"

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
      zap-api-scan.py -t "$OPENAPI_URL" -f openapi -r "$REPORT_NAME" -z "$AUTH_OPTS" || true
    ;;
  baseline)
    echo "=== zap-baseline (passive spider only, $BASE_URL) ==="
    docker run "${DOCKER_OPTS[@]}" \
      zap-baseline.py -t "$BASE_URL" -r "$REPORT_NAME" -z "$AUTH_OPTS" || true
    ;;
  *)
    echo "unknown MODE=$MODE (expected api|baseline)" >&2
    exit 1
    ;;
esac

# zap-api-scan.py/zap-baseline.py exit non-zero whenever they find WARN/FAIL alerts (their
# designed behavior, not a script failure) — `|| true` above keeps this script's own exit clean
# so the report is always produced; read the report itself for pass/fail, not this script's $?.
echo "=== done — report: $OUT_DIR/$REPORT_NAME ==="
