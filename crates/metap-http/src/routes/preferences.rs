//! Self-service `GET`/`PUT` for the caller's own `metap_peripherals::preferences` row — the
//! backend half of i18n (`docs/roadmap.md` Phase 14). A separate top-level path (not
//! `/api/preferences`) so it can't collide with `routes::records`' `/api/{entity}` wildcard.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::{Json, Router};
use serde::{Deserialize, Serialize};
use serde_json::json;
use utoipa::ToSchema;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

/// Conservative allowlist rather than accepting any string — a typo'd locale would otherwise
/// silently fall back to the frontend's default with no server-side signal anything was
/// wrong. Extend as real locales are added (`packages/platform-react`'s i18n resources are
/// the source of truth for what's actually translated).
const SUPPORTED_LOCALES: [&str; 2] = ["en", "vi"];

fn user_id(context: &metap_permission::RequestContext) -> Result<Uuid, Box<Response>> {
    context
        .user_id
        .as_deref()
        .and_then(|s| Uuid::parse_str(s).ok())
        .ok_or_else(|| Box::new(internal_error_response(anyhow::anyhow!("token missing user id"))))
}

// Never actually constructed — doc-only, see `health.rs`'s comment.
#[derive(Serialize, ToSchema)]
struct PreferencesDto {
    locale: String,
}

#[derive(Serialize, ToSchema)]
struct GetPreferencesResponse {
    data: PreferencesDto,
}

#[utoipa::path(
    get,
    path = "/preferences",
    responses((status = 200, description = "OK", body = GetPreferencesResponse)),
)]
async fn get_preferences(State(state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let user_id = match user_id(&context) {
        Ok(id) => id,
        Err(resp) => return *resp,
    };
    match metap_peripherals::get_locale(&state.pool, tenant_id, user_id).await {
        Ok(locale) => Json(json!({ "data": { "locale": locale } })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize, ToSchema)]
struct UpdatePreferencesBody {
    locale: String,
}

#[utoipa::path(
    put,
    path = "/preferences",
    request_body = UpdatePreferencesBody,
    responses(
        (status = 200, description = "OK", body = GetPreferencesResponse),
        (status = 400, description = "Unsupported locale"),
    ),
)]
async fn update_preferences(
    State(state): State<AppState>,
    AuthContext(context): AuthContext,
    Json(body): Json<UpdatePreferencesBody>,
) -> Response {
    if !SUPPORTED_LOCALES.contains(&body.locale.as_str()) {
        return service_error_response(
            400,
            "validation_failed",
            Some(&format!("`locale` must be one of: {}.", SUPPORTED_LOCALES.join(", "))),
            None,
        );
    }
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let user_id = match user_id(&context) {
        Ok(id) => id,
        Err(resp) => return *resp,
    };
    match metap_peripherals::set_locale(&state.pool, tenant_id, user_id, &body.locale).await {
        Ok(()) => Json(json!({ "data": { "locale": body.locale } })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

fn build_router() -> OpenApiRouter<AppState> {
    OpenApiRouter::new().routes(routes!(get_preferences, update_preferences))
}

pub fn router() -> Router<AppState> {
    build_router().split_for_parts().0
}

pub(crate) fn openapi() -> utoipa::openapi::OpenApi {
    build_router().split_for_parts().1
}
