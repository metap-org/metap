//! Mirrors `packages/core/src/core/auth/{jwt-verifier,request-context,errors}.ts` and
//! `server/plugins/auth-hook.ts`. Role lookup delegates to
//! `metap_peripherals::get_roles_for_user` (the canonical port of
//! `role-assignment-service.ts`'s read path, Migration Order step 9) rather than
//! duplicating the query here — roles are looked up fresh from `user_roles` on every
//! request, never cached on the token, since the JWT is a bare identity assertion
//! (`docs/roadmap.md` Phase 3).

use axum::extract::{FromRef, FromRequestParts};
use axum::http::request::Parts;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::Json;
use jsonwebtoken::{decode, Algorithm, Validation};
use metap_peripherals::get_roles_for_user;
use metap_permission::RequestContext;
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::state::AppState;

#[derive(Debug, Serialize, Deserialize)]
struct Claims {
    sub: String,
    #[serde(rename = "tenantId")]
    tenant_id: String,
    #[serde(rename = "functionId")]
    function_id: Option<String>,
    exp: usize,
}

#[derive(Debug)]
pub struct AuthError(pub &'static str);

impl IntoResponse for AuthError {
    fn into_response(self) -> Response {
        (
            StatusCode::UNAUTHORIZED,
            Json(serde_json::json!({
                "error": { "code": "unauthorized", "message": self.0 }
            })),
        )
            .into_response()
    }
}

/// Wraps `metap_permission::RequestContext` — a local newtype so this crate can implement
/// `FromRequestParts` for it (the orphan rule blocks implementing a foreign trait for a
/// foreign type directly).
pub struct AuthContext(pub RequestContext);

impl<S> FromRequestParts<S> for AuthContext
where
    AppState: FromRef<S>,
    S: Send + Sync,
{
    type Rejection = AuthError;

    async fn from_request_parts(parts: &mut Parts, state: &S) -> Result<Self, Self::Rejection> {
        let app_state = AppState::from_ref(state);

        let header = parts
            .headers
            .get(axum::http::header::AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .ok_or(AuthError("Missing or invalid authorization header."))?;

        let token = header
            .strip_prefix("Bearer ")
            .ok_or(AuthError("Missing or invalid authorization header."))?;

        let validation = Validation::new(Algorithm::RS256);
        let token_data = decode::<Claims>(token, &app_state.jwt_decoding_key, &validation)
            .map_err(|_| AuthError("Invalid or expired token."))?;
        let claims = token_data.claims;

        let tenant_id = Uuid::parse_str(&claims.tenant_id)
            .map_err(|_| AuthError("Token is missing required claims."))?;
        let user_id =
            Uuid::parse_str(&claims.sub).map_err(|_| AuthError("Token is missing required claims."))?;

        let roles = get_roles_for_user(&app_state.pool, tenant_id, user_id)
            .await
            .map_err(|_| AuthError("Failed to resolve roles."))?;

        Ok(AuthContext(RequestContext {
            tenant_id: claims.tenant_id,
            user_id: Some(claims.sub),
            roles: Some(roles),
            function_id: claims.function_id,
        }))
    }
}
