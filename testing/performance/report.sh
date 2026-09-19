#!/usr/bin/env bash
# Turns a just-finished k6 run's Prometheus remote-write data into a human-readable report —
# thin orchestration only (curl + jq), no percentile math of its own: k6/Prometheus already
# computed everything this prints, same "no stress-test logic in a .sh file" convention
# testing/performance/k6/run.sh's own doc comment states.
#
# Reads (instant query, right after the run — k6's Prometheus output pushes running aggregates,
# so the value at "now" right after the run finishes is the final one; a range query would only
# add noise for a single completed run):
#   - k6_http_req_duration_p50/p95/p99{testrun=$RUN_TAG, scenario=<label>} — broken down per
#     scenario (`list`/`filter+sort`/`cursor`, testing/performance/k6/scenario.js's own `LABEL`
#     tag), not blended into one number: seed's POST latency and a list GET's latency answer
#     different questions, and k6/run.sh tags every request in one CI run with the same
#     `testrun`, so a single un-filtered query would silently average across all of them.
#   - process_cpu_seconds_total/process_resident_memory_bytes{job=$METRICS_JOB_NAME}
#     (crates/metap-http/src/routes/metrics.rs's `/metrics`, scraped by prometheus.yml)
#   - pg_stat_database_numbackends/pg_stat_database_xact_commit{datname="metap"}
#     (postgres-exporter, same as docker/grafana/dashboards/metap-postgres.json)
#
# Usage:
#   PROMETHEUS_URL=http://localhost:9090 RUN_TAG=1234567890 METRICS_JOB_NAME=jira-server \
#     OUTPUT_JSON=/tmp/perf-current.json PREVIOUS_JSON=/tmp/perf-previous.json \
#     ./testing/performance/report.sh
#
# PREVIOUS_JSON is optional — when given and it exists, prints a %-delta trend line per metric
# (report-only: never exits non-zero for a regression, see testing/README.md's Performance
# section and the CI workflow's own doc comment for why). Prints a Markdown report to stdout —
# the caller (.github/workflows/performance.yml) redirects that into $GITHUB_STEP_SUMMARY.
set -euo pipefail

PROMETHEUS_URL="${PROMETHEUS_URL:-http://localhost:9090}"
RUN_TAG="${RUN_TAG:?RUN_TAG is required - the same tag testing/performance/k6/run.sh tagged this run with}"
METRICS_JOB_NAME="${METRICS_JOB_NAME:?METRICS_JOB_NAME is required - see testing/apps/*.env}"
OUTPUT_JSON="${OUTPUT_JSON:?OUTPUT_JSON is required - path to write this run numbers to}"
PREVIOUS_JSON="${PREVIOUS_JSON:-}"

# testing/performance/k6/run.sh's own hardcoded scenario set — kept in sync by hand, same as
# testing/performance/scenarios.md's table. A future scenario needs a line added both places.
SCENARIOS=("list" "filter+sort" "cursor")

query() {
  # $1 = PromQL expression. Returns the scalar result, or "null" if the series doesn't exist
  # (e.g. no failed requests at all -> k6_http_req_failed_rate may be absent, not zero).
  curl -sf --get "$PROMETHEUS_URL/api/v1/query" --data-urlencode "query=$1" \
    | jq -r '.data.result[0].value[1] // "null"'
}

app_cpu_seconds=$(query "process_cpu_seconds_total{job=\"$METRICS_JOB_NAME\"}")
app_rss_bytes=$(query "process_resident_memory_bytes{job=\"$METRICS_JOB_NAME\"}")
pg_connections=$(query "pg_stat_database_numbackends{datname=\"metap\"}")
pg_commits_total=$(query "pg_stat_database_xact_commit{datname=\"metap\"}")
overall_failed_rate=$(query "k6_http_req_failed_rate{testrun=\"$RUN_TAG\"}")
overall_total_requests=$(query "sum(k6_http_reqs_total{testrun=\"$RUN_TAG\"})")

