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
use metap_peripherals::{get_roles_for_user, JWT_AUDIENCE, JWT_ISSUER};
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
    iss: String,
    aud: String,
}

#[derive(Debug)]
pub struct AuthError {
    message: &'static str,
    status: StatusCode,
}

impl AuthError {
    fn unauthorized(message: &'static str) -> Self {
        Self {
            message,
            status: StatusCode::UNAUTHORIZED,
        }
    }

    fn forbidden(message: &'static str) -> Self {
        Self {
            message,
            status: StatusCode::FORBIDDEN,
        }
    }
}

impl IntoResponse for AuthError {
    fn into_response(self) -> Response {
        let code = if self.status == StatusCode::FORBIDDEN {
            "forbidden"
        } else {
            "unauthorized"
        };
        (
            self.status,
            Json(serde_json::json!({
                "error": { "code": code, "message": self.message }
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
            .ok_or(AuthError::unauthorized("Missing or invalid authorization header."))?;

        let token = header
            .strip_prefix("Bearer ")
            .ok_or(AuthError::unauthorized("Missing or invalid authorization header."))?;

        let mut validation = Validation::new(Algorithm::RS256);
        validation.set_audience(&[JWT_AUDIENCE]);
        validation.set_issuer(&[JWT_ISSUER]);
        let token_data = decode::<Claims>(token, &app_state.jwt_decoding_key, &validation)
            .map_err(|_| AuthError::unauthorized("Invalid or expired token."))?;
        let claims = token_data.claims;

        let tenant_id = Uuid::parse_str(&claims.tenant_id)
            .map_err(|_| AuthError::unauthorized("Token is missing required claims."))?;
        let user_id =
            Uuid::parse_str(&claims.sub).map_err(|_| AuthError::unauthorized("Token is missing required claims."))?;

        // Routed through `Router` (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20), not
        // `app_state.pool` directly — a `DedicatedDb`-strategy tenant's `user_roles` table
        // lives only in that tenant's own database. `PLATFORM_TENANT_ID` (the sentinel
        // `PlatformAdminContext` checks for) is never a real `control.tenants` row by design,
        // so `Router::begin` takes its documented unregistered-tenant fallback
        // (`{Active, Schema("public")}`) for it — exactly where `users`/`user_roles` for that
        // sentinel actually live, so no special-casing is needed here.
        let mut tx = app_state
            .router
            .begin(tenant_id.into())
            .await
            .map_err(|_| AuthError::unauthorized("Failed to resolve roles."))?;
        let roles = get_roles_for_user(&mut *tx, tenant_id, user_id)
            .await
            .map_err(|_| AuthError::unauthorized("Failed to resolve roles."))?;
        tx.commit()
            .await
            .map_err(|_| AuthError::unauthorized("Failed to resolve roles."))?;

        Ok(AuthContext(RequestContext {
            tenant_id: claims.tenant_id,
            user_id: Some(claims.sub),
            roles: Some(roles),
            function_id: claims.function_id,
        }))
    }
}

/// Same identity/tenant resolution as `AuthContext`, plus an `admin` role check — the
/// extractor every `/admin/*` route uses instead of `AuthContext` so the gate can't be
/// forgotten on a future handler the way a per-handler `if !context.is_admin()` check could
/// be.
pub struct AdminContext(pub RequestContext);

impl<S> FromRequestParts<S> for AdminContext
where
    AppState: FromRef<S>,
    S: Send + Sync,
{
    type Rejection = AuthError;

    async fn from_request_parts(parts: &mut Parts, state: &S) -> Result<Self, Self::Rejection> {
        let AuthContext(context) = AuthContext::from_request_parts(parts, state).await?;
        if !context.is_admin() {
            return Err(AuthError::forbidden("This action requires the admin role."));
        }
        Ok(AdminContext(context))
    }
}

/// Cross-tenant admin gate for the optional platform/SaaS-control-plane HTTP surface
/// (`metap-control-http`, Phase 16 Giai đoạn 3, `docs/roadmap.md`) — distinct from
/// `AdminContext`, which only authorizes actions *inside the caller's own tenant*. A request
/// passes this gate when its JWT's `tenantId` is `metap_control::PLATFORM_TENANT_ID` (a
/// sentinel, never a real tenant — see that constant's doc comment) and the resolved roles
/// include `"platform_admin"`. No new auth mechanism: same `AuthContext` resolution
/// (JWT verify + `get_roles_for_user`), just a different role/tenant check than `AdminContext`'s.
pub struct PlatformAdminContext(pub RequestContext);

impl<S> FromRequestParts<S> for PlatformAdminContext
where
    AppState: FromRef<S>,
    S: Send + Sync,
{
    type Rejection = AuthError;

    async fn from_request_parts(parts: &mut Parts, state: &S) -> Result<Self, Self::Rejection> {
        let AuthContext(context) = AuthContext::from_request_parts(parts, state).await?;
        let is_platform_tenant = context.tenant_id == metap_control::PLATFORM_TENANT_ID.to_string();
        let has_platform_admin_role = context
            .roles
            .as_ref()
            .is_some_and(|roles| roles.iter().any(|r| r == "platform_admin"));
        if !is_platform_tenant || !has_platform_admin_role {
            return Err(AuthError::forbidden("This action requires the platform_admin role."));
        }
        Ok(PlatformAdminContext(context))
    }
}
