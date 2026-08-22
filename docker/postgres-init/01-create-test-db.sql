CREATE DATABASE metap_test;

-- pg_stat_statements (docs/local-benchmarking.md) — per-query stats for the `observability`
-- profile's Grafana dashboard. `shared_preload_libraries` is set via docker-compose.yml's
-- `command:` on the postgres service; this just exposes the view in both databases.
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
\c metap_test
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
