//! Turns an incoming RPC's metadata into a `RequestContext` — the gRPC counterpart to
//! `crates/metap-http/src/auth.rs`'s `AuthContext` extractor, deliberately built as a plain
//! async function each RPC handler calls at its own top rather than a `tonic::service::Interceptor`:
//! that trait's `call` is synchronous (`FnMut(Request<()>) -> Result<Request<()>, Status>`), and
//! this needs an async DB round-trip (`metap_control::resolve_request_context`'s role lookup) —
//! the same reason `AuthContext` itself is an axum extractor (also async) rather than
//! middleware. A `tower::Layer`-based async middleware wrapping every RPC uniformly is a
//! reasonable later refactor once there's more than one RPC group needing it; with a single
//! generic `CrudService` service today, calling this once per handler is no more repetitive than
//! `AuthContext` being an extractor parameter on every REST route handler already is.
//!
//! **On-behalf-of-user only, for now.** Every RPC here authenticates via a forwarded bearer
//! token (`authorization` gRPC metadata) — either JWKS-verified (`JwksClient`, the multi-service
//! trust root) or the platform's own static per-app key (`decode_access_token`), whichever the
//! caller configured. Pure machine-to-machine calls with no originating user (mTLS-authenticated,
//! per the design this crate implements) are a deliberately deferred extension point: this
//! crate's job is to accept an optional `tonic::transport::ServerTlsConfig`/`ClientTlsConfig` (see
//! `serve.rs`) so a deployment *can* run mTLS between services, but deriving a `RequestContext`
//! from a peer certificate's identity is a deployment-specific convention (which SAN field means
//! what tenant/role) that doesn't exist yet anywhere in this platform — inventing one here without
//! a real deployment to validate against would be speculative, not a real implementation. A
//! request with a valid client cert and no `authorization` metadata is rejected today
//! (`Status::unauthenticated`) rather than silently treated as some guessed-at service identity.

use metap_control::{resolve_request_context, ContextAttributesCache, Router};
use metap_jwks::JwksClient;
use metap_peripherals::decode_access_token;
use metap_permission::RequestContext;
use tonic::metadata::MetadataMap;
use tonic::Status;
use uuid::Uuid;

/// Either verification path an issuing deployment might configure — exactly one of the two,
/// never both, since a token is signed by one trust root or the other. `Static` mirrors
/// `crates/metap-http/src/auth.rs`'s single-per-app-keypair model (for a microservice that
/// hasn't opted into the JWKS trust root); `Jwks` is the multi-service trust root
/// (`metap-jwks`'s doc comment).
pub enum TokenVerifier {
    Static {
        decoding_key: jsonwebtoken::DecodingKey,
        leeway: u64,
    },
    Jwks {
        client: std::sync::Arc<JwksClient>,
        leeway: u64,
    },
}

/// Everything `authenticate` needs beyond the request itself — bundled so `GrpcCrudService`
/// holds exactly one of these rather than four loose fields, and so a caller only has to update
/// one place if this pipeline ever needs another input (mirroring `AppState`'s own "one struct"
/// shape on the HTTP side).
pub struct AuthConfig {
    pub verifier: TokenVerifier,
    pub router: Router,
    pub auth_context_entity: Option<String>,
    pub context_attributes_cache: ContextAttributesCache,
}

fn bearer_token(metadata: &MetadataMap) -> Result<&str, Status> {
    let raw = metadata
        .get("authorization")
        .ok_or_else(|| Status::unauthenticated("missing authorization metadata"))?
        .to_str()
        .map_err(|_| Status::unauthenticated("authorization metadata is not valid UTF-8"))?;
    raw.strip_prefix("Bearer ")
        .ok_or_else(|| Status::unauthenticated("authorization metadata must be a Bearer token"))
}

pub async fn authenticate(metadata: &MetadataMap, config: &AuthConfig) -> Result<RequestContext, Status> {
    let token = bearer_token(metadata)?;

    let (tenant_id, user_id, function_id) = match &config.verifier {
        TokenVerifier::Static { decoding_key, leeway } => {
            let claims = decode_access_token(token, decoding_key, *leeway)
                .map_err(|_| Status::unauthenticated("invalid or expired token"))?;
            (claims.tenant_id, claims.sub, claims.function_id)
        }
        TokenVerifier::Jwks { client, leeway } => {
            let claims = client
                .decode(token, *leeway)
                .await
                .map_err(|_| Status::unauthenticated("invalid or expired token"))?;
            (claims.tenant_id, claims.sub, claims.function_id)
        }
    };

    let tenant_id =
        Uuid::parse_str(&tenant_id).map_err(|_| Status::unauthenticated("token is missing required claims"))?;
    let user_id = Uuid::parse_str(&user_id).map_err(|_| Status::unauthenticated("token is missing required claims"))?;

    resolve_request_context(
        &config.router,
        tenant_id,
        user_id,
        function_id,
        config.auth_context_entity.as_deref(),
        &config.context_attributes_cache,
    )
    .await
    .map_err(|_| Status::unauthenticated("failed to resolve roles"))
}
