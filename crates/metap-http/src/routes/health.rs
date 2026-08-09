//! Mirrors `packages/core/src/server/routes/health.ts`.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde_json::json;

use crate::state::AppState;

async fn health(State(state): State<AppState>) -> Response {
    let db_ok = metap_infra::health_check(&state.pool).await;
    Json(json!({
        "status": if db_ok { "ok" } else { "degraded" },
        "checks": { "database": db_ok },
    }))
    .into_response()
}

pub fn router() -> Router<AppState> {
    Router::new().route("/health", get(health))
}
