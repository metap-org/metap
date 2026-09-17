//! Mints a JWT signed by a [`JwksKeyPair`] — the JWKS-trust-root counterpart to
//! `metap_peripherals::mint_jwt` (RS256, single static per-app key). Deliberately reuses that
//! module's claim shape (`sub`/`tenantId`/`functionId`/`exp`/`iss`/`aud`, same `JWT_ISSUER`/
//! `JWT_AUDIENCE` constants) so a token minted here and one minted by `mint_jwt` are
//! semantically interchangeable claims-wise — only the signing key/algorithm and the presence of
//! a `kid` header differ. `functionId` absent (`None`) is skipped entirely (not serialized as
//! `null`), matching `metap_peripherals::AccessClaims`'s own optionality.

use jsonwebtoken::{encode, Algorithm, Header};
use metap_peripherals::{JWT_AUDIENCE, JWT_ISSUER};
use serde::Serialize;
use uuid::Uuid;

use crate::keys::JwksKeyPair;

#[derive(Serialize)]
struct Claims {
    sub: String,
    #[serde(rename = "tenantId")]
    tenant_id: String,
    #[serde(rename = "functionId", skip_serializing_if = "Option::is_none")]
    function_id: Option<String>,
    exp: usize,
    iss: String,
    aud: String,
    /// Mirrors `metap_peripherals::mint_oauth_access_token`'s claim — see that function's doc
    /// comment. Only [`mint_scoped_service_or_user_jwt`] sets this.
    #[serde(skip_serializing_if = "Option::is_none")]
    scope: Option<String>,
    #[serde(rename = "clientId", skip_serializing_if = "Option::is_none")]
    client_id: Option<String>,
}

/// A caller-on-behalf-of-user token (portal login, or forwarded end-user identity for an
/// on-behalf-of-user gRPC call) or a machine-identity token for pure service-to-service calls —
/// this function doesn't distinguish the two, since the claim shape is identical either way
/// (see `crates/metap-http/src/auth.rs`'s M2M-vs-user split, which happens on the *verifying*
/// side by inspecting `roles`, not here on the minting side).
pub fn mint_service_or_user_jwt(
    key: &JwksKeyPair,
    tenant_id: Uuid,
    user_id: Uuid,
    function_id: Option<String>,
    ttl_seconds: u64,
) -> anyhow::Result<String> {
    mint_claims(key, tenant_id, user_id, function_id, ttl_seconds, None, None)
}

/// The JWKS-trust-root counterpart to `metap_peripherals::mint_oauth_access_token` — same
/// reasoning as that function's doc comment, mirrored here so a deployment on the JWKS trust
/// root (`crates/metap-http/src/state.rs`'s `token_signer`) can mint OAuth2 access tokens too.
pub fn mint_scoped_service_or_user_jwt(
    key: &JwksKeyPair,
    tenant_id: Uuid,
    user_id: Uuid,
    ttl_seconds: u64,
    scope: &str,
    client_id: &str,
) -> anyhow::Result<String> {
    mint_claims(
        key,
        tenant_id,
        user_id,
        None,
        ttl_seconds,
        Some(scope.to_string()),
        Some(client_id.to_string()),
    )
}

#[allow(clippy::too_many_arguments)]
fn mint_claims(
    key: &JwksKeyPair,
    tenant_id: Uuid,
    user_id: Uuid,
    function_id: Option<String>,
    ttl_seconds: u64,
    scope: Option<String>,
    client_id: Option<String>,
) -> anyhow::Result<String> {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?;
    let claims = Claims {
        sub: user_id.to_string(),
        tenant_id: tenant_id.to_string(),
        function_id,
        exp: (now.as_secs() + ttl_seconds) as usize,
        iss: JWT_ISSUER.to_string(),
        aud: JWT_AUDIENCE.to_string(),
        scope,
        client_id,
    };
    let mut header = Header::new(Algorithm::EdDSA);
    header.kid = Some(key.kid.clone());
    Ok(encode(&header, &claims, &key.encoding_key())?)
}
