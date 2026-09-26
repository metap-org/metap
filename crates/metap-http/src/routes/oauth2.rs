//! HTTP surface for `crates/metap-oauth-server` — metap acting as an OAuth2 Authorization
//! Server for a **third-party client** acting on behalf of a tenant's own user. See that crate's
//! own doc comment for the full design (scope shipped, why access tokens are ordinary platform
//! JWTs, how revocation works). Distinct from `routes::auth`'s `oauth2_login`/`oauth2_callback`
//! — those are the opposite direction, a tenant's user logging *into* metap via an external
//! OAuth2 IdP.
//!
//! **`oauth_clients`/`oauth_authorization_codes`/`oauth_refresh_tokens` are looked up against
//! the shared platform pool (`state.pool`), never `Router::begin(tenant)`** — `POST /oauth/token`
//! and `POST /oauth/revoke` resolve which tenant a request belongs to *from the row a caller-
//! supplied client_id/code/refresh-token resolves to*, which carries no tenant hint of its own.
//! Same reasoning `crates/migrations/0034_oauth2.sql` and `metap_control::tenant_schema`'s
//! `users`/`user_roles` exclusion both give: a lookup with no tenant picker can't be split across
//! per-tenant schemas.
//!
//! **Deliberate simplification, not strict RFC 6749 behavior**: every error from
//! `GET /oauth/authorize`, including one raised after `client_id`/`redirect_uri` are already
//! confirmed registered, is returned as a direct HTTP error response rather than a redirect
//! carrying `?error=...`. A conforming client still gets a clear, immediate error; it just isn't
//! ferried back through the browser redirect the spec technically prescribes for that subset of
//! failures. Flagged rather than silently deviated from.
//!
//! **Real interactive consent (2026-09-19)**: `authorize` used to treat any already-authenticated
//! caller as approving the client's request outright — no screen, no way for a user to see what
//! they were granting. It now renders a plain server-rendered HTML page (`consent_page_html`) the
//! first time a `(client, user)` pair has no prior [`metap_oauth_server::get_consent_scope`]
//! covering the requested scope, and skips straight to the redirect exactly as before once one
//! does — same "don't nag a returning user" behavior every real IdP has. The page is rendered by
//! this crate, not handed off to `platform-ui`/a tenant's own frontend: the consent decision is
//! part of the auth boundary itself (deciding what a third party can do as this user), not a
//! business screen, and rendering it here needs no new "where is this tenant's frontend hosted"
//! config concept the platform doesn't have today. Its approve/deny action
//! (`POST /oauth/authorize/decision`) is a `fetch()` call from an inline `<script>`, not a plain
//! HTML form post, specifically so it can carry the `X-CSRF-Token` header the existing
//! cookie-session CSRF defense (`crate::auth`, double-submit against `metap_csrf`) already
//! requires on every cookie-authenticated mutation — a native form submission has no way to set a
//! custom header at all.

use axum::extract::{Query, State};
use axum::http::header::AUTHORIZATION;
use axum::http::{HeaderMap, StatusCode};
use axum::response::{IntoResponse, Redirect, Response};
use axum::routing::{get, post};
use axum::{Form, Json, Router};
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine as _;
use serde::{Deserialize, Serialize};
use serde_json::json;
use utoipa_axum::router::OpenApiRouter;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

/// No config key for this yet (`metap-oauth-server`'s own doc comment flags it as a follow-up
/// alongside `client_credentials`) — 1 hour matches the platform's own pre-config-tiers session
/// TTL default, a reasonable fixed starting point for a token a real integration is expected to
/// refresh rather than hold indefinitely.
const OAUTH_ACCESS_TOKEN_TTL_SECONDS: u64 = 3600;

fn percent_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

// -------------------------------------------------------------------------------------------
// GET /oauth/authorize
// -------------------------------------------------------------------------------------------

#[derive(Deserialize)]
struct AuthorizeQuery {
    response_type: String,
    client_id: String,
    redirect_uri: String,
    #[serde(default)]
    scope: String,
    state: Option<String>,
    code_challenge: Option<String>,
    code_challenge_method: Option<String>,
}

