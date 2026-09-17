//! The mint-side counterpart to `verifier::TokenVerifier` — the same "which trust root" choice,
//! made explicit for `POST /auth/login` and friends (`crates/metap-http/src/routes/auth.rs`,
//! the only caller today) instead of every minting call site hardcoding
//! `metap_peripherals::mint_jwt`.

use std::sync::Arc;

use metap_peripherals::{mint_jwt, mint_oauth_access_token};
use uuid::Uuid;

use crate::keys::JwksKeyPair;
use crate::mint::{mint_scoped_service_or_user_jwt, mint_service_or_user_jwt};

pub enum TokenSigner {
    Static {
        /// Raw PEM, matching `metap_peripherals::mint_jwt`'s own signature — see
        /// `crates/metap-http/src/state.rs`'s `jwt_encoding_key_pem` field doc comment for why
        /// this stays unparsed between mints.
        private_key_pem: Arc<str>,
    },
    Jwks {
        key: Arc<JwksKeyPair>,
    },
}

/// Mints a token via whichever trust root `signer` names — see `verifier::decode_with_verifier`
/// for the read-side equivalent this mirrors.
pub fn mint_with_signer(
    signer: &TokenSigner,
    tenant_id: Uuid,
    user_id: Uuid,
    function_id: Option<String>,
    ttl_seconds: u64,
) -> anyhow::Result<String> {
    match signer {
        TokenSigner::Static { private_key_pem } => mint_jwt(private_key_pem, tenant_id, user_id, ttl_seconds),
        TokenSigner::Jwks { key } => mint_service_or_user_jwt(key, tenant_id, user_id, function_id, ttl_seconds),
    }
}

/// The OAuth2-authorization-server counterpart to [`mint_with_signer`] — see
/// `metap_peripherals::mint_oauth_access_token`'s doc comment for why this is a sibling function
/// rather than 2 more parameters on that one.
pub fn mint_oauth_token_with_signer(
    signer: &TokenSigner,
    tenant_id: Uuid,
    user_id: Uuid,
    ttl_seconds: u64,
    scope: &str,
    client_id: &str,
) -> anyhow::Result<String> {
    match signer {
        TokenSigner::Static { private_key_pem } => {
            mint_oauth_access_token(private_key_pem, tenant_id, user_id, ttl_seconds, scope, client_id)
        }
        TokenSigner::Jwks { key } => {
            mint_scoped_service_or_user_jwt(key, tenant_id, user_id, ttl_seconds, scope, client_id)
        }
    }
}
