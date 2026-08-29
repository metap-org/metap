//! Ed25519 keypair generation + JWK/JWKS document shape. Ed25519 (`EdDSA`), not RS256, for
//! every token this crate mints — smaller tokens, faster verify than RS256, and a good fit for
//! the "performance" half of the multi-service trust model's requirement (per-app login tokens
//! elsewhere in this platform stay RS256 via `metap_peripherals::mint_jwt`; this crate is only
//! for the new inter-service/portal trust root, not a replacement for that).
//!
//! Deliberately works in raw bytes end to end (PKCS8 DER for the private half, a base64url JWK
//! `x` value for the public half) rather than PEM — Ed25519's JWK encoding (`kty: "OKP"`) *is*
//! just the raw 32-byte public key, so there is no PEM/SPKI ASN.1 step to round-trip through;
//! `ring::signature::Ed25519KeyPair::generate_pkcs8` already returns exactly the DER bytes
//! `jsonwebtoken::EncodingKey::from_ed_der` wants, and `jsonwebtoken::DecodingKey::from_ed_components`
//! already accepts exactly the base64url string a JWK's `x` field carries.

use base64::engine::general_purpose::URL_SAFE_NO_PAD as B64;
use base64::Engine as _;
use jsonwebtoken::{DecodingKey, EncodingKey};
use ring::signature::{Ed25519KeyPair, KeyPair};
use serde::{Deserialize, Serialize};

/// One Ed25519 signing identity: a `kid` (key id, carried in every JWT this key signs so a
/// verifier holding several published keys knows which one to check against) plus the key
/// material itself. `private_pkcs8` never leaves the issuing process in cleartext form beyond
/// whatever the caller does with it (e.g. `metap_control::SecretStore`, per the platform design
/// doc) — only `public_x` (via [`Jwk`]/[`JwkSet`]) is ever meant to be published.
pub struct JwksKeyPair {
    pub kid: String,
    private_pkcs8: Vec<u8>,
    public_x: String,
}

impl JwksKeyPair {
    /// Generates a fresh Ed25519 keypair with the given `kid`. Callers mint a new `kid` per
    /// rotation (a UUID or a timestamp-derived string both work — this crate doesn't prescribe
    /// one, since `kid` uniqueness is the only real constraint).
    pub fn generate(kid: impl Into<String>) -> anyhow::Result<Self> {
        let rng = ring::rand::SystemRandom::new();
        let pkcs8 =
            Ed25519KeyPair::generate_pkcs8(&rng).map_err(|e| anyhow::anyhow!("failed to generate Ed25519 key: {e}"))?;
        let keypair = Ed25519KeyPair::from_pkcs8(pkcs8.as_ref())
            .map_err(|e| anyhow::anyhow!("failed to parse freshly generated Ed25519 key: {e}"))?;
        let public_x = B64.encode(keypair.public_key().as_ref());
        Ok(Self {
            kid: kid.into(),
            private_pkcs8: pkcs8.as_ref().to_vec(),
            public_x,
        })
    }

    /// Reconstructs a keypair from previously generated PKCS8 DER bytes (e.g. read back from a
    /// `SecretStore`) — the counterpart to `generate` for a process that didn't mint this key
    /// itself but needs to sign with it (or just re-derive `public_x`).
    pub fn from_pkcs8(kid: impl Into<String>, private_pkcs8: Vec<u8>) -> anyhow::Result<Self> {
        let keypair =
            Ed25519KeyPair::from_pkcs8(&private_pkcs8).map_err(|e| anyhow::anyhow!("invalid PKCS8 key: {e}"))?;
        let public_x = B64.encode(keypair.public_key().as_ref());
        Ok(Self {
            kid: kid.into(),
            private_pkcs8,
            public_x,
        })
    }

    pub fn private_pkcs8(&self) -> &[u8] {
        &self.private_pkcs8
    }

    pub fn encoding_key(&self) -> EncodingKey {
        EncodingKey::from_ed_der(&self.private_pkcs8)
    }

