//! Mirrors `packages/core/src/core/auth/{jwt-verifier,request-context,errors}.ts` and
//! `server/plugins/auth-hook.ts`. Role lookup delegates to
//! `metap_peripherals::get_roles_for_user` (the canonical port of
//! `role-assignment-service.ts`'s read path, Migration Order step 9) rather than
//! duplicating the query here — roles are looked up fresh from `user_roles` on every
//! request, never cached on the token, since the JWT is a bare identity assertion
//! (`docs/roadmap.md` Phase 3). `AUTH_CONTEXT_ENTITY` lookup (`context_attributes`, below)
//! delegates to `metap_peripherals::fetch_context_attributes` for the same reason — this crate
//! must not run `sqlx` queries directly (CLAUDE.md's route/handler boundary rule; a first
//! version of this file did, fixed in code review 2026-08-22).

use axum::extract::{FromRef, FromRequestParts};
use axum::http::request::Parts;
use axum::http::{Method, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::Json;
use axum_extra::extract::cookie::CookieJar;
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine as _;
use metap_auth::{AuthProviderKind, LocalPasswordProvider};
use metap_control::resolve_request_context;
use metap_peripherals::{decode_access_token, get_roles_for_user};
use metap_permission::RequestContext;
use uuid::Uuid;

use crate::cookies::{CSRF_COOKIE_NAME, CSRF_HEADER_NAME, SESSION_COOKIE_NAME};
use crate::state::AppState;

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
            .and_then(|v| v.to_str().ok());

        // An explicit `Authorization` header always wins when present — unchanged from before
        // cookie support existed, and this ordering is what keeps it safe: a header can't be
        // attached by a browser to a cross-site request the way a cookie is, so nothing here
        // needs (or runs) a CSRF check.
        let token = match header {
            Some(header) => {
                // Stateless per-request path — no JWT involved at all, tried first since it's a
                // cheap prefix check. See `basic_auth`'s doc comment for why it needs
                // `X-Tenant-Id` and why it's off by default for every tenant.
                if let Some(credentials_b64) = header.strip_prefix("Basic ") {
                    return basic_auth(&app_state, parts, credentials_b64).await;
                }
                metap_runtime::bearer::parse_bearer(header)
                    .ok_or(AuthError::unauthorized("Missing or invalid authorization header."))?
                    .to_string()
            }
            // No header at all — the shape a browser request takes, since `fetch(..., {credentials:
            // "include"})` attaches the session cookie automatically rather than through a header
            // a page's own script would have to set. See `crate::cookies`'s doc comment for the
            // two cookies this reads and why the CSRF check only applies here, never to the
            // `Authorization` branch above.
            None => {
                let jar = CookieJar::from_headers(&parts.headers);
                let session_token = jar
                    .get(SESSION_COOKIE_NAME)
                    .map(|c| c.value().to_string())
                    .ok_or(AuthError::unauthorized("Missing or invalid authorization header."))?;

                if !matches!(parts.method, Method::GET | Method::HEAD | Method::OPTIONS) {
                    let csrf_cookie = jar.get(CSRF_COOKIE_NAME).map(|c| c.value());
                    let csrf_header = parts.headers.get(CSRF_HEADER_NAME).and_then(|v| v.to_str().ok());
                    let matches = matches!((csrf_cookie, csrf_header), (Some(a), Some(b)) if a == b);
                    if !matches {
                        return Err(AuthError::unauthorized("Missing or invalid CSRF token."));
                    }
                }

                session_token
            }
        };
        let token = token.as_str();

        // Default leeway is 60s (`docs/roadmap.md`'s Phase 20 security checklist flagged this as
        // wider than needed — no revocation list exists, so exp+leeway is the only bound on how
        // long a leaked token stays usable past its stated expiry). Tightened to 20s per project
        // owner decision 2026-08-24 — still enough to forgive minor clock drift between processes.
        let claims = decode_access_token(token, &app_state.jwt_decoding_key, 20)
            .map_err(|_| AuthError::unauthorized("Invalid or expired token."))?;

        let tenant_id = Uuid::parse_str(&claims.tenant_id)
            .map_err(|_| AuthError::unauthorized("Token is missing required claims."))?;
        let user_id =
            Uuid::parse_str(&claims.sub).map_err(|_| AuthError::unauthorized("Token is missing required claims."))?;

        let context = resolve_request_context(
            &app_state.router,
            tenant_id,
            user_id,
            claims.function_id,
            app_state.auth_context_entity.as_deref(),
            &app_state.context_attributes_cache,
        )
        .await
        .map_err(|_| AuthError::unauthorized("Failed to resolve roles."))?;

        Ok(AuthContext(context))
    }
}

