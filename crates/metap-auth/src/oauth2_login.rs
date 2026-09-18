//! Plain OAuth2 authorization-code login provider — a tenant logging its users in against an
//! IdP that speaks OAuth2 but not OIDC (no discovery document, no `id_token`; GitHub's own OAuth
//! apps are the canonical example). Distinct from `oidc.rs` in exactly one way that matters:
//! there is no signed identity token to decode, so identity is resolved by calling a configured
//! **userinfo endpoint** with the freshly obtained access token and reading two fields out of
//! whatever JSON it returns.
//!
//! Reuses the same `VerifiedIdentity{email, external_subject}` shape `oidc.rs` produces
//! (`oidc.rs`'s own doc comment), so `crates/metap-http/src/routes/auth.rs`'s callback handler
//! and `find_external_user`/`jit_provision_external_user` (this crate's `lib.rs`, generalized
//! off their `"oidc"`-only originals) serve both providers without a second code path.

use oauth2::basic::BasicClient;
use oauth2::{
    AuthUrl, AuthorizationCode, ClientId, ClientSecret, CsrfToken, EndpointNotSet, EndpointSet, PkceCodeChallenge,
    PkceCodeVerifier, RedirectUrl, Scope, TokenResponse, TokenUrl,
};
use serde::{Deserialize, Serialize};
use sqlx::PgExecutor;
use uuid::Uuid;

use crate::oidc::VerifiedIdentity;

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OAuth2LoginConfig {
    pub authorize_url: String,
    pub token_url: String,
    /// Called with `Authorization: Bearer <access_token>` right after the code exchange — the
    /// substitute for an OIDC `id_token`'s claims.
    pub userinfo_url: String,
    pub client_id: String,
    /// Same "name of an env var, never the secret itself" contract as `OidcConfig::client_secret_ref`.
    pub client_secret_ref: String,
    pub redirect_uri: String,
    #[serde(default)]
    pub scopes: Vec<String>,
    pub post_login_redirect: String,
    /// Field name in the userinfo JSON response holding the provider's stable user identifier —
    /// configurable because it varies by provider (GitHub: `"id"`, a numeric field; many others:
    /// `"sub"`, matching OIDC's own claim name). Defaults to `"id"`.
    #[serde(default = "default_subject_field")]
    pub subject_field: String,
    /// Field name holding the user's email. Defaults to `"email"`.
    #[serde(default = "default_email_field")]
    pub email_field: String,
}

fn default_subject_field() -> String {
    "id".to_string()
}

fn default_email_field() -> String {
    "email".to_string()
}

pub async fn oauth2_login_config<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
) -> anyhow::Result<Option<OAuth2LoginConfig>> {
    let row: Option<serde_json::Value> = sqlx::query_scalar(
        "SELECT config FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'oauth2' AND enabled = true",
    )
    .bind(tenant_id)
    .fetch_optional(executor)
    .await?;
    row.map(|v| serde_json::from_value(v).map_err(anyhow::Error::from))
        .transpose()
}

// `EndpointSet`/`EndpointNotSet` markers mirror `oidc.rs`'s own note on why a client's concrete
// type isn't named as a return type here — see that file's comment for the full reasoning
// (`oauth2` v5 tracks which endpoints are configured at the type level, same as `openidconnect`
// v4, which is built directly on this crate).
type LoginClient = BasicClient<EndpointSet, EndpointNotSet, EndpointNotSet, EndpointNotSet, EndpointSet>;

fn build_client(config: &OAuth2LoginConfig, client_secret: &str) -> anyhow::Result<LoginClient> {
    Ok(BasicClient::new(ClientId::new(config.client_id.clone()))
        .set_client_secret(ClientSecret::new(client_secret.to_string()))
        .set_auth_uri(AuthUrl::new(config.authorize_url.clone())?)
        .set_token_uri(TokenUrl::new(config.token_url.clone())?)
        .set_redirect_uri(RedirectUrl::new(config.redirect_uri.clone())?))
}

fn http_client() -> anyhow::Result<reqwest::Client> {
    // No redirect-follow, same SSRF-shaped reasoning `oidc.rs::http_client` documents.
    Ok(reqwest::ClientBuilder::new()
        .redirect(reqwest::redirect::Policy::none())
        .build()?)
}

