//! Public `POST /auth/login` — the first route in this crate that mints a JWT instead of only
//! verifying one. See `metap_peripherals::auth`'s doc comment for why the encoding logic lives
//! there (shared with `dev-tools mint-token`), not here.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::json;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

const TOKEN_TTL_SECONDS: u64 = 3600;

#[derive(Deserialize)]
struct LoginBody {
    email: String,
    password: String,
    /// Optional (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20) — when the caller knows
    /// which tenant it's logging into, this routes credential verification through
    /// `Router::begin(tenantId)`, required for a `DedicatedDb`-strategy tenant whose `users`
    /// table lives only in that tenant's own database, never in the shared control-plane pool
    /// the omitted-field path below still checks by email alone. Omitting it keeps today's
    /// behavior unchanged (global-by-email lookup against the shared pool) — the right default
    /// for `Schema`-strategy tenants, which currently all share one physical `public` schema
    /// anyway (`docs/roadmap.md` Phase 16: "schema/trial vẫn ghim public, chưa có isolation
    /// thật"), so email is already the only practical lookup key for them.
    #[serde(rename = "tenantId", default)]
    tenant_id: Option<Uuid>,
}

async fn login(State(state): State<AppState>, Json(body): Json<LoginBody>) -> Response {
    let verify_result = match body.tenant_id {
        Some(tenant_id) => {
            let mut tx = match state.router.begin(tenant_id.into()).await {
                Ok(tx) => tx,
                Err(e) => return router_unavailable_response(e),
            };
            let result = metap_peripherals::verify_credentials(&mut *tx, &body.email, &body.password).await;
            if result.is_ok() {
                if let Err(e) = tx.commit().await {
                    return internal_error_response(e.into());
                }
            }
            result
        }
        None => metap_peripherals::verify_credentials(&state.pool, &body.email, &body.password).await,
    };

    let user = match verify_result {
        Ok(Some(user)) => user,
        Ok(None) => {
            return service_error_response(401, "invalid_credentials", Some("Invalid email or password."), None)
        }
        Err(e) => return internal_error_response(e),
    };

    match metap_peripherals::mint_jwt(&state.jwt_encoding_key_pem, user.tenant_id, user.id, TOKEN_TTL_SECONDS) {
        Ok(token) => Json(json!({ "data": { "token": token } })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

/// Identity + roles for the caller's own token — the frontend's only way to know "am I an
/// admin" for UI gating, since roles are deliberately never encoded on the JWT itself (see
/// `crate::auth`'s doc comment): they're looked up fresh here the same way every other
/// `AuthContext` route does.
async fn me(State(_state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    Json(json!({
        "data": {
            "userId": context.user_id,
            "tenantId": context.tenant_id,
            "roles": context.roles.unwrap_or_default(),
        }
    }))
    .into_response()
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/auth/login", post(login))
        .route("/auth/me", get(me))
}