/// `Authorization: Basic base64(email:password)` — stateless per request, no JWT minted or
/// checked, no `context_attributes`/`function_id` (neither has an equivalent in this scheme).
/// Unlike Bearer (whose JWT claims embed `tenantId`), Basic carries no tenant information at
/// all, so the caller must supply `X-Tenant-Id` explicitly — same reasoning `POST /auth/login`'s
/// optional `tenantId` body field already documents for a `DedicatedDb` tenant whose `users`
/// table isn't reachable by email alone. Off by default for every tenant
/// (`tenant_auth_configs` has no `basic` row until an operator explicitly enables one) — enabling
/// this does not change the security surface of any tenant that hasn't opted in.
async fn basic_auth(app_state: &AppState, parts: &Parts, credentials_b64: &str) -> Result<AuthContext, AuthError> {
    let tenant_id = parts
        .headers
        .get("x-tenant-id")
        .and_then(|v| v.to_str().ok())
        .and_then(|v| Uuid::parse_str(v).ok())
        .ok_or(AuthError::unauthorized(
            "Basic auth requires a valid X-Tenant-Id header.",
        ))?;

    let decoded = BASE64
        .decode(credentials_b64)
        .ok()
        .and_then(|bytes| String::from_utf8(bytes).ok())
        .ok_or(AuthError::unauthorized("Missing or invalid authorization header."))?;
    let (email, password) = decoded
        .split_once(':')
        .ok_or(AuthError::unauthorized("Missing or invalid authorization header."))?;

    // Cached (`TenantAuthCache`) since this runs on every Basic-authed request, unlike Bearer's
    // one-off login check — see that cache's doc comment.
    let router = app_state.router.clone();
    let enabled = app_state
        .tenant_auth_cache
        .get_with(tenant_id, move || async move {
            let mut tx = router.begin(tenant_id.into()).await?;
            let kinds = metap_auth::enabled_providers(&mut *tx, tenant_id).await?;
            tx.commit().await?;
            Ok(kinds)
        })
        .await
        .map_err(|_| AuthError::unauthorized("Failed to resolve tenant auth configuration."))?;
    if !enabled.contains(&AuthProviderKind::Basic) {
        return Err(AuthError::unauthorized("Basic auth is not enabled for this tenant."));
    }

    let mut tx = app_state
        .router
        .begin(tenant_id.into())
        .await
        .map_err(|_| AuthError::unauthorized("Invalid email or password."))?;
    let user = LocalPasswordProvider
        .verify(&mut *tx, email, password)
        .await
        .map_err(|_| AuthError::unauthorized("Invalid email or password."))?
        .ok_or(AuthError::unauthorized("Invalid email or password."))?;
    // `verify_credentials`'s lookup (`WHERE email = $1`) is not itself tenant-scoped — the
    // `users` table is genuinely shared across every `Schema`-strategy tenant (all pinned to
    // `public` today, see `LoginBody::tenant_id`'s doc comment in `routes/auth.rs`), so a valid
    // password for a user of tenant A would otherwise authenticate a request that *claims* to be
    // tenant B via this header alone. `POST /auth/login` avoids this by minting the JWT for
    // `user.tenant_id` (the row actually found), never the caller-supplied value — this must
    // reject instead of silently substituting, since a caller-declared tenant that doesn't match
    // is a request Basic auth's header-only scheme can't safely serve at all.
    if user.tenant_id != tenant_id {
        return Err(AuthError::unauthorized("Invalid email or password."));
    }
    let roles = get_roles_for_user(&mut *tx, tenant_id, user.id)
        .await
        .map_err(|_| AuthError::unauthorized("Failed to resolve roles."))?;
    tx.commit()
        .await
        .map_err(|_| AuthError::unauthorized("Failed to resolve roles."))?;

    Ok(AuthContext(RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(user.id.to_string()),
        roles: Some(roles),
        function_id: None,
        context_attributes: None,
        // Basic auth has no bearer token to forward — nothing to carry here.
        forwarded_bearer_token: None,
    }))
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
