//! A JWKS-based multi-service trust root — an alternative to the platform's default per-app
//! static RS256 keypair (`metap_peripherals::mint_jwt`/`crates/metap-http/src/auth.rs`) for a
//! deployment where several separately-deployed microservices (all one product, e.g. a WAAP
//! deployment's many sub-modules) need to trust the *same* signing identity: one nominated
//! issuer binary holds the only private key(s) (`JwksKeyStore`, `metap-jwks-http`'s
//! `GET /.well-known/jwks.json`), every other microservice verifies locally via a cached
//! `JwksClient` — no shared private key file ever needs to be copied between processes, and no
//! per-request network round-trip to the issuer is needed once a key is cached.
//!
//! Entirely opt-in: `crates/metap-http`, `apps/crm-server`, and `apps/jira-server` have zero
//! dependency on this crate and are unaffected by its existence — see this crate's own tests and
//! `docs/roadmap.md` for the phase that introduced it.

mod client;
mod keys;
mod mint;

pub use client::JwksClient;
pub use keys::{Jwk, JwkSet, JwksKeyPair, JwksKeyStore};
pub use mint::mint_service_or_user_jwt;

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use uuid::Uuid;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    use super::*;

    #[test]
    fn a_freshly_generated_key_signs_and_verifies_itself() {
        let key = JwksKeyPair::generate("kid-1").unwrap();
        let tenant_id = Uuid::new_v4();
        let user_id = Uuid::new_v4();
        let token = mint_service_or_user_jwt(&key, tenant_id, user_id, None, 3600).unwrap();

        let decoding_key = key.decoding_key().unwrap();
        let mut validation = jsonwebtoken::Validation::new(jsonwebtoken::Algorithm::EdDSA);
        validation.set_audience(&[metap_peripherals::JWT_AUDIENCE]);
        validation.set_issuer(&[metap_peripherals::JWT_ISSUER]);
        let claims = jsonwebtoken::decode::<metap_peripherals::AccessClaims>(&token, &decoding_key, &validation)
            .unwrap()
            .claims;
        assert_eq!(claims.sub, user_id.to_string());
        assert_eq!(claims.tenant_id, tenant_id.to_string());
    }

    #[test]
    fn a_token_signed_by_one_key_does_not_verify_against_another() {
        let key_a = JwksKeyPair::generate("kid-a").unwrap();
        let key_b = JwksKeyPair::generate("kid-b").unwrap();
        let token = mint_service_or_user_jwt(&key_a, Uuid::new_v4(), Uuid::new_v4(), None, 3600).unwrap();

        let decoding_key_b = key_b.decoding_key().unwrap();
        let mut validation = jsonwebtoken::Validation::new(jsonwebtoken::Algorithm::EdDSA);
        validation.set_audience(&[metap_peripherals::JWT_AUDIENCE]);
        validation.set_issuer(&[metap_peripherals::JWT_ISSUER]);
        let result = jsonwebtoken::decode::<metap_peripherals::AccessClaims>(&token, &decoding_key_b, &validation);
        assert!(result.is_err());
    }

    #[test]
    fn jwk_set_round_trips_through_json() {
        let key = JwksKeyPair::generate("kid-1").unwrap();
        let store = JwksKeyStore::new(key);
        let json = serde_json::to_string(&store.jwk_set()).unwrap();
        let parsed: JwkSet = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed.keys.len(), 1);
        assert_eq!(parsed.keys[0].kty, "OKP");
        assert_eq!(parsed.keys[0].crv, "Ed25519");
        assert_eq!(parsed.keys[0].kid, "kid-1");
    }

    #[test]
    fn rotation_publishes_the_next_key_before_signing_with_it() {
        let key_a = JwksKeyPair::generate("kid-a").unwrap();
        let mut store = JwksKeyStore::new(key_a);
        assert_eq!(store.signing_key().kid, "kid-a");

        let key_b = JwksKeyPair::generate("kid-b").unwrap();
        store.add_key(key_b);
        // Published (in the JWKS) but not yet signing.
        assert_eq!(store.jwk_set().keys.len(), 2);
        assert_eq!(store.signing_key().kid, "kid-a");

        store.promote("kid-b").unwrap();
        assert_eq!(store.signing_key().kid, "kid-b");
        // The retired key stays published during the grace window.
        assert_eq!(store.jwk_set().keys.len(), 2);

        store.remove_key("kid-a").unwrap();
        assert_eq!(store.jwk_set().keys.len(), 1);
    }

    #[test]
    fn promoting_an_unpublished_kid_fails() {
        let mut store = JwksKeyStore::new(JwksKeyPair::generate("kid-a").unwrap());
        assert!(store.promote("does-not-exist").is_err());
    }

    #[test]
    fn removing_the_signing_key_fails() {
        let mut store = JwksKeyStore::new(JwksKeyPair::generate("kid-a").unwrap());
        assert!(store.remove_key("kid-a").is_err());
    }

    #[tokio::test]
    async fn jwks_client_verifies_a_token_after_fetching_the_issuers_jwk_set() {
        let key = JwksKeyPair::generate("kid-1").unwrap();
        let store = JwksKeyStore::new(key);
        let token = mint_service_or_user_jwt(store.signing_key(), Uuid::new_v4(), Uuid::new_v4(), None, 3600).unwrap();

        let server = MockServer::start().await;
        Mock::given(method("GET"))
            .and(path("/.well-known/jwks.json"))
            .respond_with(ResponseTemplate::new(200).set_body_json(store.jwk_set()))
            .mount(&server)
            .await;

        let client = JwksClient::new(
            format!("{}/.well-known/jwks.json", server.uri()),
            Duration::from_secs(300),
        );
        let claims = client.decode(&token, 20).await.unwrap();
        assert_eq!(claims.tenant_id.len(), 36); // a UUID string, roundtripped through the token
    }

    #[tokio::test]
    async fn jwks_client_picks_up_a_newly_rotated_in_key_on_cache_miss() {
        let key_a = JwksKeyPair::generate("kid-a").unwrap();
        let mut store = JwksKeyStore::new(key_a);

        // Rotate in a second key before the client (created below) has ever fetched anything —
        // its first fetch, triggered by decoding `token_b`, must see the *current* (post-
        // rotation) JWKS, which already contains kid-b, since a real issuer's endpoint always
        // reflects its live rotation state, not a stale snapshot from before rotation.
        let key_b = JwksKeyPair::generate("kid-b").unwrap();
        store.add_key(key_b);
        store.promote("kid-b").unwrap();
        let token_b =
            mint_service_or_user_jwt(store.signing_key(), Uuid::new_v4(), Uuid::new_v4(), None, 3600).unwrap();

        let server = MockServer::start().await;
        Mock::given(method("GET"))
            .and(path("/.well-known/jwks.json"))
            .respond_with(ResponseTemplate::new(200).set_body_json(store.jwk_set()))
            .mount(&server)
            .await;

        let client = JwksClient::new(
            format!("{}/.well-known/jwks.json", server.uri()),
            Duration::from_secs(300),
        );
        let claims = client.decode(&token_b, 20).await.unwrap();
        assert_eq!(claims.sub.len(), 36);
    }
}
