//! The verify-side dispatch every transport that accepts this platform's bearer/session tokens
//! needs: a token is either signed by a binary's own static per-app RS256 keypair
//! (`metap_peripherals::decode_access_token`, unchanged since before this crate existed) or by
//! the JWKS multi-service trust root (`JwksClient::decode`, this crate's own module doc). Both
//! produce the same `AccessClaims` shape, so a caller downstream of either can't tell which one
//! verified a given request's token — this enum is just the "which one do I have" choice made
//! explicit and reusable, instead of every transport (`metap-http`, `metap-grpc`,
//! `graphql-gateway`) re-deriving its own copy of the same 2-arm match. Lives in this crate
//! (not `metap-runtime`) because `metap-runtime` is meant to stay free of any specific
//! technology's plumbing, and this crate already owns both halves (`JwksClient` here,
//! `metap_peripherals` a dependency already) — putting it here also avoids a dependency cycle,
//! since `metap-jwks` itself already depends on `metap-runtime`.

use std::sync::Arc;

use jsonwebtoken::DecodingKey;
use metap_peripherals::{decode_access_token, AccessClaims};

use crate::client::JwksClient;

/// Either verification path an issuing deployment might configure — exactly one of the two,
/// never both, since a token is signed by one trust root or the other. `Static` mirrors the
/// platform's original single-per-app-keypair model (a microservice that hasn't opted into the
/// JWKS trust root); `Jwks` is the multi-service trust root. `Clone` is cheap for both variants
/// (`DecodingKey`/`Arc<JwksClient>`) — lets a caller that builds one verifier at boot (e.g.
/// `AppState.token_verifier`) hand a second owned copy to a sibling transport (gRPC) without
/// re-deriving it.
#[derive(Clone)]
pub enum TokenVerifier {
    Static { decoding_key: DecodingKey, leeway: u64 },
    Jwks { client: Arc<JwksClient>, leeway: u64 },
}

/// Verifies `token` against whichever trust root `verifier` names — the one place this dispatch
/// happens, so `metap-http::auth::AuthContext`, `metap-grpc::auth::authenticate`, and
/// `graphql-gateway::server::authenticate` can't drift from each other on how a `Static` vs
/// `Jwks` token gets checked.
pub async fn decode_with_verifier(
    token: &str,
    verifier: &TokenVerifier,
    leeway_override: Option<u64>,
) -> anyhow::Result<AccessClaims> {
    match verifier {
        TokenVerifier::Static { decoding_key, leeway } => {
            decode_access_token(token, decoding_key, leeway_override.unwrap_or(*leeway))
                .map_err(|e| anyhow::anyhow!("invalid or expired token: {e}"))
        }
        TokenVerifier::Jwks { client, leeway } => client
            .decode(token, leeway_override.unwrap_or(*leeway))
            .await
            .map_err(|e| anyhow::anyhow!("invalid or expired token: {e}")),
    }
}
