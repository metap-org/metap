//! Public `POST /auth/login` — the first route in this crate that mints a JWT instead of only
//! verifying one. See `metap_peripherals::auth`'s doc comment for why the encoding logic lives
//! there (shared with `dev-tools mint-token`), not here.

use axum::extract::{Path, Query, State};
use axum::http::header::SET_COOKIE;
use axum::http::{HeaderValue, StatusCode};
use axum::response::{IntoResponse, Redirect, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::json;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::cache::OidcFlowEntry;
use crate::cookies::{clear_session_cookies, session_cookies};
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

/// Session lifetime in seconds for one tenant (`docs/features/18-config-tiers-db-backed.md`) — the
/// single source for both the JWT's own `exp` and the session cookie's `Max-Age`, so the two can
/// never be derived from different values. Replaced a `TOKEN_TTL_SECONDS = 3600` constant, and 3600
/// is now that key's declared default, so behavior is unchanged until someone sets it.
///
/// Tenant-scoped since slice 2: how long its own users stay signed in is a policy call a tenant
/// makes for itself, resolved as `declared default <- platform fleet default <- this tenant's
/// override`. The bounds stay operator-controlled in `metap_config::keys`, so the worst a tenant
/// admin can do here is pick a legal value.
///
/// Read per login rather than cached at boot, so a change applies to the next login without a
/// restart — and off `AppState::effective_config`, which is cache-first, so it does not add a
/// database round trip to a login that would otherwise not need one.
async fn session_ttl_seconds(state: &AppState, tenant_id: Uuid) -> u64 {
    state
        .effective_config(tenant_id)
        .await
        .get_u64(metap_config::keys::AUTH_SESSION_TTL_SECONDS)
}

/// Appends both `Set-Cookie` headers from a `session_cookies`/`clear_session_cookies` pair onto
/// an already-built response — `HeaderMap` allows repeated header names via `append` (unlike
/// `insert`, which would silently drop the first), which is required here since both cookies
/// share the name of no other header but do need to coexist as two separate `Set-Cookie` lines.
fn attach_cookies(
    mut response: Response,
    cookies: (
        axum_extra::extract::cookie::Cookie<'static>,
        axum_extra::extract::cookie::Cookie<'static>,
    ),
) -> Response {
    let (a, b) = cookies;
    for cookie in [a, b] {
        if let Ok(value) = HeaderValue::from_str(&cookie.to_string()) {
            response.headers_mut().append(SET_COOKIE, value);
        }
    }
    response
}

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

    let ttl = session_ttl_seconds(&state, user.tenant_id).await;
    match state.mint_token(user.tenant_id, user.id, None, ttl) {
        // `token` still rides in the JSON body too — non-browser callers (`dev-tools mint-token`
        // never calls this route, but a mobile client or a script hitting `POST /auth/login`
        // directly would) have no cookie jar to read a `Set-Cookie` from and still need the JWT
        // handed to them explicitly to use as a Bearer token. `@metap/platform-ui`'s own
        // `LoginForm` stops reading this field (2026-09-03) now that the cookie does the job, but
        // the field stays for everyone else who never adopted it.
        Ok(token) => {
            let csrf_value = Uuid::new_v4().to_string();
            let cookies = session_cookies(&token, &csrf_value, ttl as i64, state.cookie_secure);
            let response = Json(json!({ "data": { "token": token } })).into_response();
            attach_cookies(response, cookies)
        }
        Err(e) => internal_error_response(e),
    }
}

/// Clears both session cookies regardless of whether the caller is currently authenticated —
/// logging out an already-logged-out browser (an expired cookie, a second tab that already
/// logged out) is a no-op either way, so there is nothing gained by requiring `AuthContext` here
/// and a real cost: a request whose cookie already expired would otherwise 401 on the one route
/// whose entire job is "make sure there's no session left."
async fn logout(State(state): State<AppState>) -> Response {
    let response = StatusCode::NO_CONTENT.into_response();
    attach_cookies(response, clear_session_cookies(state.cookie_secure))
}

/// Identity + roles for the caller's own token — the frontend's only way to know "am I an
/// admin" for UI gating, since roles are deliberately never encoded on the JWT itself (see
/// `crate::auth`'s doc comment): they're looked up fresh here the same way every other
/// `AuthContext` route does.
///
/// `email` is looked up here too (2026-09-03) rather than left for the caller to resolve. The JWT
/// carries only `sub`, so a frontend wanting to show "who am I" previously had to fetch the whole
/// tenant user list and search it — see `metap_peripherals::find_user_by_id`'s doc comment. It is
/// deliberately **additive and best-effort**: any failure resolving it (router unavailable, no
/// matching row, a token whose `sub` isn't a real user) yields `null` and the identity/roles
/// payload is returned unchanged, because those are what every caller actually gates on.
async fn me(State(state): State<AppState>, AuthContext(context): AuthContext) -> Response {
    let email = resolve_own_email(&state, &context).await;
    Json(json!({
        "data": {
            "userId": context.user_id,
            "tenantId": context.tenant_id,
            "email": email,
            "roles": context.roles.unwrap_or_default(),
        }
    }))
    .into_response()
}

