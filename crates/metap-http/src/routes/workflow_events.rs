//! Generic `/api/{entity}/{id}/workflow-events` — read-only transition history for one record,
//! parameterized by `:entity` the same way `attachments.rs` is. Backs anything that needs "when
//! did this record change state" without a bespoke time-series table (first consumer:
//! `apps/jira-fe`'s sprint burndown, reconstructing remaining story points per day from
//! `to_state` transitions — see `metap-workflow::list_events`'s doc comment). Read-only, so only
//! `can_read_entity` is checked, no write path exists here.

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde_json::json;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

async fn list_workflow_events(
    State(state): State<AppState>,
    Path((entity, record_id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    let decision = match state.permissions.can_read_entity(&context, &entity).await {
        Ok(d) => d,
        Err(e) => return internal_error_response(e),
    };
    if !decision.allowed {
        return service_error_response(403, "forbidden", None, None);
    }
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let events = match metap_workflow::list_events(&mut *tx, tenant_id, &entity, record_id).await {
        Ok(e) => e,
        Err(e) => return internal_error_response(e),
    };
    let _ = tx.commit().await;

    Json(json!({ "data": events })).into_response()
}

pub fn router() -> Router<AppState> {
    Router::new().route("/api/{entity}/{record_id}/workflow-events", get(list_workflow_events))
}
