//! HTTP request + process resource metrics (`docs/local-benchmarking.md`) — a `GET /metrics`
//! endpoint (`crate::routes::metrics`) in the same Prometheus text format the `observability`
//! docker-compose profile's Prometheus/Grafana stack already scrapes for Postgres. Deliberately
//! scoped to `crm-server`'s HTTP surface only, not the other Rust binaries (`outbox-publisher`,
//! `notification-worker`, `cron-scheduler`): those have no axum router for `axum-prometheus` to
//! instrument, and adding a bespoke `/metrics` server to each would be new scope beyond what
//! this session's benchmark trigger asked for.

use std::sync::OnceLock;

use axum_prometheus::metrics_exporter_prometheus::PrometheusHandle;
use axum_prometheus::PrometheusMetricLayer;

/// `PrometheusMetricLayer::pair()` installs a *global* `metrics` recorder as a side effect
/// (`metrics::set_global_recorder`, inside `axum-prometheus`'s `Handle::default()`) — calling it
/// a second time in the same process panics ("Failed to set global recorder"). Every e2e test in
/// `crates/metap-http/tests/http_server.rs` builds its own `AppState` (hence its own call to
/// `AppState::new`), so without this guard the second test function to run in the same test
/// binary would crash the whole suite, not just fail one test. `OnceLock` makes the recorder
/// install happen exactly once per process; every caller after the first gets the same
/// (cheaply-`Clone`, `Arc`-backed) handle back.
fn metrics_handle_once() -> &'static PrometheusHandle {
    static HANDLE: OnceLock<PrometheusHandle> = OnceLock::new();
    HANDLE.get_or_init(|| PrometheusMetricLayer::pair().1)
}

/// Returns the process-wide metrics handle for `AppState.metrics_handle` — see
/// `metrics_handle_once`'s doc comment for why this is safe to call more than once.
pub fn prometheus_handle() -> PrometheusHandle {
    metrics_handle_once().clone()
}

/// A *fresh* tower `Layer` instance per `build_router` call — unlike the recorder/handle above,
/// constructing this doesn't touch any global state (see `axum-prometheus`'s `GenericMetricLayer::new`,
/// which only builds the tower middleware itself), so no guarding is needed here.
pub fn metric_layer<'a>() -> PrometheusMetricLayer<'a> {
    PrometheusMetricLayer::new()
}

/// `describe_*` calls into the `metrics` crate are idempotent (re-registering the same metric's
/// description just overwrites it, no panic) — unlike the recorder install above, this is safe
/// to call on every `AppState::new()`, not just the first.
pub fn process_collector() -> metrics_process::Collector {
    let collector = metrics_process::Collector::default();
    collector.describe();
    collector
}
