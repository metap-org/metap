//! PKCE (RFC 7636), S256 only — plain `code_challenge_method=plain` is not supported, since it
//! offers no protection against an authorization code interception attack on a device that can
//! read another app's redirect (the exact threat PKCE exists for).

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use sha2::{Digest, Sha256};

/// `true` iff `code_verifier` hashes (SHA-256, base64url no-pad) to `code_challenge` — RFC 7636
/// §4.6's `S256` check. A `(None, None)` pair (no PKCE on this authorization request) is `true`
/// by construction of the caller: this function is only ever called when a challenge was stored,
/// so an absent verifier against a present challenge is always a caller error, not something
/// this function decides — see `code::consume_authorization_code`'s own PKCE branch.
pub fn verify_pkce(code_verifier: &str, code_challenge: &str) -> bool {
    let digest = Sha256::digest(code_verifier.as_bytes());
    URL_SAFE_NO_PAD.encode(digest) == code_challenge
}

#[cfg(test)]
mod tests {
    use super::*;

    // RFC 7636 Appendix B's worked example.
    const VERIFIER: &str = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
    const CHALLENGE: &str = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM";

    #[test]
    fn matches_the_rfc_worked_example() {
        assert!(verify_pkce(VERIFIER, CHALLENGE));
    }

    #[test]
    fn rejects_a_wrong_verifier() {
        assert!(!verify_pkce("wrong-verifier", CHALLENGE));
    }
}