/// Mints a fresh, short-lived Bearer token for the caller's own identity — added 2026-09-03
/// alongside cookie-based sessions, for exactly one kind of consumer: a browser caller that needs
/// to authenticate to a service which only ever speaks Bearer and has no cookie of its own to
/// send, e.g. `crates/graphql-gateway` (its own separate deployment/keypair, `Authorization:
/// Bearer` only — see that crate's `authenticate` function). `@metap/platform-ui`'s
/// `useGraphQLQuery` calls this immediately before every gateway request rather than holding a
/// token in memory across requests, so the thing this whole cookie migration was for (a session
/// that survives a page reload) isn't quietly reintroduced through this one path: nothing here is
/// ever persisted client-side, it's fetched fresh, used once, and discarded.
///
/// Accepts `AuthContext` — works whether the caller's own request arrived via cookie or Bearer —
/// and mints exactly the same kind of token `login`/`oidc_callback` do, so this can't diverge from
/// what a real login produces or grant anything a real login wouldn't have.
///
/// **Additionally CSRF-gated despite being a `GET`** (audit 04 finding A#4, 2026-09-03) — see
/// `crate::cookies::credential_issuing_request_allowed` for why a token-minting endpoint doesn't
/// get the safe-method exemption the rest of the API does. A Bearer caller is unaffected.
async fn issue_token(
    State(state): State<AppState>,
    headers: axum::http::HeaderMap,
    AuthContext(context): AuthContext,
) -> Response {
    if !crate::cookies::credential_issuing_request_allowed(&headers) {
        return service_error_response(401, "unauthorized", Some("Missing or invalid CSRF token."), None);
    }
    let Some(tenant_id) = Uuid::parse_str(&context.tenant_id).ok() else {
        return internal_error_response(anyhow::anyhow!("session context has an invalid tenant id"));
    };
    let Some(user_id) = context.user_id.as_deref().and_then(|id| Uuid::parse_str(id).ok()) else {
        return internal_error_response(anyhow::anyhow!("session context has no valid user id"));
    };
    match state.mint_token(tenant_id, user_id, None, session_ttl_seconds(&state, tenant_id).await) {
        Ok(token) => Json(json!({ "data": { "token": token } })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

async fn resolve_own_email(state: &AppState, context: &metap_permission::RequestContext) -> Option<String> {
    let user_id = Uuid::parse_str(context.user_id.as_deref()?).ok()?;
    let tenant_id = state.permissions.scoped_tenant(context).ok()?;
    let mut tx = state.router.begin(tenant_id.into()).await.ok()?;
    let user = metap_peripherals::find_user_by_id(&mut *tx, tenant_id, user_id)
        .await
        .ok()
        .flatten();
    let _ = tx.commit().await;
    user.map(|u| u.email)
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

async fn oidc_config_or_404(
    state: &AppState,
    tenant_id: Uuid,
) -> Result<(metap_auth::OidcConfig, String), Box<Response>> {
    let mut tx = state
        .router
        .begin(tenant_id.into())
        .await
        .map_err(|e| Box::new(router_unavailable_response(e)))?;
    let config = metap_auth::oidc_config(&mut *tx, tenant_id)
        .await
        .map_err(|e| Box::new(internal_error_response(e)))?
        .ok_or_else(|| {
            Box::new(service_error_response(
                404,
                "oidc_not_configured",
                Some("OIDC is not enabled for this tenant."),
                None,
            ))
        })?;
    let _ = tx.commit().await;
    let client_secret = metap_auth::resolve_client_secret_env(&config.client_secret_ref)
        .map_err(|e| Box::new(internal_error_response(e)))?;
    Ok((config, client_secret))
}

/// Redirects the browser to the tenant's IdP. Stashes the CSRF token this generates (as the
/// cache key) alongside the nonce/PKCE verifier the callback needs — `openidconnect` embeds the
/// CSRF token as the `state` query param on the URL it returns, so the callback gets it back
/// automatically from the IdP redirect.
async fn oidc_login(State(state): State<AppState>, Path(tenant_id): Path<Uuid>) -> Response {
    let (config, client_secret) = match oidc_config_or_404(&state, tenant_id).await {
        Ok(v) => v,
        Err(resp) => return *resp,
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
/// route), and redirects the browser back to the tenant's configured frontend.
///
/// The session cookies are set directly on this redirect response (2026-09-03) — superseding the
/// previous `#token=...` URL-fragment handoff (`@metap/platform-ui`'s `OidcCallbackPage`, before
/// it switched to reacting to auth status instead of reading a fragment). That approach existed
/// to keep the token out of server access logs and `Referer` headers, which a fragment achieves
/// but a `Set-Cookie` header achieves *more completely*: the token now never touches the URL at
/// all, not even transiently in the browser's address bar or history before client script could
/// scrub it. See `crate::cookies`'s doc comment for the cookies themselves.
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
        Err(resp) => return *resp,
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

    let token = match state.mint_token(user.tenant_id, user.id, None, session_ttl_seconds(&state, user.tenant_id).await) {
        Ok(token) => token,
        Err(e) => return internal_error_response(e),
    };
    let csrf_value = Uuid::new_v4().to_string();
    let cookies = session_cookies(
        &token,
        &csrf_value,
        session_ttl_seconds(&state, user.tenant_id).await as i64,
        state.cookie_secure,
    );
    let response = Redirect::to(&config.post_login_redirect).into_response();
    attach_cookies(response, cookies)
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/auth/login", post(login))
        .route("/auth/logout", post(logout))
        .route("/auth/me", get(me))
        .route("/auth/token", get(issue_token))
        .route("/auth/providers", get(list_providers))
        .route("/auth/oidc/{tenant_id}/login", get(oidc_login))
        .route("/auth/oidc/{tenant_id}/callback", get(oidc_callback))
}