/// Requires an existing metap session (`AuthContext` — cookie or Bearer, whichever the caller's
/// browser already carries). Renders a real consent screen (see this module's own doc comment)
/// unless the caller has already approved this client for at least this scope before, in which
/// case it skips straight to minting the code exactly like a returning-user IdP would.
async fn authorize(
    State(state): State<AppState>,
    AuthContext(context): AuthContext,
    Query(query): Query<AuthorizeQuery>,
) -> Response {
    if query.response_type != "code" {
        return service_error_response(
            400,
            "unsupported_response_type",
            Some("Only response_type=code is supported."),
            None,
        );
    }
    let Ok(tenant_id) = Uuid::parse_str(&context.tenant_id) else {
        return internal_error_response(anyhow::anyhow!("session context has an invalid tenant id"));
    };
    let Some(user_id) = context.user_id.as_deref().and_then(|id| Uuid::parse_str(id).ok()) else {
        return service_error_response(
            401,
            "unauthorized",
            Some("A user session is required to authorize a client."),
            None,
        );
    };

    let client = match metap_oauth_server::get_client_by_client_id(&state.pool, &query.client_id).await {
        Ok(Some(c)) => c.client,
        Ok(None) => return service_error_response(400, "invalid_client", Some("Unknown client_id."), None),
        Err(e) => return internal_error_response(e),
    };
    if client.revoked_at.is_some() {
        return service_error_response(400, "invalid_client", Some("This client has been revoked."), None);
    }
    if client.tenant_id != tenant_id {
        return service_error_response(
            403,
            "invalid_client",
            Some("This client does not belong to your tenant."),
            None,
        );
    }
    if !client.redirect_uris.iter().any(|u| u == &query.redirect_uri) {
        return service_error_response(
            400,
            "invalid_request",
            Some("redirect_uri is not registered for this client."),
            None,
        );
    }
    if let Some(method) = &query.code_challenge_method {
        if method != "S256" {
            return service_error_response(
                400,
                "invalid_request",
                Some("Only code_challenge_method=S256 is supported."),
                None,
            );
        }
    }
    if !client.is_confidential && query.code_challenge.is_none() {
        return service_error_response(
            400,
            "invalid_request",
            Some("code_challenge (PKCE) is required for a public client."),
            None,
        );
    }
    if !metap_oauth_server::scope_is_subset(&query.scope, &client.allowed_scopes.join(" ")) {
        return service_error_response(
            400,
            "invalid_scope",
            Some("Requested scope exceeds what this client is allowed to request."),
            None,
        );
    }

    let consented_scope = match metap_oauth_server::get_consent_scope(&state.pool, client.id, user_id).await {
        Ok(s) => s,
        Err(e) => return internal_error_response(e),
    };
    let already_consented =
        consented_scope.is_some_and(|consented| metap_oauth_server::scope_is_subset(&query.scope, &consented));

    if already_consented {
        return match issue_authorization_code(
            &state.pool,
            client.id,
            tenant_id,
            user_id,
            &query.redirect_uri,
            &query.scope,
            query.code_challenge.as_deref(),
            query.code_challenge_method.as_deref(),
            query.state.as_deref(),
        )
        .await
        {
            Ok(redirect_to) => Redirect::to(&redirect_to).into_response(),
            Err(e) => internal_error_response(e),
        };
    }

    let pending = match metap_oauth_server::create_pending_authorization(
        &state.pool,
        metap_oauth_server::CreatePendingAuthorizationInput {
            client_id: client.id,
            tenant_id,
            user_id,
            redirect_uri: query.redirect_uri.clone(),
            scope: query.scope.clone(),
            code_challenge: query.code_challenge.clone(),
            code_challenge_method: query.code_challenge_method.clone(),
            client_state: query.state.clone(),
        },
    )
    .await
    {
        Ok(p) => p,
        Err(e) => return internal_error_response(e),
    };

    axum::response::Html(consent_page_html(&client.name, &query.scope, pending.id)).into_response()
}

// -------------------------------------------------------------------------------------------
// Shared: minting a code + building the redirect it goes back on
// -------------------------------------------------------------------------------------------

