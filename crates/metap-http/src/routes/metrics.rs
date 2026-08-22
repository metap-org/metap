//! `GET /metrics` — Prometheus text exposition format, public (no auth, same convention as
//! `/health`): per-route HTTP request count/duration/in-flight (`axum-prometheus`, via
//! `AppState.metrics_handle`) plus process CPU/RSS/fd/thread metrics
//! (`AppState.process_collector`). See `docs/local-benchmarking.md` for the Grafana dashboard
//! this feeds (opt-in, `docker compose --profile observability`).

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
