#!/usr/bin/env bash
# Backup/restore drill for the dev Postgres (Phase 8 Hardening, docs/roadmap.md). Runs a real
# pg_dump against the docker-compose `postgres` service, restores it into a disposable database
# on the same container, and diffs exact per-table row counts between the live DB and the
# restored copy — proof the dump is actually usable, not just that pg_dump exited 0.
#
# Not a production backup pipeline (no off-site upload, no retention policy, no schedule) — see
# docs/roadmap.md's Phase 8 "secret manager integration" note: no production deployment
# topology is documented yet, so there's nowhere real to wire a schedule/retention policy to
# (same gap, same reason). This is the drill: prove the mechanism (pg_dump -Fc / pg_restore)
# actually works end to end against this repo's real schema, on demand, by hand — same spirit
# as smoke.sh/permission-smoke.sh/load-test.sh in this directory.
#
# Requires: docker compose postgres service up (`docker compose up -d postgres`).
# Usage: ./apps/crm-server/scripts/backup-restore-drill.sh [OUT_DIR]
#   OUT_DIR   default: <repo-root>/tmp/backup-drill (created if missing; the .dump file is left
#             there afterward — delete by hand, or copy it off-box, once there's somewhere to
#             copy it to).

set -euo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
OUT_DIR="${1:-$REPO_ROOT/tmp/backup-drill}"
mkdir -p "$OUT_DIR"

PG_USER=metap
PG_DB=metap
RESTORE_DB="metap_restore_drill_$(date +%s)"
DUMP_FILE="$OUT_DIR/metap-$(date +%Y%m%dT%H%M%S).dump"

section() { printf '\n\033[1;36m=== %s ===\033[0m\n' "$1"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '\033[31m✗ %s\033[0m\n' "$1"; exit 1; }

cd "$REPO_ROOT"

# every table across every schema, as "schema.table|exact_count" lines, sorted — used both
# before and after restore so the diff is a plain text comparison.
COUNT_SQL="SELECT format('SELECT %L::text AS tbl, count(*) AS n FROM %I.%I', schemaname||'.'||tablename, schemaname, tablename)
FROM pg_tables
WHERE schemaname NOT IN ('pg_catalog','information_schema')
ORDER BY schemaname, tablename
\\gexec"

table_counts() {
  local db="$1"
  docker compose exec -T postgres psql -U "$PG_USER" -d "$db" -Atq <<< "$COUNT_SQL" | sort
}

section "check postgres reachable"
docker compose exec -T postgres pg_isready -U "$PG_USER" > /dev/null \
  || fail "postgres not reachable via docker compose — run 'docker compose up -d postgres' first"
ok "postgres reachable"

section "row counts in $PG_DB before dump (source of truth)"
SRC_COUNTS=$(table_counts "$PG_DB")
echo "$SRC_COUNTS"

section "dump $PG_DB -> $DUMP_FILE"
docker compose exec -T postgres pg_dump -U "$PG_USER" -d "$PG_DB" -Fc > "$DUMP_FILE"
DUMP_SIZE=$(du -h "$DUMP_FILE" | cut -f1)
ok "dumped ($DUMP_SIZE)"

section "restore into disposable database $RESTORE_DB"
docker compose exec -T postgres createdb -U "$PG_USER" "$RESTORE_DB"
docker compose exec -T postgres pg_restore -U "$PG_USER" -d "$RESTORE_DB" < "$DUMP_FILE" || true
# pg_restore on a brand-new empty db can print harmless warnings (role/ownership mismatches);
# the row-count diff below is the real pass/fail signal, not pg_restore's own exit code.
ok "restore ran"

section "row counts in $RESTORE_DB after restore"
DST_COUNTS=$(table_counts "$RESTORE_DB")
echo "$DST_COUNTS"

section "diff"
if [ "$SRC_COUNTS" = "$DST_COUNTS" ]; then
  ok "row counts match exactly between $PG_DB and $RESTORE_DB — restore verified"
else
  echo "--- source ($PG_DB) ---"
  echo "$SRC_COUNTS"
  echo "--- restored ($RESTORE_DB) ---"
  echo "$DST_COUNTS"
  section "cleanup"
  docker compose exec -T postgres dropdb -U "$PG_USER" "$RESTORE_DB"
  fail "row counts differ — restore did NOT reproduce the source database"
fi

section "cleanup"
docker compose exec -T postgres dropdb -U "$PG_USER" "$RESTORE_DB"
ok "dropped $RESTORE_DB"

section "done"
echo "dump kept at $DUMP_FILE — delete by hand, or wire an off-site copy once a real production"
echo "deployment target exists (docs/roadmap.md's Phase 8 secret-manager note has the same gap)."
echo "record the outcome + date + dump size in docs/roadmap.md's Phase 8 backup/restore entry."
