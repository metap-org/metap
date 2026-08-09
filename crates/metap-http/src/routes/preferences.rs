//! Self-service `GET`/`PUT` for the caller's own `metap_peripherals::preferences` row — the
//! backend half of i18n (`docs/roadmap.md` Phase 14). A separate top-level path (not
//! `/api/preferences`) so it can't collide with `routes::records`' `/api/{entity}` wildcard.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::json;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

/// Conservative allowlist rather than accepting any string — a typo'd locale would otherwise
/// silently fall back to the frontend's default with no server-side signal anything was
/// wrong. Extend as real locales are added (`packages/platform-react`'s i18n resources are
/// the source of truth for what's actually translated).
const SUPPORTED_LOCALES: [&str; 2] = ["en", "vi"];

fn user_id(context: &metap_permission::RequestContext) -> Result<Uuid, Response> {
    context
        .user_id
        .as_deref()
        .and_then(|s| Uuid::parse_str(s).ok())
        .ok_or_else(|| internal_error_response(anyhow::anyhow!("token missing user id")))
}

async fn get_preferences(State(state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let user_id = match user_id(&context) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    match metap_peripherals::get_locale(&state.pool, tenant_id, user_id).await {
        Ok(locale) => Json(json!({ "data": { "locale": locale } })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct UpdatePreferencesBody {
    locale: String,
}

async fn update_preferences(
    State(state): State<AppState>,
    AuthContext(context): AuthContext,
    Json(body): Json<UpdatePreferencesBody>,
) -> Response {
    if !SUPPORTED_LOCALES.contains(&body.locale.as_str()) {
        return service_error_response(
            400,
            "validation_failed",
            Some(&format!(
                "`locale` must be one of: {}.",
                SUPPORTED_LOCALES.join(", ")
            )),
            None,
        );
    }
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let user_id = match user_id(&context) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    match metap_peripherals::set_locale(&state.pool, tenant_id, user_id, &body.locale).await {
        Ok(()) => Json(json!({ "data": { "locale": body.locale } })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

pub fn router() -> Router<AppState> {
    Router::new().route("/preferences", get(get_preferences).put(update_preferences))
}