    pub fn decoding_key(&self) -> anyhow::Result<DecodingKey> {
        Ok(DecodingKey::from_ed_components(&self.public_x)?)
    }

    pub fn jwk(&self) -> Jwk {
        Jwk {
            kty: "OKP".to_string(),
            crv: "Ed25519".to_string(),
            kid: self.kid.clone(),
            use_: "sig".to_string(),
            alg: "EdDSA".to_string(),
            x: self.public_x.clone(),
        }
    }
}

/// One entry of a JWKS document (RFC 7517) — only the OKP/Ed25519 shape this crate ever mints,
/// not a general-purpose JWK type (no RSA/EC variant needed here, see this module's doc comment
/// for why RS256 stays out of scope for this crate).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Jwk {
    pub kty: String,
    pub crv: String,
    pub kid: String,
    #[serde(rename = "use")]
    pub use_: String,
    pub alg: String,
    pub x: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct JwkSet {
    pub keys: Vec<Jwk>,
}

/// Holds every key a verifier should currently trust, plus which one new tokens actually get
/// signed with — the "keep 2-3 active keys" rotation model
/// (`docs/architectures/...` — see the plan this crate implements): a key is published (added
/// here, appears in [`JwkSet`]) before it's ever used to sign anything, so every verifier's next
/// JWKS refresh already has it cached by the time it needs to; conversely a retired key stays
/// published for a grace window after signing stops, so a token minted right before rotation
/// (with the longest TTL) still verifies. This type only holds the in-memory rotation state —
/// where the keys themselves persist across restarts (a `SecretStore`, a database row, ...) is
/// the issuing binary's own decision, not this crate's.
pub struct JwksKeyStore {
    keys: Vec<JwksKeyPair>,
    signing_kid: String,
}

impl JwksKeyStore {
    /// Starts a store with exactly one key, immediately both published and signing — the
    /// bootstrap case (a brand new issuer with no rotation history yet).
    pub fn new(signing_key: JwksKeyPair) -> Self {
        let signing_kid = signing_key.kid.clone();
        Self {
            keys: vec![signing_key],
            signing_kid,
        }
    }

    /// Step 1 of rotation: publish a new key's public half without signing anything with it yet.
    /// Safe to call at any time — an unused published key is inert.
    pub fn add_key(&mut self, key: JwksKeyPair) {
        self.keys.push(key);
    }

    /// Step 2 of rotation: switch signing to a key that's already published. Errors if `kid`
    /// isn't in the published set — call [`Self::add_key`] first and let at least one JWKS
    /// refresh cycle elapse (so every verifier has it cached) before promoting.
    pub fn promote(&mut self, kid: &str) -> anyhow::Result<()> {
        if !self.keys.iter().any(|k| k.kid == kid) {
            anyhow::bail!("cannot promote {kid}: not in the published key set yet");
        }
        self.signing_kid = kid.to_string();
        Ok(())
    }

    /// Step 3 of rotation: stop publishing a retired key. Call only after the grace window
    /// (`max_token_ttl + jwks_ttl`, per the rotation protocol this store implements) has passed
    /// since the key stopped signing — removing it earlier would break verification for any
    /// still-live token it signed. Refuses to remove the currently-signing key (rotate off it
    /// with [`Self::promote`] first) since that would leave the store unable to sign anything.
    pub fn remove_key(&mut self, kid: &str) -> anyhow::Result<()> {
        if kid == self.signing_kid {
            anyhow::bail!("cannot remove {kid}: it is the currently signing key — promote another key first");
        }
        self.keys.retain(|k| k.kid != kid);
        Ok(())
    }

    pub fn signing_key(&self) -> &JwksKeyPair {
        self.keys
            .iter()
            .find(|k| k.kid == self.signing_kid)
            .expect("signing_kid always points at a key present in `keys`")
    }

    pub fn jwk_set(&self) -> JwkSet {
        JwkSet {
            keys: self.keys.iter().map(JwksKeyPair::jwk).collect(),
        }
    }
}
