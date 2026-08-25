#!/usr/bin/env bash
# Nightly criterion micro-benchmark run (`.github/workflows/nightly-benchmark.yml`) — the "chưa
# làm" item `docs/roadmap/20-backend-test-kit.md` left open. Deliberately NOT the 10M-row
# sustained-load tests `testing/performance/baseline.md` records — those need a real seeded
# Postgres (`seed_10m.sql`) and take 10 minutes of real traffic against a live DB, which doesn't
# fit a GitHub-hosted runner's resources/time budget for a nightly job. This runs the pure-CPU
# `plan_list`/`compile`+`diff` micro-benchmarks instead (`crates/metap-query/benches/
# plan_list_bench.rs`, `crates/metap-reconciler/benches/diff_bench.rs`) — no DB needed at all,
# fast enough to run every night, and still a real regression signal for the SQL-generation/
# schema-diff logic itself (as opposed to network/DB round-trip time, which the manual sustained-
# load tests already cover).
#
# criterion persists history under target/criterion/ and prints a "Performance has
# regressed/improved" line automatically when that directory already has a prior run to compare
# against — the workflow caches target/criterion/ across runs (keyed to persist indefinitely,
# not per-lockfile-hash) specifically so this comparison has something to compare against.
set -euo pipefail

echo "== plan_list (metap-query) =="
cargo bench -p metap-query --bench plan_list_bench

echo
echo "== compile/diff (metap-reconciler) =="
cargo bench -p metap-reconciler --bench diff_bench
