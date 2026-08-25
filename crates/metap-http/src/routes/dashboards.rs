//! `GET/PUT /dashboards/me` (any authenticated user, their own personal layout) and
//! `GET/PUT /dashboards/tenant-default` (`AdminContext` for the write, same posture
//! `routes/admin.rs` takes) — the HTTP surface for `metap-dashboards`. Generic across every
//! app/entity: a layout is an opaque JSON blob to this crate and to `metap-dashboards` itself,
//! interpreted only by the frontend's widget catalog.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use crate::auth::{AdminContext, AuthContext};
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

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

#[derive(Deserialize)]
struct SaveLayoutBody {
    layout: Value,
}

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

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/dashboards/me", get(get_my_dashboard).put(save_my_dashboard))
        .route(
            "/dashboards/tenant-default",
            get(get_tenant_default_dashboard).put(save_tenant_default_dashboard),
        )
}
