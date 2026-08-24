//! Public `POST /auth/login` — the first route in this crate that mints a JWT instead of only
//! verifying one. See `metap_peripherals::auth`'s doc comment for why the encoding logic lives
//! there (shared with `dev-tools mint-token`), not here.

use axum::extract::{Path, Query, State};
use axum::response::{IntoResponse, Redirect, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::json;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::cache::OidcFlowEntry;
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
    // Local password is one of possibly several providers a tenant can enable
    // (`crates/metap-auth`'s doc comment) — this route only ever speaks the local one; Basic/OIDC
    // get their own extractor branch / routes, not this handler.
    let local = metap_auth::LocalPasswordProvider;
    let verify_result = match body.tenant_id {
        Some(tenant_id) => {
            let mut tx = match state.router.begin(tenant_id.into()).await {
                Ok(tx) => tx,
                Err(e) => return router_unavailable_response(e),
            };
            let result = local.verify(&mut *tx, &body.email, &body.password).await;
            if result.is_ok() {
                if let Err(e) = tx.commit().await {
                    return internal_error_response(e.into());
                }
            }
            result
        }
        None => local.verify(&state.pool, &body.email, &body.password).await,
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

#[derive(Deserialize)]
struct ProvidersQuery {
    #[serde(rename = "tenantId")]
    tenant_id: Uuid,
}

/// Public (no auth) — lets the frontend's login page decide which buttons to show (e.g. "Sign in
/// with SSO") for a given tenant without leaking any secret; `metap_auth::enabled_providers`
/// returns kinds only, never the `tenant_auth_configs.config` payload itself.
async fn list_providers(State(state): State<AppState>, Query(query): Query<ProvidersQuery>) -> Response {
    let mut tx = match state.router.begin(query.tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let kinds = match metap_auth::enabled_providers(&mut *tx, query.tenant_id).await {
        Ok(kinds) => kinds,
        Err(e) => return internal_error_response(e),
    };
    let _ = tx.commit().await;
    Json(json!({ "data": { "providers": kinds.iter().map(|k| k.as_str()).collect::<Vec<_>>() } })).into_response()
}

async fn oidc_config_or_404(state: &AppState, tenant_id: Uuid) -> Result<(metap_auth::OidcConfig, String), Response> {
    let mut tx = state
        .router
        .begin(tenant_id.into())
        .await
        .map_err(router_unavailable_response)?;
    let config = metap_auth::oidc_config(&mut *tx, tenant_id)
        .await
        .map_err(internal_error_response)?
        .ok_or_else(|| {
            service_error_response(
                404,
                "oidc_not_configured",
                Some("OIDC is not enabled for this tenant."),
                None,
            )
        })?;
    let _ = tx.commit().await;
    let client_secret =
        metap_auth::resolve_client_secret_env(&config.client_secret_ref).map_err(internal_error_response)?;
    Ok((config, client_secret))
}

/// Redirects the browser to the tenant's IdP. Stashes the CSRF token this generates (as the
/// cache key) alongside the nonce/PKCE verifier the callback needs — `openidconnect` embeds the
/// CSRF token as the `state` query param on the URL it returns, so the callback gets it back
/// automatically from the IdP redirect.
async fn oidc_login(State(state): State<AppState>, Path(tenant_id): Path<Uuid>) -> Response {
    let (config, client_secret) = match oidc_config_or_404(&state, tenant_id).await {
        Ok(v) => v,
        Err(resp) => return resp,
    };
    let (auth_url, csrf_token, nonce, pkce_verifier) =
        match metap_auth::oidc_authorize_url(&config, &client_secret).await {
            Ok(v) => v,
            Err(e) => return internal_error_response(e),
        };
    state
        .oidc_flow_cache
        .insert(
            csrf_token,
            OidcFlowEntry {
                tenant_id,
                nonce,
                pkce_verifier,
            },
        )
        .await;
    Redirect::to(&auth_url).into_response()
}

#[derive(Deserialize)]
struct OidcCallbackQuery {
    code: String,
    state: String,
}

/// Exchanges the IdP's callback code, JIT-provisions (or reuses) the local `users` row, mints
/// the exact same kind of session JWT local login does (`metap_peripherals::mint_jwt` — from
/// here on, an OIDC-authenticated session is indistinguishable from a local one to every other
/// route), and redirects the browser back to the tenant's configured frontend with the token in
/// a URL fragment (`#token=...`, never a query param — fragments never reach server access logs
/// or `Referer` headers).
async fn oidc_callback(
    State(state): State<AppState>,
    Path(tenant_id): Path<Uuid>,
    Query(query): Query<OidcCallbackQuery>,
) -> Response {
    let Some(flow) = state.oidc_flow_cache.take(&query.state).await else {
        return service_error_response(
            400,
            "invalid_oidc_state",
            Some("OIDC login session expired or invalid."),
            None,
        );
    };
    if flow.tenant_id != tenant_id {
        return service_error_response(
            400,
            "invalid_oidc_state",
            Some("OIDC login session expired or invalid."),
            None,
        );
    }

    let (config, client_secret) = match oidc_config_or_404(&state, tenant_id).await {
        Ok(v) => v,
        Err(resp) => return resp,
    };
    let identity =
        match metap_auth::oidc_verify_callback(&config, &client_secret, &query.code, &flow.nonce, &flow.pkce_verifier)
            .await
        {
            Ok(v) => v,
            Err(e) => {
                tracing::warn!(%tenant_id, error = %e, "oidc callback verification failed");
                return service_error_response(
                    401,
                    "oidc_verification_failed",
                    Some("Failed to verify OIDC login."),
                    None,
                );
            }
        };

    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let existing = match metap_auth::find_oidc_user(&mut *tx, tenant_id, &identity.external_subject).await {
        Ok(v) => v,
        Err(e) => return internal_error_response(e),
    };
    let user = match existing {
        Some(user) => user,
        None => {
            match metap_auth::jit_provision_oidc_user(&mut *tx, tenant_id, &identity.email, &identity.external_subject)
                .await
            {
                Ok(user) => user,
                Err(e) => return internal_error_response(e),
            }
        }
    };
    if let Err(e) = tx.commit().await {
        return internal_error_response(e.into());
    }

    let token =
        match metap_peripherals::mint_jwt(&state.jwt_encoding_key_pem, user.tenant_id, user.id, TOKEN_TTL_SECONDS) {
            Ok(token) => token,
            Err(e) => return internal_error_response(e),
        };
    Redirect::to(&format!("{}#token={token}", config.post_login_redirect)).into_response()
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/auth/login", post(login))
        .route("/auth/me", get(me))
        .route("/auth/providers", get(list_providers))
        .route("/auth/oidc/{tenant_id}/login", get(oidc_login))
        .route("/auth/oidc/{tenant_id}/callback", get(oidc_callback))
}