/// `https` only, with a loopback exception for a local mock IdP (`oauth2_login_e2e.rs`'s
/// `wiremock::MockServer` binds a plain-`http://127.0.0.1:<port>` listener — same allowance
/// RFC 8252 makes for a native app's own `http://localhost` redirect URI) — a real tenant-admin-
/// configured `userinfo_url` pointing anywhere else over plain `http` would send the access token
/// in cleartext.
fn require_https_or_loopback(url: &reqwest::Url) -> anyhow::Result<()> {
    if url.scheme() == "https" {
        return Ok(());
    }
    // `Url::host()` gives the parsed `Host`, not `host_str()`'s display form — the latter wraps
    // an IPv6 address in `[]` brackets, which `IpAddr::parse` would then reject outright.
    let is_loopback = match url.host() {
        Some(url::Host::Domain(d)) => d == "localhost",
        Some(url::Host::Ipv4(ip)) => ip.is_loopback(),
        Some(url::Host::Ipv6(ip)) => ip.is_loopback(),
        None => false,
    };
    if is_loopback {
        return Ok(());
    }
    anyhow::bail!("userinfo_url must use https (got {:?})", url.scheme());
}

/// Returns `(authorize_url, csrf_token, pkce_verifier)` — one fewer element than
/// `oidc_authorize_url`'s tuple (no `nonce`: that's an OIDC-specific replay defense tied to the
/// `id_token` this provider never receives). The caller (`crates/metap-http`) stashes
/// `csrf_token -> (tenant_id, pkce_verifier)` the same way it already does for OIDC
/// (`OidcFlowCache`/`OidcFlowEntry`, reused here with an empty `nonce` field — see that cache's
/// doc comment).
pub fn oauth2_login_authorize_url(
    config: &OAuth2LoginConfig,
    client_secret: &str,
) -> anyhow::Result<(String, String, String)> {
    let client = build_client(config, client_secret)?;
    let (pkce_challenge, pkce_verifier) = PkceCodeChallenge::new_random_sha256();

    let mut request = client
        .authorize_url(CsrfToken::new_random)
        .set_pkce_challenge(pkce_challenge);
    for scope in &config.scopes {
        request = request.add_scope(Scope::new(scope.clone()));
    }
    let (auth_url, csrf_token) = request.url();

    Ok((
        auth_url.to_string(),
        csrf_token.secret().clone(),
        pkce_verifier.secret().clone(),
    ))
}

/// Exchanges the callback's `code`, then calls `userinfo_url` with the resulting access token
/// and reads `subject_field`/`email_field` out of the JSON response.
pub async fn oauth2_login_verify_callback(
    config: &OAuth2LoginConfig,
    client_secret: &str,
    code: &str,
    pkce_verifier: &str,
) -> anyhow::Result<VerifiedIdentity> {
    let client = build_client(config, client_secret)?;
    let http = http_client()?;

    let token_response = client
        .exchange_code(AuthorizationCode::new(code.to_string()))
        .set_pkce_verifier(PkceCodeVerifier::new(pkce_verifier.to_string()))
        .request_async(&http)
        .await
        .map_err(|e| anyhow::anyhow!("OAuth2 code exchange failed: {e}"))?;

    // The access token goes out in this request's `Authorization` header — refuse to send it
    // anywhere but `https`, since `userinfo_url` is tenant-admin-configured and nothing upstream
    // of this call validates its scheme (CodeQL: cleartext transmission of sensitive information).
    let userinfo_url =
        reqwest::Url::parse(&config.userinfo_url).map_err(|e| anyhow::anyhow!("invalid userinfo_url: {e}"))?;
    require_https_or_loopback(&userinfo_url)?;

    let userinfo: serde_json::Value = http
        .get(userinfo_url)
        .bearer_auth(token_response.access_token().secret())
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;

    let external_subject = userinfo
        .get(&config.subject_field)
        .ok_or_else(|| anyhow::anyhow!("userinfo response has no {:?} field", config.subject_field))?
        // A subject id is legitimately either a JSON string or number depending on provider
        // (GitHub's `id` is numeric) — normalized to a string either way, since
        // `external_subject` is stored/compared as text regardless of the source shape.
        .to_string()
        .trim_matches('"')
        .to_string();
    let email = userinfo
        .get(&config.email_field)
        .and_then(|v| v.as_str())
        .ok_or_else(|| anyhow::anyhow!("userinfo response has no {:?} string field", config.email_field))?
        .to_string();

    Ok(VerifiedIdentity {
        email,
        external_subject,
    })
}

#[cfg(test)]
mod tests {
    use super::require_https_or_loopback;

    #[test]
    fn https_is_always_allowed() {
        let url = reqwest::Url::parse("https://idp.example/user").unwrap();
        assert!(require_https_or_loopback(&url).is_ok());
    }

    #[test]
    fn plain_http_to_a_real_host_is_rejected() {
        let url = reqwest::Url::parse("http://idp.example/user").unwrap();
        assert!(require_https_or_loopback(&url).is_err());
    }

    #[test]
    fn plain_http_to_loopback_is_allowed_for_local_mock_idps() {
        for raw in [
            "http://127.0.0.1:8080/user",
            "http://localhost:8080/user",
            "http://[::1]:8080/user",
        ] {
            let url = reqwest::Url::parse(raw).unwrap();
            assert!(require_https_or_loopback(&url).is_ok(), "{raw} should be allowed");
        }
    }
}
