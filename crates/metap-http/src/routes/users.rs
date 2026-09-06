//! `GET /users` — lightweight `{id, email}` list of every user in the caller's tenant, the "pick
//! a user" primitive an assignee/reporter picker needs. Deliberately **not** under `/admin/*`
//! (`AuthContext`, not `AdminContext`) — assigning an issue to a colleague isn't an admin action,
//! unlike granting a role (`GET /admin/users`, `routes/admin.rs`, which returns role assignments,
//! a different shape for a different purpose).

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::{Json, Router};
use serde::Serialize;
use serde_json::json;
use utoipa::ToSchema;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, router_unavailable_response};
use crate::state::AppState;

// Never actually constructed — doc-only, see `health.rs`'s comment.
#[derive(Serialize, ToSchema)]
struct UserSummaryDto {
    id: Uuid,
    email: String,
}

#[derive(Serialize, ToSchema)]
struct ListUsersResponse {
    data: Vec<UserSummaryDto>,
}

// Explicit operation_id: utoipa defaults to the bare function name, which collides with
// `admin.rs`'s own `list_users` handler (`GET /admin/users`, a different shape for a different
// purpose — see this file's top doc comment) once both are in the same combined OpenApi document.
// openapi-typescript's `operations` namespace is keyed by operation_id, so the collision only
// surfaces there as a `tsc` duplicate-identifier error, not at `cargo build`/`clippy` time.
#[utoipa::path(
    get,
    path = "/users",
    operation_id = "listTenantUsers",
    responses((status = 200, description = "OK", body = ListUsersResponse)),
)]
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

fn build_router() -> OpenApiRouter<AppState> {
    OpenApiRouter::new().routes(routes!(list_users))
}

pub fn router() -> Router<AppState> {
    build_router().split_for_parts().0
}

pub(crate) fn openapi() -> utoipa::openapi::OpenApi {
    build_router().split_for_parts().1
}