/// Builds the `redirect_uri?code=...&state=...` target every successful authorization ends on —
/// shared by [`authorize`]'s "already consented, skip the screen" path and
/// [`authorize_decision`]'s "just approved" path so the two can't drift.
#[allow(clippy::too_many_arguments)]
async fn issue_authorization_code(
    pool: &sqlx::PgPool,
    client_id: Uuid,
    tenant_id: Uuid,
    user_id: Uuid,
    redirect_uri: &str,
    scope: &str,
    code_challenge: Option<&str>,
    code_challenge_method: Option<&str>,
    client_state: Option<&str>,
) -> anyhow::Result<String> {
    let (_, raw_code) = metap_oauth_server::create_authorization_code(
        pool,
        metap_oauth_server::CreateCodeInput {
            client_id,
            tenant_id,
            user_id,
            redirect_uri: redirect_uri.to_string(),
            scope: scope.to_string(),
            code_challenge: code_challenge.map(str::to_string),
            code_challenge_method: code_challenge_method.map(str::to_string),
        },
    )
    .await?;

    let sep = if redirect_uri.contains('?') { '&' } else { '?' };
    let mut redirect_to = format!("{redirect_uri}{sep}code={}", percent_encode(&raw_code));
    if let Some(s) = client_state {
        redirect_to.push_str(&format!("&state={}", percent_encode(s)));
    }
    Ok(redirect_to)
}

fn denied_redirect(redirect_uri: &str, client_state: Option<&str>) -> String {
    let sep = if redirect_uri.contains('?') { '&' } else { '?' };
    let mut redirect_to = format!("{redirect_uri}{sep}error=access_denied");
    if let Some(s) = client_state {
        redirect_to.push_str(&format!("&state={}", percent_encode(s)));
    }
    redirect_to
}

fn escape_html(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&#39;")
}

