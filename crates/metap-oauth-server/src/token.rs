//! Opaque bearer-credential generation/hashing shared by client secrets, authorization codes and
//! refresh tokens — one implementation so the three can't drift on entropy source or digest
//! choice.

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use sha2::{Digest, Sha256};

/// 256 bits of randomness (two concatenated `Uuid::new_v4`s, each backed by the OS RNG via the
/// `uuid` crate's own `v4` feature — no new RNG dependency needed) base64url-encoded into an
/// opaque, URL-safe string. Used for a client secret, an authorization code, and a refresh
/// token — all server-generated, never user-chosen, which is what makes the fast digest in
/// [`hash_token`] the right choice below instead of a slow password hash.
pub fn generate_opaque_token() -> String {
    let mut bytes = [0u8; 32];
    bytes[..16].copy_from_slice(uuid::Uuid::new_v4().as_bytes());
    bytes[16..].copy_from_slice(uuid::Uuid::new_v4().as_bytes());
    URL_SAFE_NO_PAD.encode(bytes)
}

/// SHA-256 hex digest — deliberately not argon2/bcrypt. Every caller of this hashes a
/// server-generated, high-entropy (256-bit) opaque token, never a human-chosen password, so the
/// offline-brute-force threat a slow password hash defends against doesn't apply here; a fast
/// digest is the correct and standard choice for this class of secret (the same reasoning most
/// API-key/OAuth implementations use), and matters because `POST /oauth/token` looks one of
/// these up on every single call, not just at login.
pub fn hash_token(raw: &str) -> String {
    let digest = Sha256::digest(raw.as_bytes());
    hex::encode(digest)
}

// Tiny local hex encoder rather than pulling in the `hex` crate for one function — this is the
// only place in this crate that needs it.
mod hex {
    pub fn encode(bytes: impl AsRef<[u8]>) -> String {
        use std::fmt::Write;
        bytes.as_ref().iter().fold(String::new(), |mut s, b| {
            let _ = write!(s, "{b:02x}");
            s
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generated_tokens_are_unique_and_url_safe() {
        let a = generate_opaque_token();
        let b = generate_opaque_token();
        assert_ne!(a, b);
        assert!(a.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'));
        // 32 raw bytes, base64url no-pad -> 43 chars.
        assert_eq!(a.len(), 43);
    }

    #[test]
    fn hash_is_deterministic_and_hex() {
        let raw = "a-fixed-test-token";
        let h1 = hash_token(raw);
        let h2 = hash_token(raw);
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 64);
        assert!(h1.chars().all(|c| c.is_ascii_hexdigit()));
        assert_ne!(hash_token("other"), h1);
    }
}