SCENARIOS_JSON="{}"
for label in "${SCENARIOS[@]}"; do
  p50=$(query "k6_http_req_duration_p50{testrun=\"$RUN_TAG\", scenario=\"$label\"}")
  p95=$(query "k6_http_req_duration_p95{testrun=\"$RUN_TAG\", scenario=\"$label\"}")
  p99=$(query "k6_http_req_duration_p99{testrun=\"$RUN_TAG\", scenario=\"$label\"}")
  requests=$(query "sum(k6_http_reqs_total{testrun=\"$RUN_TAG\", scenario=\"$label\"})")
  SCENARIOS_JSON=$(echo "$SCENARIOS_JSON" | jq \
    --arg label "$label" --arg p50 "$p50" --arg p95 "$p95" --arg p99 "$p99" --arg requests "$requests" \
    '.[$label] = {p50: ($p50|tonumber? // null), p95: ($p95|tonumber? // null),
                   p99: ($p99|tonumber? // null), requests: ($requests|tonumber? // null)}')
done

jq -n \
  --arg run_tag "$RUN_TAG" \
  --argjson scenarios "$SCENARIOS_JSON" \
  --arg overall_failed_rate "$overall_failed_rate" --arg overall_total_requests "$overall_total_requests" \
  --arg app_cpu_seconds "$app_cpu_seconds" --arg app_rss_bytes "$app_rss_bytes" \
  --arg pg_connections "$pg_connections" --arg pg_commits_total "$pg_commits_total" \
  '{run_tag: $run_tag, scenarios: $scenarios,
    overall_failed_rate: ($overall_failed_rate|tonumber? // null),
    overall_total_requests: ($overall_total_requests|tonumber? // null),
    app_cpu_seconds: ($app_cpu_seconds|tonumber? // null),
    app_rss_bytes: ($app_rss_bytes|tonumber? // null),
    pg_connections: ($pg_connections|tonumber? // null),
    pg_commits_total: ($pg_commits_total|tonumber? // null)}' \
  > "$OUTPUT_JSON"

have_previous=false
if [ -n "$PREVIOUS_JSON" ] && [ -f "$PREVIOUS_JSON" ]; then
  have_previous=true
fi

trend_for() {
  # $1 = jq path into the run JSON (e.g. '.scenarios["list"].p95'). Prints "" (no previous run,
  # or the field is null on either side) or " (Δ +12.3%)" / " (Δ -4.1%)".
  local path="$1"
  if [ "$have_previous" = false ]; then
    return
  fi
  local prev cur
  prev=$(jq -r "$path // empty" "$PREVIOUS_JSON")
  cur=$(jq -r "$path // empty" "$OUTPUT_JSON")
  if [ -z "$prev" ] || [ -z "$cur" ] || [ "$prev" = "0" ]; then
    return
  fi
  awk -v prev="$prev" -v cur="$cur" 'BEGIN { printf " (Δ %+.1f%%)", ((cur - prev) / prev) * 100 }'
}

# k6's Prometheus remote-write output reports every duration trend in seconds (Prometheus's own
# base-unit convention), not milliseconds — confirmed live (2026-09-19): a "list" scenario k6's
# own CLI output printed as p95=63.99ms came back from this exact query as 0.0634..., a ~1000x
# mismatch that silently showed as "0.06ms" instead of "63ms" before this fix. Every *stored*
# value in OUTPUT_JSON stays in raw seconds (trend_for's %-delta math is unit-independent, and
# a future reader comparing 2 JSON files by hand should see the same unit Prometheus itself
# uses) — only the human-facing table converts to ms for display.
to_ms() {
  awk -v s="$1" 'BEGIN { if (s == "n/a" || s == "") { print "n/a" } else { printf "%.1f", s * 1000 } }'
}

echo "## Performance report - run \`$RUN_TAG\`"
echo
echo "| Scenario | p50 | p95 | p99 | Requests |"
echo "|---|---|---|---|---|"
for label in "${SCENARIOS[@]}"; do
  p50=$(jq -r ".scenarios[\"$label\"].p50 // \"n/a\"" "$OUTPUT_JSON")
  p95=$(jq -r ".scenarios[\"$label\"].p95 // \"n/a\"" "$OUTPUT_JSON")
  p99=$(jq -r ".scenarios[\"$label\"].p99 // \"n/a\"" "$OUTPUT_JSON")
  requests=$(jq -r ".scenarios[\"$label\"].requests // \"n/a\"" "$OUTPUT_JSON")
  echo "| $label | $(to_ms "$p50")ms$(trend_for ".scenarios[\"$label\"].p50") | $(to_ms "$p95")ms$(trend_for ".scenarios[\"$label\"].p95") | $(to_ms "$p99")ms$(trend_for ".scenarios[\"$label\"].p99") | $requests |"
done
echo
echo "| Metric | Value |"
echo "|---|---|"
echo "| Total requests (all scenarios) | $overall_total_requests |"
echo "| Failed rate | $overall_failed_rate |"
echo "| App CPU (cumulative seconds) | ${app_cpu_seconds}$(trend_for '.app_cpu_seconds') |"
echo "| App RSS | $(awk -v b="$app_rss_bytes" 'BEGIN { printf "%.1f MB", b/1024/1024 }')$(trend_for '.app_rss_bytes') |"
echo "| Postgres active connections | $pg_connections |"
echo "| Postgres commits (cumulative) | $pg_commits_total |"
if [ "$have_previous" = false ]; then
  echo
  echo "_No previous run cached — this is the first recorded run for this target, or the cache was reset. Trend column will appear next run._"
fi
