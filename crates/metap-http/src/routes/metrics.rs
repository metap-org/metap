//! `GET /metrics` — Prometheus text exposition format, public (no auth, same convention as
//! `/health`): per-route HTTP request count/duration/in-flight (`axum-prometheus`, via
//! `AppState.metrics_handle`) plus process CPU/RSS/fd/thread metrics
//! (`AppState.process_collector`). See `docs/local-benchmarking.md` for the Grafana dashboard
//! this feeds (opt-in, `docker compose --profile observability`).
//!
//! **Genuinely leaks more than `/health` does**, unlike what "same convention" might suggest —
//! `/health` is a bare liveness check, this exposes per-route request counts/latencies (which
//! routes exist and how heavily they're used) plus process RSS/CPU/fd counts to anyone who can
//! reach the port. Deliberate for now (matches how most Prometheus exporters ship by default —
//! access control is expected to be a network-level concern, e.g. not exposing this port
//! publicly), not an oversight, but worth knowing before assuming it's as harmless as `/health`
//! (architecture audit `../metap-docs/docs/audits/03-metap-core-architecture-audit.md` finding
//! #14, 2026-09-02).

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::Router;

use crate::state::AppState;

async fn metrics(State(state): State<AppState>) -> Response {
    // Refreshes the process-resource values read from `/proc` right before rendering, rather
    // than on a background timer — a scrape only happens every few seconds anyway (Prometheus's
    // `scrape_interval`), so there's no benefit to updating more often than that.
    state.process_collector.collect();
    state.metrics_handle.render().into_response()
}

pub fn router() -> Router<AppState> {
    Router::new().route("/metrics", get(metrics))
}
