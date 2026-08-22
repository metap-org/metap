#!/usr/bin/env bash
# Bulk-seed a large synthetic dataset directly via SQL — for benchmarking at a realistic scale
# (100K-500K+ rows), which `load-test.sh`'s HTTP-based seeding (one POST per row, fine up to a
# few thousand) is far too slow for. Not a committed test suite, same spirit as smoke.sh/
# load-test.sh — a repeatable script to run by hand before `bench-queries.sh`.
#
# Publishes a Jira-issue-shaped entity (`title` substring-searchable, `description`
# FTS-searchable, `status`/`assignee` indexed) via the low-code admin API, then inserts rows
# directly into `records` with `generate_series` + `jsonb_build_object` (bypasses CrudService/
# HTTP entirely — this is a fixture-loading step, not something meant to exercise the API).
# `~2%` of rows share a distinctive title/description substring so a benchmark filter query has
# a realistic, non-trivial selectivity to measure instead of matching everything or nothing.
#
# To seed a DIFFERENT entity shape, copy this file and edit the `jsonb_build_object(...)` below
# — this script does not attempt a generic templating mechanism, that would be more machinery
# than a benchmark fixture warrants.
#
# Requires: crm-server running (pnpm dev:rs), docker compose postgres up, psql, jq, uuidgen.
# Usage: ./apps/crm-server/scripts/seed-bulk.sh [BASE_URL] [COUNT] [ENTITY]
#   BASE_URL  default http://localhost:3000
#   COUNT     default 300000
#   ENTITY    default bench.issues

set -uo pipefail

BASE_URL="${1:-http://localhost:3000}"
COUNT="${2:-300000}"
ENTITY="${3:-bench.issues}"
REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
DATABASE_URL="${DATABASE_URL:-postgres://metap:metap@localhost:5433/metap}"
# Same fixed dev tenant/user `smoke.sh`/`load-test.sh` use — lets `pnpm mint-token` (no args)
# read the seeded data back immediately, no extra bookkeeping to pass a tenant id around.
TENANT_ID="00000000-0000-0000-0000-000000000001"
ADMIN_ID="00000000-0000-0000-0000-000000000002"

section() { printf '\n\033[1;36m=== %s ===\033[0m\n' "$1"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '\033[31m✗ %s\033[0m\n' "$1"; exit 1; }

section "health check"
curl -sf "$BASE_URL/health" > /dev/null || fail "server not reachable at $BASE_URL — is 'pnpm dev:rs' running?"
ok "server reachable"

section "seed admin + mint dev token"
(cd "$REPO_ROOT" && pnpm seed:admin "$TENANT_ID" "$ADMIN_ID" >/dev/null 2>&1)
TOKEN=$(cd "$REPO_ROOT" && pnpm mint-token 2>/dev/null | tail -1)
[ -n "$TOKEN" ] && ok "token minted" || fail "mint-token failed"

section "publish $ENTITY (idempotent — draft+publish again is harmless)"
curl -sf -X PUT "$BASE_URL/admin/lowcode/entities/$ENTITY/draft" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "label": "Issue",
    "fields": [
      {"name": "title", "label": "Title", "kind": "string", "searchable": true},
      {"name": "description", "label": "Description", "kind": "string", "searchable": true, "searchMode": "fts"},
      {"name": "status", "label": "Status", "kind": "enum", "enumValues": ["todo","in_progress","done"], "indexed": true, "sortable": true},
      {"name": "assignee", "label": "Assignee", "kind": "string", "indexed": true},
      {"name": "priority", "label": "Priority", "kind": "enum", "enumValues": ["low","medium","high","urgent"]}
    ],
    "listViews": [
      {"name": "default", "label": "Default", "fields": ["title","status","assignee"], "filters": ["status","assignee","title","description"], "defaultSort": "-createdAt", "maxLimit": 100}
    ]
  }' > /dev/null || fail "draft failed"
curl -sf -X POST "$BASE_URL/admin/lowcode/entities/$ENTITY/publish" -H "Authorization: Bearer $TOKEN" > /dev/null || fail "publish failed"
ok "$ENTITY published (indexes reconciled — includes the pg_trgm trigram index for title)"

section "seeding $COUNT rows into $ENTITY (tenant $TENANT_ID)"
docker exec -i metap-postgres-1 psql -U metap -d metap <<SQL
INSERT INTO records (id, tenant_id, entity, data, version, created_at, updated_at, deleted)
SELECT
  gen_random_uuid(),
  '$TENANT_ID',
  '$ENTITY',
  jsonb_build_object(
    'title', 'Fix login bug in module ' || (g % 500)::text || CASE WHEN g % 50 = 0 THEN ' widget-render-timeout' ELSE '' END,
    'description', CASE WHEN (g % 50 = 0) THEN 'Investigation notes: the payment gateway timeout occurs under load when the widget renders concurrently with checkout.'
                        ELSE 'Routine ticket notes covering general maintenance work unrelated to any known incident right now.' END,
    'status', (ARRAY['todo','in_progress','done'])[1 + (g % 3)],
    'assignee', 'user' || (g % 50)::text,
    'priority', (ARRAY['low','medium','high','urgent'])[1 + (g % 4)]
  ),
  1,
  now() - (g || ' seconds')::interval,
  now() - (g || ' seconds')::interval,
  false
FROM generate_series(1, $COUNT) AS g;
SQL
ok "$COUNT rows inserted"

section "VACUUM ANALYZE (planner statistics)"
docker exec metap-postgres-1 psql -U metap -d metap -c "SET max_parallel_maintenance_workers = 0;" -c "VACUUM ANALYZE records;" > /dev/null
ok "vacuumed and analyzed"

TOTAL=$(docker exec metap-postgres-1 psql -U metap -d metap -tA -c "SELECT count(*) FROM records WHERE entity = '$ENTITY' AND tenant_id = '$TENANT_ID'")
section "done — $TOTAL total $ENTITY rows for tenant $TENANT_ID"
printf 'Run ./apps/crm-server/scripts/bench-queries.sh next.\n'
printf 'To clean up: docker exec metap-postgres-1 psql -U metap -d metap -c "DELETE FROM records WHERE entity = '"'"'%s'"'"'"\n' "$ENTITY"
