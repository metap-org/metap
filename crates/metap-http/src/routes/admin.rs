//! Admin-gated role/policy management over HTTP — the HTTP surface for
//! `metap_peripherals::assign_role`/`revoke_role`/`list_users` and
//! `PermissionService::list_policies`/`create_policy`/`delete_policy`/`explain`, all of
//! which previously existed only as functions with e2e coverage (see
//! `docs/architectures/11-risks.md`). Every handler here uses `AdminContext`, not
//! `AuthContext` — the `admin` role check happens once, in the extractor, so it can't be
//! forgotten on a future route added to this file.

use std::collections::HashMap;

use axum::extract::{Path, Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use metap_permission::{PolicyCondition, PolicyRow, PolicySubject};
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use crate::auth::AdminContext;
use crate::error::internal_error_response;
use crate::state::AppState;

fn policy_to_json(row: &PolicyRow) -> Value {
    json!({
        "id": row.id,
        "tenantId": row.tenant_id,
        "entity": row.entity,
        "action": row.action,
        "field": row.field,
        "subject": row.subject,
        "roles": row.roles,
        "condition": row.condition,
        "createdBy": row.created_by,
    })
}

async fn list_users(State(state): State<AppState>, AdminContext(context): AdminContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    match metap_peripherals::list_users(&state.pool, tenant_id).await {
        Ok(users) => {
            let data: Vec<Value> =
                users.into_iter().map(|u| json!({ "userId": u.user_id, "roles": u.roles })).collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct AssignRoleBody {
    role: String,
}

async fn assign_role(
    State(state): State<AppState>,
    Path(user_id): Path<Uuid>,
    AdminContext(context): AdminContext,
    Json(body): Json<AssignRoleBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let assigned_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    match metap_peripherals::assign_role(&state.pool, tenant_id, user_id, &body.role, assigned_by).await {
        Ok(()) => {
            (StatusCode::CREATED, Json(json!({ "data": { "userId": user_id, "role": body.role } })))
                .into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

async fn revoke_role(
    State(state): State<AppState>,
    Path((user_id, role)): Path<(Uuid, String)>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    match metap_peripherals::revoke_role(&state.pool, tenant_id, user_id, &role).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => internal_error_response(e),
    }
}

async fn list_policies(
    State(state): State<AppState>,
    Query(params): Query<HashMap<String, String>>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let entity = params.get("entity").map(String::as_str);
    match state.permissions.list_policies(tenant_id, entity).await {
        Ok(rows) => {
            let data: Vec<Value> = rows.iter().map(policy_to_json).collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct CreatePolicyBody {
    entity: String,
    action: String,
    roles: Option<Vec<String>>,
    condition: Option<PolicyCondition>,
    field: Option<String>,
    subject: Option<String>,
}

async fn create_policy(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<CreatePolicyBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    let subject = match body.subject.as_deref() {
        Some("record") => PolicySubject::Record,
        _ => PolicySubject::Context,
    };
    match state
        .permissions
        .create_policy(
            tenant_id,
            &body.entity,
            &body.action,
            body.roles,
            body.condition,
            created_by,
            body.field.as_deref(),
            Some(subject),
        )
        .await
    {
        Ok(row) => (StatusCode::CREATED, Json(json!({ "data": policy_to_json(&row) }))).into_response(),
        Err(e) => internal_error_response(e),
    }
}

async fn delete_policy(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    match state.permissions.delete_policy(tenant_id, id).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct ExplainBody {
    entity: String,
    action: String,
    field: Option<String>,
    record: Option<serde_json::Map<String, Value>>,
}

async fn explain_policy(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<ExplainBody>,
) -> Response {
    match state
        .permissions
        .explain(&context, &body.entity, &body.action, body.field.as_deref(), body.record.as_ref())
        .await
    {
        Ok(explanation) => Json(json!({ "data": explanation })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/admin/users", get(list_users))
        .route("/admin/users/{userId}/roles", post(assign_role))
        .route("/admin/users/{userId}/roles/{role}", axum::routing::delete(revoke_role))
        .route("/admin/policies", get(list_policies).post(create_policy))
        .route("/admin/policies/explain", post(explain_policy))
        .route("/admin/policies/{id}", axum::routing::delete(delete_policy))
}