/// A minimal, dependency-free consent screen — see this module's own top doc comment for why it's
/// server-rendered here rather than a `platform-ui` page. `client_name`/`scope` are escaped since
/// both ultimately come from a tenant admin's own free-text input (`POST /admin/oauth/clients`);
/// low-risk (an admin attacking their own tenant's users), but cheap to close properly regardless.
fn consent_page_html(client_name: &str, scope: &str, request_id: Uuid) -> String {
    let scope_items = metap_oauth_server::scope_tokens(scope)
        .iter()
        .map(|s| format!("<li>{}</li>", escape_html(s)))
        .collect::<String>();
    let scope_list = if scope_items.is_empty() {
        "<li>(no specific permissions requested)</li>".to_string()
    } else {
        scope_items
    };
    format!(
        r#"<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Authorize application</title>
<style>
  body {{ font-family: system-ui, sans-serif; max-width: 32rem; margin: 4rem auto; padding: 0 1rem; color: #1a1a1a; }}
  h1 {{ font-size: 1.25rem; }}
  ul {{ background: #f5f5f5; border-radius: 0.5rem; padding: 1rem 1rem 1rem 2rem; }}
  .actions {{ margin-top: 1.5rem; display: flex; gap: 0.75rem; }}
  button {{ font-size: 1rem; padding: 0.5rem 1.25rem; border-radius: 0.375rem; border: 1px solid #ccc; cursor: pointer; }}
  #approve {{ background: #111827; color: #fff; border-color: #111827; }}
  #error {{ color: #b91c1c; margin-top: 1rem; }}
</style>
</head>
<body>
  <h1>{client_name} is requesting access to your account</h1>
  <p>This application would like to:</p>
  <ul>{scope_list}</ul>
  <div class="actions">
    <button id="approve" type="button">Approve</button>
    <button id="deny" type="button">Deny</button>
  </div>
  <p id="error" hidden></p>
  <script>
    function csrfToken() {{
      const m = document.cookie.match(/(?:^|; )metap_csrf=([^;]*)/);
      return m ? decodeURIComponent(m[1]) : "";
    }}
    async function decide(approve) {{
      try {{
        const res = await fetch("/oauth/authorize/decision", {{
          method: "POST",
          credentials: "include",
          headers: {{ "content-type": "application/json", "x-csrf-token": csrfToken() }},
          body: JSON.stringify({{ requestId: "{request_id}", approve: approve }}),
        }});
        const body = await res.json();
        if (res.ok && body.redirectTo) {{
          window.location.href = body.redirectTo;
          return;
        }}
        throw new Error((body.error && body.error.message) || "request failed");
      }} catch (e) {{
        const el = document.getElementById("error");
        el.hidden = false;
        el.textContent = "Something went wrong: " + e.message;
      }}
    }}
    document.getElementById("approve").addEventListener("click", () => decide(true));
    document.getElementById("deny").addEventListener("click", () => decide(false));
  </script>
</body>
</html>"#,
        client_name = escape_html(client_name),
    )
}

// -------------------------------------------------------------------------------------------
// POST /oauth/authorize/decision
// -------------------------------------------------------------------------------------------

#[derive(Deserialize)]
struct DecisionBody {
    #[serde(rename = "requestId")]
    request_id: Uuid,
    approve: bool,
}

#[derive(Serialize)]
struct DecisionResponseDto {
    #[serde(rename = "redirectTo")]
    redirect_to: String,
}

/// The consent page's approve/deny action. Requires the same live session that saw the consent
/// screen — `consume_pending_authorization` matches `tenant_id`/`user_id` in its own query, so a
/// pending authorization created for one user can't be decided by another, even within the same
/// tenant.
async fn authorize_decision(
    State(state): State<AppState>,
    AuthContext(context): AuthContext,
    Json(body): Json<DecisionBody>,
) -> Response {
    let Ok(tenant_id) = Uuid::parse_str(&context.tenant_id) else {
        return internal_error_response(anyhow::anyhow!("session context has an invalid tenant id"));
    };
    let Some(user_id) = context.user_id.as_deref().and_then(|id| Uuid::parse_str(id).ok()) else {
        return service_error_response(401, "unauthorized", Some("A user session is required."), None);
    };

    // `tenant_id`/`user_id` are matched inside the query itself (see that function's own doc
    // comment) — a pending request belonging to a different user in the same tenant comes back as
    // `None` here, the same as an unknown/expired id, rather than a distinguishable error; same
    // "don't reveal why" posture this codebase already applies to login/refresh-token failures.
    let pending =
        match metap_oauth_server::consume_pending_authorization(&state.pool, body.request_id, tenant_id, user_id).await
        {
            Ok(Some(p)) => p,
            Ok(None) => {
                return service_error_response(
                    400,
                    "invalid_request",
                    Some("This authorization request has expired or was already decided. Please start over."),
                    None,
                )
            }
            Err(e) => return internal_error_response(e),
        };

    if !body.approve {
        return Json(DecisionResponseDto {
            redirect_to: denied_redirect(&pending.redirect_uri, pending.client_state.as_deref()),
        })
        .into_response();
    }

    if let Err(e) =
        metap_oauth_server::record_consent(&state.pool, tenant_id, pending.client_id, user_id, &pending.scope).await
    {
        return internal_error_response(e);
    }

    match issue_authorization_code(
        &state.pool,
        pending.client_id,
        tenant_id,
        user_id,
        &pending.redirect_uri,
        &pending.scope,
        pending.code_challenge.as_deref(),
        pending.code_challenge_method.as_deref(),
        pending.client_state.as_deref(),
    )
    .await
    {
        Ok(redirect_to) => Json(DecisionResponseDto { redirect_to }).into_response(),
        Err(e) => internal_error_response(e),
    }
}

// -------------------------------------------------------------------------------------------
// POST /oauth/token
// -------------------------------------------------------------------------------------------

/// `Authorization: Basic base64(client_id:client_secret)` first (RFC 6749 §2.3.1's preferred
/// form), falling back to `client_id`/`client_secret` form fields — real client libraries use
/// either, so both are accepted rather than picking one.
fn extract_client_auth(
    headers: &HeaderMap,
    form_client_id: Option<&str>,
    form_client_secret: Option<&str>,
) -> Option<(String, String)> {
    if let Some(header) = headers.get(AUTHORIZATION).and_then(|v| v.to_str().ok()) {
        if let Some(b64) = header.strip_prefix("Basic ") {
            if let Some((id, secret)) = BASE64
                .decode(b64)
                .ok()
                .and_then(|bytes| String::from_utf8(bytes).ok())
                .and_then(|s| s.split_once(':').map(|(a, b)| (a.to_string(), b.to_string())))
            {
                return Some((id, secret));
            }
        }
    }
    match (form_client_id, form_client_secret) {
        (Some(id), Some(secret)) => Some((id.to_string(), secret.to_string())),
        _ => None,
    }
}

/// The `Err` is boxed (clippy's `result_large_err`) since a full `Response` is much larger than
/// the `Ok` variant — same pattern as `metap-graphql-gateway::server::authenticate`.
async fn authenticate_client(
    state: &AppState,
    client_id: &str,
    client_secret: &str,
) -> Result<metap_oauth_server::ClientWithSecret, Box<Response>> {
    let client = metap_oauth_server::get_client_by_client_id(&state.pool, client_id)
        .await
        .map_err(internal_error_response)
        .map_err(Box::new)?
        .ok_or_else(|| {
            Box::new(service_error_response(
                401,
                "invalid_client",
                Some("Unknown client."),
                None,
            ))
        })?;
    if !metap_oauth_server::verify_client_secret(&client, client_secret) {
        return Err(Box::new(service_error_response(
            401,
            "invalid_client",
            Some("Invalid client credentials."),
            None,
        )));
    }
    Ok(client)
}

#[derive(Deserialize)]
struct TokenForm {
    grant_type: String,
    code: Option<String>,
    redirect_uri: Option<String>,
    code_verifier: Option<String>,
    refresh_token: Option<String>,
    scope: Option<String>,
    client_id: Option<String>,
    client_secret: Option<String>,
}

/// RFC 6749 §5.1's exact response shape (`access_token`/`token_type`/`expires_in`/
/// `refresh_token`/`scope`, no `{"data": ...}` envelope) — a deliberate exception to this crate's
/// own REST convention everywhere else, because a real OAuth2 client library parses this
/// response against the spec's shape, not this platform's.
#[derive(Serialize)]
struct TokenResponseDto {
    access_token: String,
    token_type: &'static str,
    expires_in: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    refresh_token: Option<String>,
    scope: String,
}

async fn token(State(state): State<AppState>, headers: HeaderMap, Form(body): Form<TokenForm>) -> Response {
    let Some((client_id_str, client_secret)) =
        extract_client_auth(&headers, body.client_id.as_deref(), body.client_secret.as_deref())
    else {
        return service_error_response(401, "invalid_client", Some("Client authentication is required."), None);
    };
    let client = match authenticate_client(&state, &client_id_str, &client_secret).await {
        Ok(c) => c,
        Err(resp) => return *resp,
    };
    if client.client.revoked_at.is_some() {
        return service_error_response(401, "invalid_client", Some("This client has been revoked."), None);
    }

    match body.grant_type.as_str() {
        "authorization_code" => authorization_code_grant(&state, &client, body).await,
        "refresh_token" => refresh_token_grant(&state, &client, body).await,
        "client_credentials" => client_credentials_grant(&state, &client, body).await,
        _ => service_error_response(
            400,
            "unsupported_grant_type",
            Some("Only authorization_code, refresh_token, and client_credentials are supported."),
            None,
        ),
    }
}

/// RFC 6749 §4.4 — no resource owner in the loop, the client acts as itself. Mints a token for
/// `client.service_user_id` (see that field's own doc comment: eagerly provisioned at
/// `POST /admin/oauth/clients` time, `None` only for a client registered before this grant
/// existed) rather than any particular tenant user. No refresh token is issued (§4.4.3) — the
/// client already holds its own long-lived credential (`client_secret`) and can request a fresh
/// access token the same way at any time.
async fn client_credentials_grant(
    state: &AppState,
    client: &metap_oauth_server::ClientWithSecret,
    body: TokenForm,
) -> Response {
    if !client.client.is_confidential {
        return service_error_response(
            400,
            "unauthorized_client",
            Some("client_credentials requires a confidential client."),
            None,
        );
    }
    let Some(service_user_id) = client.client.service_user_id else {
        return service_error_response(
            400,
            "unauthorized_client",
            Some("This client has no service account provisioned; re-register it to use client_credentials."),
            None,
        );
    };
    let requested_scope = body.scope.as_deref().unwrap_or("");
    if !metap_oauth_server::scope_is_subset(requested_scope, &client.client.allowed_scopes.join(" ")) {
        return service_error_response(
            400,
            "invalid_scope",
            Some("Requested scope exceeds what this client is allowed to request."),
            None,
        );
    }

    let access_token = match state.mint_oauth_token(
        client.client.tenant_id,
        service_user_id,
        OAUTH_ACCESS_TOKEN_TTL_SECONDS,
        requested_scope,
        &client.client.client_id,
    ) {
        Ok(t) => t,
        Err(e) => return internal_error_response(e),
    };

    Json(TokenResponseDto {
        access_token,
        token_type: "Bearer",
        expires_in: OAUTH_ACCESS_TOKEN_TTL_SECONDS,
        refresh_token: None,
        scope: requested_scope.to_string(),
    })
    .into_response()
}

async fn authorization_code_grant(
    state: &AppState,
    client: &metap_oauth_server::ClientWithSecret,
    body: TokenForm,
) -> Response {
    let (Some(code), Some(redirect_uri)) = (body.code, body.redirect_uri) else {
        return service_error_response(
            400,
            "invalid_request",
            Some("code and redirect_uri are required."),
            None,
        );
    };

    let record =
        match metap_oauth_server::consume_authorization_code(&state.pool, &code, client.client.id, &redirect_uri).await
        {
            Ok(Some(r)) => r,
            Ok(None) => {
                return service_error_response(
                    400,
                    "invalid_grant",
                    Some("Invalid, expired, or already-used authorization code."),
                    None,
                )
            }
            Err(e) => return internal_error_response(e),
        };

    // `authorize`'s own check already refuses a public client no `code_challenge`, so reaching
    // here with `record.code_challenge` unset means the client is confidential and simply chose
    // not to use PKCE, which RFC 7636 allows — nothing further to verify in that case.
    if let Some(challenge) = &record.code_challenge {
        let Some(verifier) = &body.code_verifier else {
            return service_error_response(
                400,
                "invalid_grant",
                Some("code_verifier is required for this authorization code."),
                None,
            );
        };
        if !metap_oauth_server::verify_pkce(verifier, challenge) {
            return service_error_response(
                400,
                "invalid_grant",
                Some("code_verifier does not match code_challenge."),
                None,
            );
        }
    }

    let access_token = match state.mint_oauth_token(
        record.tenant_id,
        record.user_id,
        OAUTH_ACCESS_TOKEN_TTL_SECONDS,
        &record.scope,
        &client.client.client_id,
    ) {
        Ok(t) => t,
        Err(e) => return internal_error_response(e),
    };

    let mut tx = match state.pool.begin().await {
        Ok(tx) => tx,
        Err(e) => return internal_error_response(e.into()),
    };
    let (_, raw_refresh) = match metap_oauth_server::create_refresh_token(
        &mut tx,
        metap_oauth_server::CreateRefreshInput {
            client_id: client.client.id,
            tenant_id: record.tenant_id,
            user_id: record.user_id,
            scope: record.scope.clone(),
        },
    )
    .await
    {
        Ok(v) => v,
        Err(e) => return internal_error_response(e),
    };
    if let Err(e) = tx.commit().await {
        return internal_error_response(e.into());
    }

    Json(TokenResponseDto {
        access_token,
        token_type: "Bearer",
        expires_in: OAUTH_ACCESS_TOKEN_TTL_SECONDS,
        refresh_token: Some(raw_refresh),
        scope: record.scope,
    })
    .into_response()
}

async fn refresh_token_grant(
    state: &AppState,
    client: &metap_oauth_server::ClientWithSecret,
    body: TokenForm,
) -> Response {
    let Some(raw_refresh) = body.refresh_token else {
        return service_error_response(400, "invalid_request", Some("refresh_token is required."), None);
    };

    let mut tx = match state.pool.begin().await {
        Ok(tx) => tx,
        Err(e) => return internal_error_response(e.into()),
    };
    let outcome = match metap_oauth_server::consume_refresh_token(&mut tx, &raw_refresh, client.client.id).await {
        Ok(o) => o,
        Err(e) => {
            let _ = tx.rollback().await;
            return internal_error_response(e);
        }
    };

    match outcome {
        metap_oauth_server::ConsumeRefreshOutcome::Invalid => {
            let _ = tx.rollback().await;
            service_error_response(
                400,
                "invalid_grant",
                Some("Invalid, expired, or revoked refresh token."),
                None,
            )
        }
        metap_oauth_server::ConsumeRefreshOutcome::Reused => {
            // The chain revocation already happened inside `consume_refresh_token` — commit so
            // it sticks, then report the failure.
            if let Err(e) = tx.commit().await {
                return internal_error_response(e.into());
            }
            service_error_response(
                400,
                "invalid_grant",
                Some("Refresh token reuse detected; this client's token chain has been revoked. The user must re-authorize."),
                None,
            )
        }
        metap_oauth_server::ConsumeRefreshOutcome::Rotated(record, raw_new_refresh) => {
            let requested_scope = body.scope.as_deref().unwrap_or(&record.scope);
            if !metap_oauth_server::scope_is_subset(requested_scope, &record.scope) {
                let _ = tx.rollback().await;
                return service_error_response(
                    400,
                    "invalid_scope",
                    Some("Requested scope exceeds the originally granted scope."),
                    None,
                );
            }
            let access_token = match state.mint_oauth_token(
                record.tenant_id,
                record.user_id,
                OAUTH_ACCESS_TOKEN_TTL_SECONDS,
                requested_scope,
                &client.client.client_id,
            ) {
                Ok(t) => t,
                Err(e) => {
                    let _ = tx.rollback().await;
                    return internal_error_response(e);
                }
            };
            if let Err(e) = tx.commit().await {
                return internal_error_response(e.into());
            }
            Json(TokenResponseDto {
                access_token,
                token_type: "Bearer",
                expires_in: OAUTH_ACCESS_TOKEN_TTL_SECONDS,
                refresh_token: Some(raw_new_refresh),
                scope: requested_scope.to_string(),
            })
            .into_response()
        }
    }
}

// -------------------------------------------------------------------------------------------
// POST /oauth/revoke
// -------------------------------------------------------------------------------------------

#[derive(Deserialize)]
struct RevokeForm {
    token: String,
    client_id: Option<String>,
    client_secret: Option<String>,
}

async fn revoke(State(state): State<AppState>, headers: HeaderMap, Form(body): Form<RevokeForm>) -> Response {
    let Some((client_id_str, client_secret)) =
        extract_client_auth(&headers, body.client_id.as_deref(), body.client_secret.as_deref())
    else {
        return service_error_response(401, "invalid_client", Some("Client authentication is required."), None);
    };
    let client = match authenticate_client(&state, &client_id_str, &client_secret).await {
        Ok(c) => c,
        Err(resp) => return *resp,
    };
    // Errors here still degrade to success per RFC 7009 §2.2 (see `revoke_refresh_token`'s doc
    // comment) — this only turns a genuine infrastructure failure into a 500 the caller can
    // retry, not "token not found" into one.
    if let Err(e) = metap_oauth_server::revoke_refresh_token(&state.pool, &body.token, client.client.id).await {
        return internal_error_response(e);
    }
    StatusCode::OK.into_response()
}

// -------------------------------------------------------------------------------------------
// GET /.well-known/oauth-authorization-server (RFC 8414)
// -------------------------------------------------------------------------------------------

/// Endpoint paths only, not absolute URIs — this platform has no configured public base-URL
/// surface today (every other route in this crate is also referenced by relative path in its own
/// docs), so a strictly spec-conforming absolute-URI `issuer`/`*_endpoint` would need a new
/// config key this pass doesn't add. A caller resolves these against whatever origin it already
/// reached this document on, which is what every real client actually does in practice.
async fn discovery_metadata() -> Response {
    Json(json!({
        "issuer": metap_peripherals::JWT_ISSUER,
        "authorization_endpoint": "/oauth/authorize",
        "token_endpoint": "/oauth/token",
        "revocation_endpoint": "/oauth/revoke",
        "response_types_supported": ["code"],
        "grant_types_supported": ["authorization_code", "refresh_token", "client_credentials"],
        "code_challenge_methods_supported": ["S256"],
        "token_endpoint_auth_methods_supported": ["client_secret_basic", "client_secret_post"],
    }))
    .into_response()
}

// `/admin/oauth/clients*` (admin CRUD over registered clients) moved to GraphQL 2026-09-26
// (`createOAuthClient`/`oauthClients`/`revokeOAuthClient`, `metap-graphql-http::platform_fields`)
// — see `../metap-docs/docs/roadmap/95-platform-graphql-fields.md`. Nothing below this point is
// part of the OAuth2/OIDC wire protocol REST had to keep it near; it was only ever admin-CRUD
// that happened to live in this file. Everything above (the 5 routes this file still serves) is
// permanently REST — a third-party OAuth2 client library speaks RFC 6749/7009/8414, not GraphQL.

// Deliberately plain `.route()` calls, not `routes!` — these aren't `utoipa`-documented (matches
// `routes::auth`'s own `logout`/`issue_token`, undocumented for the same reason: not in scope of
// this crate's utoipa migration).
fn build_router() -> OpenApiRouter<AppState> {
    OpenApiRouter::new()
        .route("/oauth/authorize", get(authorize))
        .route("/oauth/authorize/decision", post(authorize_decision))
        .route("/oauth/token", post(token))
        .route("/oauth/revoke", post(revoke))
        .route("/.well-known/oauth-authorization-server", get(discovery_metadata))
}

pub fn router() -> Router<AppState> {
    build_router().split_for_parts().0
}

pub(crate) fn openapi() -> utoipa::openapi::OpenApi {
    build_router().split_for_parts().1
}
