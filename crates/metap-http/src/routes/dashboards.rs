//! `GET/PUT /dashboards/me` (any authenticated user, their own personal layout) and
//! `GET/PUT /dashboards/tenant-default` (`AdminContext` for the write, same posture
//! `routes/admin.rs` takes) — the HTTP surface for `metap-dashboards`. Generic across every
//! app/entity: a layout is an opaque JSON blob to this crate and to `metap-dashboards` itself,
//! interpreted only by the frontend's widget catalog.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::{Json, Router};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use utoipa::ToSchema;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;
use uuid::Uuid;

use crate::auth::{AdminContext, AuthContext};
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

// Never actually constructed — doc-only, see `health.rs`'s comment. `to_json` below builds this
// exact shape by hand (not `DashboardConfig`'s own `Serialize`, which it doesn't have), so this
// DTO mirrors `to_json`'s field set rather than the domain struct's.
#[derive(Serialize, ToSchema)]
struct DashboardConfigDto {
    id: Uuid,
    #[serde(rename = "ownerUserId")]
    owner_user_id: Option<Uuid>,
    layout: Value,
    #[serde(rename = "updatedAt")]
    updated_at: DateTime<Utc>,
}

#[derive(Serialize, ToSchema)]
struct GetDashboardResponse {
    data: Option<DashboardConfigDto>,
}

#[derive(Serialize, ToSchema)]
struct SaveDashboardResponse {
    data: DashboardConfigDto,
}

fn to_json(config: &metap_dashboards::DashboardConfig) -> Value {
    json!({
        "id": config.id,
        "ownerUserId": config.owner_user_id,
        "layout": config.layout,
        "updatedAt": config.updated_at,
    })
}

fn parse_user_id(context: &metap_permission::RequestContext) -> Result<Uuid, Box<Response>> {
    context
        .user_id
        .as_deref()
        .and_then(|s| Uuid::parse_str(s).ok())
        .ok_or_else(|| Box::new(service_error_response(401, "unauthorized", None, None)))
}

#[derive(Deserialize, ToSchema)]
struct SaveLayoutBody {
    layout: Value,
}

#[utoipa::path(
    get,
    path = "/dashboards/me",
    responses((status = 200, description = "OK", body = GetDashboardResponse)),
)]
async fn get_my_dashboard(State(state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let user_id = match parse_user_id(&context) {
        Ok(id) => id,
        Err(resp) => return *resp,
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let config = match metap_dashboards::get_effective_dashboard(&mut tx, tenant_id, user_id).await {
        Ok(c) => c,
        Err(e) => return internal_error_response(e),
    };
    let _ = tx.commit().await;

    Json(json!({ "data": config.as_ref().map(to_json) })).into_response()
}

#[utoipa::path(
    put,
    path = "/dashboards/me",
    request_body = SaveLayoutBody,
    responses((status = 200, description = "OK", body = SaveDashboardResponse)),
)]
async fn save_my_dashboard(
    State(state): State<AppState>,
    AuthContext(context): AuthContext,
    Json(body): Json<SaveLayoutBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let user_id = match parse_user_id(&context) {
        Ok(id) => id,
        Err(resp) => return *resp,
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let config = match metap_dashboards::upsert_personal(&mut *tx, tenant_id, user_id, body.layout).await {
        Ok(c) => c,
        Err(e) => return internal_error_response(e),
    };
    if let Err(e) = tx.commit().await {
        return internal_error_response(e.into());
    }

    Json(json!({ "data": to_json(&config) })).into_response()
}

#[utoipa::path(
    get,
    path = "/dashboards/tenant-default",
    responses((status = 200, description = "OK", body = GetDashboardResponse)),
)]
async fn get_tenant_default_dashboard(State(state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let config = match metap_dashboards::get_tenant_default(&mut *tx, tenant_id).await {
        Ok(c) => c,
        Err(e) => return internal_error_response(e),
    };
    let _ = tx.commit().await;

    Json(json!({ "data": config.as_ref().map(to_json) })).into_response()
}

#[utoipa::path(
    put,
    path = "/dashboards/tenant-default",
    request_body = SaveLayoutBody,
    responses((status = 200, description = "OK", body = SaveDashboardResponse)),
)]
async fn save_tenant_default_dashboard(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<SaveLayoutBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let user_id = match parse_user_id(&context) {
        Ok(id) => id,
        Err(resp) => return *resp,
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let config = match metap_dashboards::upsert_tenant_default(&mut *tx, tenant_id, body.layout, user_id).await {
        Ok(c) => c,
        Err(e) => return internal_error_response(e),
    };
    if let Err(e) = tx.commit().await {
        return internal_error_response(e.into());
    }

    Json(json!({ "data": to_json(&config) })).into_response()
}

fn build_router() -> OpenApiRouter<AppState> {
    OpenApiRouter::new()
        .routes(routes!(get_my_dashboard, save_my_dashboard))
        .routes(routes!(get_tenant_default_dashboard, save_tenant_default_dashboard))
}

pub fn router() -> Router<AppState> {
    build_router().split_for_parts().0
}

pub(crate) fn openapi() -> utoipa::openapi::OpenApi {
    build_router().split_for_parts().1
}
