//! OIDC provider — the only provider here that's a genuinely separate implementation from
//! `LocalPasswordProvider` (redirect + callback, not a single verify-and-done call). A tenant's
//! config lives in `tenant_auth_configs.config` (jsonb), never a raw secret: `client_secret_ref`
//! names an env var, resolved the same way `metap-control::EnvStore` resolves a DB
//! `dsn_secret_ref` — deliberately not routed through `SecretStore` itself, since that trait is
//! shaped specifically for DB credentials (`db_credentials() -> DbCreds`), not a generic secret
//! store; reusing it here would mean bending its return type to fit an unrelated concern.
//!
//! No discovery-result caching yet — every login/callback re-fetches the IdP's provider metadata
//! fresh. Correct first, and simpler to test (no cache invalidation to reason about); worth
//! revisiting once there's a real tenant hitting this in practice and discovery latency actually
//! matters (same "ship the increment, let real usage show what's next" approach the rest of this
//! phase followed).

use openidconnect::core::{CoreAuthenticationFlow, CoreClient, CoreProviderMetadata};
use openidconnect::{
    AuthorizationCode, ClientId, ClientSecret, CsrfToken, IssuerUrl, Nonce, PkceCodeChallenge, PkceCodeVerifier,
    RedirectUrl, Scope, TokenResponse,
};
use serde::{Deserialize, Serialize};
use sqlx::PgExecutor;
use uuid::Uuid;

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OidcConfig {
    pub issuer_url: String,
    pub client_id: String,
    /// Name of an env var holding the real client secret — never the secret itself (this struct
    /// round-trips through `tenant_auth_configs.config`, a plain jsonb column, no encryption at
    /// that layer).
    pub client_secret_ref: String,
    pub redirect_uri: String,
    #[serde(default)]
    pub scopes: Vec<String>,
    /// Where the browser is sent after a successful callback, token appended as a URL fragment
    /// (`#token=...`) — the frontend origin, not this API's own origin.
    pub post_login_redirect: String,
}

pub struct VerifiedIdentity {
    pub email: String,
    pub external_subject: String,
}

/// A tenant's enabled OIDC config, or `None` if it has none — mirrors `enabled_providers`'
/// query shape but returns the parsed config `oidc_authorize_url`/`oidc_verify_callback` need.
pub async fn oidc_config<'e>(executor: impl PgExecutor<'e>, tenant_id: Uuid) -> anyhow::Result<Option<OidcConfig>> {
    let row: Option<serde_json::Value> = sqlx::query_scalar(
        "SELECT config FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'oidc' AND enabled = true",
    )
    .bind(tenant_id)
    .fetch_optional(executor)
    .await?;
    row.map(|v| serde_json::from_value(v).map_err(anyhow::Error::from))
        .transpose()
}

pub fn resolve_client_secret_env(client_secret_ref: &str) -> anyhow::Result<String> {
    std::env::var(client_secret_ref)
        .map_err(|_| anyhow::anyhow!("env var {client_secret_ref} (OIDC client secret ref) is not set"))
}

fn http_client() -> anyhow::Result<reqwest::Client> {
    // No redirect-follow — an IdP redirecting our own discovery/token requests somewhere else is
    // exactly the SSRF shape `openidconnect`'s own docs warn about, not a legitimate flow.
    Ok(reqwest::ClientBuilder::new()
        .redirect(reqwest::redirect::Policy::none())
        .build()?)
}

// Not factored into a shared helper returning a named `CoreClient` type: `openidconnect` v4
// tracks which endpoints (auth/token/userinfo/...) are set on a client at the type level
// (`EndpointSet`/`EndpointNotSet`/`EndpointMaybeSet` markers baked into the generic params), so
// the concrete type after `.set_redirect_uri(...)` doesn't match the plain `CoreClient` alias —
// letting each function infer its own client type inline sidesteps naming it.

/// Returns `(authorize_url, csrf_token, nonce, pkce_verifier)` — the caller (`crates/metap-http`)
/// is responsible for stashing `csrf_token -> (tenant_id, nonce, pkce_verifier)` somewhere that
/// survives until the callback (`OidcFlowCache`), since this crate has no HTTP-session concept of
/// its own.
pub async fn oidc_authorize_url(
    config: &OidcConfig,
    client_secret: &str,
) -> anyhow::Result<(String, String, String, String)> {
    let http = http_client()?;
    let issuer = IssuerUrl::new(config.issuer_url.clone())?;
    let metadata = CoreProviderMetadata::discover_async(issuer, &http).await?;
    let client = CoreClient::from_provider_metadata(
        metadata,
        ClientId::new(config.client_id.clone()),
        Some(ClientSecret::new(client_secret.to_string())),
    )
    .set_redirect_uri(RedirectUrl::new(config.redirect_uri.clone())?);

    let (pkce_challenge, pkce_verifier) = PkceCodeChallenge::new_random_sha256();

    let mut request = client
        .authorize_url(
            CoreAuthenticationFlow::AuthorizationCode,
            CsrfToken::new_random,
            Nonce::new_random,
        )
        .set_pkce_challenge(pkce_challenge);
    for scope in &config.scopes {
        request = request.add_scope(Scope::new(scope.clone()));
    }
    let (auth_url, csrf_token, nonce) = request.url();

    Ok((
        auth_url.to_string(),
        csrf_token.secret().clone(),
        nonce.secret().clone(),
        pkce_verifier.secret().clone(),
    ))
}

/// Exchanges the callback's `code` for tokens and verifies the id_token's signature + `nonce`
/// (`openidconnect` does the actual JWKS fetch/signature check — no crypto implemented here).
pub async fn oidc_verify_callback(
    config: &OidcConfig,
    client_secret: &str,
    code: &str,
    nonce: &str,
    pkce_verifier: &str,
) -> anyhow::Result<VerifiedIdentity> {
    let http = http_client()?;
    let issuer = IssuerUrl::new(config.issuer_url.clone())?;
    let metadata = CoreProviderMetadata::discover_async(issuer, &http).await?;
    let client = CoreClient::from_provider_metadata(
        metadata,
        ClientId::new(config.client_id.clone()),
        Some(ClientSecret::new(client_secret.to_string())),
    )
    .set_redirect_uri(RedirectUrl::new(config.redirect_uri.clone())?);

    let token_response = client
        .exchange_code(AuthorizationCode::new(code.to_string()))?
        .set_pkce_verifier(PkceCodeVerifier::new(pkce_verifier.to_string()))
        .request_async(&http)
        .await?;

    let id_token = token_response
        .id_token()
        .ok_or_else(|| anyhow::anyhow!("IdP token response has no id_token"))?;
    let claims = id_token.claims(&client.id_token_verifier(), &Nonce::new(nonce.to_string()))?;
    let email = claims
        .email()
        .ok_or_else(|| anyhow::anyhow!("IdP did not return an email claim"))?
        .to_string();

    Ok(VerifiedIdentity {
        email,
        external_subject: claims.subject().to_string(),
    })
}
