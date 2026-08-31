//! The verifying side of the JWKS trust model: fetches the issuer's published `JwkSet` over
//! HTTP, caches `DecodingKey`s by `kid`, and decodes/validates tokens against whichever key a
//! token's own header names. Every WAAP microservice that wants to accept a portal/service token
//! signed by the shared trust root holds one of these (as opposed to `metap-jwks-http`, which
//! only the single nominated issuer binary needs).

use std::sync::Arc;
use std::time::Duration;

use jsonwebtoken::{decode, decode_header, Algorithm, DecodingKey, Validation};
use metap_peripherals::{AccessClaims, JWT_AUDIENCE, JWT_ISSUER};
use moka::future::Cache;

use crate::keys::JwkSet;

/// Cache-miss behavior is "fetch the whole JWKS document, repopulate every key" rather than a
/// per-`kid` fetch — a JWKS document is one small JSON payload (a handful of keys at most), so
/// there is no benefit to a narrower fetch, and repopulating every key on any miss means a
/// verifier that's fallen behind by more than one rotation still catches up in a single request
/// instead of needing one miss per newly-unseen `kid`.
pub struct JwksClient {
    http: reqwest::Client,
    jwks_url: String,
    cache: Cache<String, Arc<DecodingKey>>,
}

impl JwksClient {
    pub fn new(jwks_url: impl Into<String>, ttl: Duration) -> Self {
        Self {
            http: metap_runtime::http_client::default_client(),
            jwks_url: jwks_url.into(),
            cache: Cache::builder().time_to_live(ttl).build(),
        }
    }

    /// Fetches the issuer's current `JwkSet` and repopulates the cache — used both by
    /// `decoding_key_for`'s miss path and by the periodic background refresh
    /// (`spawn_background_refresh`), so a key that's been rotated *out* is eventually evicted
    /// here too, not just a rotated-*in* key picked up.
    async fn refresh(&self) -> anyhow::Result<()> {
        let jwk_set: JwkSet = self
            .http
            .get(&self.jwks_url)
            .send()
            .await?
            .error_for_status()?
            .json()
            .await?;
        for jwk in &jwk_set.keys {
            let decoding_key = DecodingKey::from_ed_components(&jwk.x)?;
            self.cache.insert(jwk.kid.clone(), Arc::new(decoding_key)).await;
        }
        Ok(())
    }

    /// Cache-first; on a miss, refreshes from the issuer once before giving up — this is what
    /// makes a freshly rotated-in key usable immediately (the first verifier to see a token
    /// signed with it just pays one extra HTTP round-trip, no coordinated deploy needed).
    pub async fn decoding_key_for(&self, kid: &str) -> anyhow::Result<Arc<DecodingKey>> {
        if let Some(key) = self.cache.get(kid).await {
            return Ok(key);
        }
        self.refresh().await?;
        self.cache
            .get(kid)
            .await
            .ok_or_else(|| anyhow::anyhow!("kid {kid} not found in JWKS at {}", self.jwks_url))
    }

    /// Verifies a token minted by `mint_service_or_user_jwt` — reads the token's `kid` header to
    /// pick the right key (falling back to a fetch on a cache miss), then validates exactly like
    /// `metap_peripherals::decode_access_token` (`JWT_AUDIENCE`/`JWT_ISSUER`, `leeway` seconds of
    /// clock-skew tolerance), except `Algorithm::EdDSA` instead of `RS256` — the two decode
    /// functions intentionally share claim shape (`AccessClaims`) and issuer/audience so a
    /// caller downstream of either can't tell which one verified a given request's token.
    pub async fn decode(&self, token: &str, leeway: u64) -> anyhow::Result<AccessClaims> {
        let header = decode_header(token)?;
        let kid = header
            .kid
            .ok_or_else(|| anyhow::anyhow!("token has no kid — cannot select a JWKS verification key"))?;
        let key = self.decoding_key_for(&kid).await?;

        let mut validation = Validation::new(Algorithm::EdDSA);
        validation.set_audience(&[JWT_AUDIENCE]);
        validation.set_issuer(&[JWT_ISSUER]);
        validation.leeway = leeway;
        let data = decode::<AccessClaims>(token, &key, &validation)?;
        Ok(data.claims)
    }

    /// Opt-in background refresh so a verifier that never misses (e.g. only ever sees tokens
    /// signed by a `kid` it already cached long ago) still eventually notices that key was
    /// rotated out — without this, `decoding_key_for` alone would keep trusting a retired key
    /// forever once cached, since a cache *hit* never re-checks the issuer. Callers spawn this
    /// once at boot; the returned handle can be aborted on shutdown, matching this platform's
    /// other background-loop conventions (`outbox-publisher`/`notification-worker`).
    pub fn spawn_background_refresh(self: Arc<Self>, interval: Duration) -> tokio::task::JoinHandle<()> {
        tokio::spawn(async move {
            let mut ticker = tokio::time::interval(interval);
            ticker.tick().await; // first tick fires immediately; skip it, refresh already ran on first use
            loop {
                ticker.tick().await;
                if let Err(err) = self.refresh().await {
                    tracing::warn!(error = %err, jwks_url = %self.jwks_url, "background JWKS refresh failed");
                }
            }
        })
    }
}
