//! The issuer-only half of the JWKS trust model (`metap-jwks`'s doc comment) —
//! `GET /.well-known/jwks.json`, public, no auth (a JWKS document is public key material by
//! definition, same trust model any OAuth2/OIDC provider's own `jwks_uri` uses). Only the one
//! WAAP microservice nominated as the trust root's issuer mounts this; every other microservice
//! only needs `metap-jwks`'s `JwksClient` (the verifying side), not this crate — same "optional
//! capability, its own crate" shape as `metap-control-http`/`metap-lowcode-http`.
//!
//! Deliberately returns a plain, already-stateful `axum::Router` rather than a
//! `Router<metap_http::AppState>` merged via `metap_http::build_router`'s `extra_routes` —
//! doing that would make `metap-http` (or this crate) reach across a dependency direction
//! neither should (`metap-http` has zero dependency on this crate, matching `metap-control-http`/
//! `metap-lowcode-http`'s own rule, and this crate has no reason to depend on `metap-http` just
//! to reuse its `AppState` shape when it needs none of its fields). The returned `Router`
//! implements `tower::Service` directly (axum's `Router<()>` blanket impl), so a binary mounts it
//! into its own `AppState`-typed app the same way any other foreign `tower::Service` gets
//! composed into an axum app, regardless of the outer router's state type:
//!
//! ```ignore
//! let jwks_router = metap_jwks_http::router(key_store.clone());
//! let app = build_router(state, &cors_origins, extra_routes)
//!     .nest_service("/", jwks_router);
//! ```
//!
//! `nest_service("/", ...)`, not `route_service("/.well-known/jwks.json", ...)` — axum 0.8
//! refuses the latter outright for a `Router`-typed service ("cannot be used with Routers, use
//! Router::nest instead"; an earlier axum version this crate was first written against allowed
//! it — found live, `../metap-demo-waf/data-plane/services/zones-service/src/main.rs`,
//! 2026-09-04). Nesting at `/` strips no path prefix, so this crate's own internally-registered
//! `/.well-known/jwks.json` route is exposed unchanged. (`nest_service` still accepts any
//! `tower::Service<Request, Error = Infallible>` — `Router<()>` is one — independent of the
//! caller's own state type; see `axum::Router::merge`'s
//! doc comment for why `.merge()` itself can't be used here instead, since that requires both
//! routers to share the same state type.)

use std::sync::Arc;

use axum::extract::State;
use axum::response::Json;
use axum::routing::get;
use axum::Router;
use metap_jwks::{JwkSet, JwksKeyStore};
use tokio::sync::RwLock;

/// `RwLock`, not a plain `Mutex` — reads (every JWKS request) vastly outnumber writes (a
/// rotation step), and `tokio::sync::RwLock` lets concurrent requests actually serve in
/// parallel instead of serializing on every read the way a `Mutex` would.
pub type SharedJwksKeyStore = Arc<RwLock<JwksKeyStore>>;

pub fn router(key_store: SharedJwksKeyStore) -> Router {
    Router::new()
        .route("/.well-known/jwks.json", get(jwks_handler))
        .with_state(key_store)
}

async fn jwks_handler(State(key_store): State<SharedJwksKeyStore>) -> Json<JwkSet> {
    Json(key_store.read().await.jwk_set())
}

#[cfg(test)]
mod tests {
    use std::net::SocketAddr;
    use std::sync::Arc;

    use metap_jwks::JwksKeyPair;
    use tokio::net::TcpListener;
    use tokio::sync::RwLock;

    use super::*;

    #[tokio::test]
    async fn serves_the_current_jwk_set_as_json() {
        let key = JwksKeyPair::generate("kid-1").unwrap();
        let store: SharedJwksKeyStore = Arc::new(RwLock::new(JwksKeyStore::new(key)));

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr: SocketAddr = listener.local_addr().unwrap();
        let app = router(store.clone());
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        let body: JwkSet = reqwest::get(format!("http://{addr}/.well-known/jwks.json"))
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        assert_eq!(body.keys.len(), 1);
        assert_eq!(body.keys[0].kid, "kid-1");
        assert_eq!(body.keys[0].kty, "OKP");
    }

    #[tokio::test]
    async fn reflects_a_rotation_applied_after_the_router_was_built() {
        let key_a = JwksKeyPair::generate("kid-a").unwrap();
        let store: SharedJwksKeyStore = Arc::new(RwLock::new(JwksKeyStore::new(key_a)));

        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr: SocketAddr = listener.local_addr().unwrap();
        let app = router(store.clone());
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        store.write().await.add_key(JwksKeyPair::generate("kid-b").unwrap());

        let body: JwkSet = reqwest::get(format!("http://{addr}/.well-known/jwks.json"))
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        assert_eq!(body.keys.len(), 2);
    }
}
