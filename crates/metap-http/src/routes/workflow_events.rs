//! Generic `/api/{entity}/{id}/workflow-events` — read-only transition history for one record,
//! parameterized by `:entity` the same way `attachments.rs` is. Backs anything that needs "when
//! did this record change state" without a bespoke time-series table (first consumer:
//! `../metap-demo-jira/web`'s sprint burndown, reconstructing remaining story points per day from
//! `to_state` transitions — see `metap-workflow::list_events`'s doc comment). Read-only, so only
//! `Read` permission is checked, no write path exists here — both entity-level and record-level
//! (ABAC), via `CrudService::check_record_permission`: this route used to check only entity-level
//! `can_read_entity`, which let a caller denied by a record-level policy on `GET
//! /api/{entity}/{id}` still read that same record's full transition history here (found in an
//! architecture audit, `../metap-docs/docs/audits/03-metap-core-architecture-audit.md` finding #1).

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use metap_crud::ServiceResult;
use metap_permission::EntityAction;
use serde_json::json;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

// NOTE (2026-09-05, `docs/features/20-record-detail-audit-trail-and-tabs.md`): `WorkflowEvent`'s
// own `#[derive(Serialize)]` emits its Rust field names verbatim
// (`record_id`/`from_state`/`to_state`/`created_at`, snake_case) — inconsistent with the rest of
// this crate's camelCase JSON surfaces (`routes/attachments.rs`'s `to_json`). **Deliberately left
// as-is, not "fixed" to camelCase**: `../metap-demo-jira/web/src/pages/SprintReportPage.tsx` is a
// real, already-shipped consumer of this exact route reading these exact snake_case keys — a
// unilateral wire-format change here would silently break it. Changing this needs a coordinated
// edit across both repos (and ideally an API-version discipline this route doesn't have), not a
// solo fix bundled into an unrelated FE feature — flagged, not fixed.

async fn list_workflow_events(
    State(state): State<AppState>,
    Path((entity, record_id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    match state
        .crud
        .check_record_permission(&entity, record_id, EntityAction::Read, &context)
        .await
    {
        Ok(ServiceResult::Ok { .. }) => {}
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => return service_error_response(status, &error, message.as_deref(), field_errors),
        Err(e) => return internal_error_response(e),
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
