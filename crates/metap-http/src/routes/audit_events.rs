//! Generic `/api/{entity}/{id}/audit-events` — read-only create/update/delete/transition history
//! for one record, parameterized by `:entity` the same way `workflow_events.rs`/`attachments.rs`
//! are. Distinct from `/workflow-events` (state-machine transitions only, `metap-workflow`'s own
//! narrow ledger) — this is `metap-audit`'s general business audit trail, opt-in per entity via
//! `EntityDefinition.audit`. Read-only, so only `Read` permission is checked (record-level ABAC,
//! not just entity-level — `CrudService::check_record_permission`'s own doc comment explains why
//! that distinction matters for exactly this kind of attached-resource route).

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::{Json, Router};
use metap_audit::AuditTrailEntryRow;
use metap_crud::ServiceResult;
use serde::Serialize;
use serde_json::json;
use utoipa::ToSchema;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

// Never actually constructed — doc-only, see `workflow_events.rs`'s own comment on the same
// pattern. Reuses `AuditTrailEntryRow` directly: `Json(json!({"data": events}))` below
// serializes `Vec<AuditTrailEntryRow>` verbatim (camelCase, `entry.rs`'s own `#[serde(rename_all
// = "camelCase")]`), not a re-typed shape. Named `RecordAuditEventsResponse`/
// `list_record_audit_events` rather than the shorter `AuditEventsResponse`/`list_audit_events` —
// `../metap-lowcode`'s `metap-lowcode-http` already uses those exact 2 names for a different
// feature (its own entity-*definition*-change audit log, `/admin/lowcode/entities/{name}/audit`).
// The 2 backends never mount both routes in the same process today, but `utoipa` derives an
// operation id / schema name from the Rust identifier, and this crate's generated OpenAPI
// document is merged with others downstream (`../platform-ui/src/metadata/generated-types.ts`
// aggregates types across backends over time) — a distinct name avoids a real collision there.
#[derive(Serialize, ToSchema)]
struct RecordAuditEventsResponse {
    data: Vec<AuditTrailEntryRow>,
}

#[utoipa::path(
    get,
    path = "/api/{entity}/{record_id}/audit-events",
    params(
        ("entity" = String, Path, description = "Entity name"),
        ("record_id" = Uuid, Path, description = "Record id"),
    ),
    responses((status = 200, description = "OK", body = RecordAuditEventsResponse)),
)]
async fn list_record_audit_events(
    State(state): State<AppState>,
    Path((entity, record_id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    let events = match state.crud.list_audit_events(&entity, record_id, &context).await {
        Ok(ServiceResult::Ok { data, .. }) => data,
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => return service_error_response(status, &error, message.as_deref(), field_errors),
        Err(e) => return internal_error_response(e),
    };

    Json(json!({ "data": events })).into_response()
}

fn build_router() -> OpenApiRouter<AppState> {
    OpenApiRouter::new().routes(routes!(list_record_audit_events))
}

pub fn router() -> Router<AppState> {
    build_router().split_for_parts().0
}

pub(crate) fn openapi() -> utoipa::openapi::OpenApi {
    build_router().split_for_parts().1
}
