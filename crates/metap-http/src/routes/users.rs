//! `GET /users` — lightweight `{id, email}` list of every user in the caller's tenant, the "pick
//! a user" primitive an assignee/reporter picker needs. Deliberately **not** under `/admin/*`
//! (`AuthContext`, not `AdminContext`) — assigning an issue to a colleague isn't an admin action,
//! unlike granting a role (`GET /admin/users`, `routes/admin.rs`, which returns role assignments,
//! a different shape for a different purpose).

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde_json::json;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, router_unavailable_response};
use crate::state::AppState;

async fn list_users(State(state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let users = match metap_peripherals::list_tenant_users(&mut *tx, tenant_id).await {
        Ok(u) => u,
        Err(e) => return internal_error_response(e),
    };
    let _ = tx.commit().await;

    let data: Vec<_> = users
        .into_iter()
        .map(|u| json!({ "id": u.id, "email": u.email }))
        .collect();
    Json(json!({ "data": data })).into_response()
}

pub fn router() -> Router<AppState> {
    Router::new().route("/users", get(list_users))
}
