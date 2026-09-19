//! metap as an OAuth2 Authorization Server (RFC 6749) — issues tokens to a **third-party
//! client** acting on behalf of a tenant's own user, distinct from `metap-auth` (how that user
//! logs *into* metap in the first place; `AuthProviderKind::OAuth2` there is the unrelated
//! "log in via an external OAuth2 IdP" direction). No HTTP, no business-entity knowledge — a
//! plain library, same shape as `metap-cron`/`metap-dashboards`; `crates/metap-http/src/routes/
//! oauth2.rs` is the HTTP surface built on this.
//!
//! **Scope shipped**: `authorization_code` (+ mandatory-for-public-clients PKCE, S256 only),
//! `refresh_token`, and (2026-09-19) `client_credentials` grants. The last mints a token *as* the
//! `oauth_clients.service_user_id` row `metap-http`'s `POST /admin/oauth/clients` provisions
//! eagerly for every new client (via `metap-auth`, a dependency this crate deliberately doesn't
//! take on itself — see `CreateClientInput::service_user_id`'s own doc comment) — confidential
//! clients only (RFC 6749 §4.4), no refresh token is issued for it (§4.4.3), and a client
//! registered before this existed has no service user and can't use the grant until re-registered.
//!
//! **Access tokens are ordinary platform JWTs**, minted through the exact same trust root
//! (`metap_peripherals::mint_jwt` / `metap-jwks`) every other session token uses — so
//! `metap-http`'s existing `AuthContext` verifies one with zero new decode path, and RBAC/ABAC
//! governs what the token can actually do exactly as it would for the user's own login session.
//! The granted `scope` and `client_id` ride as two new optional JWT claims (additive — an
//! ordinary session token simply omits them) and are folded into the resolved
//! `RequestContext.context_attributes` by `AuthContext`, so a tenant that wants scope-gated
//! policies can write an ABAC condition against `fromContext.oauthScope` today; nothing in this
//! crate or `metap-permission`'s engine enforces scope automatically against `/api/:entity` on
//! its own — that's a product decision for whoever operates a given deployment, flagged rather
//! than decided here, same "don't resolve unilaterally" convention this repo already follows for
//! open questions.
//!
//! **Revocation is real for refresh tokens, not for an already-issued access token** — consistent
//! with `mint_jwt`'s own doc comment ("no revocation-checking infrastructure... a deliberate
//! scope choice"). `POST /oauth/revoke` invalidates the refresh token that would mint the *next*
//! access token; the current one remains valid until its own short `exp`. Refresh tokens rotate
//! on every use and detect reuse (`consume_refresh_token`'s doc comment) — presenting an
//! already-consumed refresh token revokes the rest of that (client, user) pair's live chain,
//! since that shape only happens if a token was stolen and used by two parties.

mod cleanup;
mod client;
mod code;
mod consent;
mod pkce;
mod refresh;
mod token;

pub use cleanup::{delete_expired as delete_expired_tokens, run as run_cleanup, CleanupCounts};
pub use client::{
    create_client, get_client_by_client_id, list_clients, revoke_client, verify_client_secret, ClientWithSecret,
    CreateClientInput, OAuthClient,
};
pub use code::{consume_authorization_code, create_authorization_code, AuthorizationCode, CreateCodeInput};
pub use consent::{
    consume_pending_authorization, create_pending_authorization, get_consent_scope, get_pending_authorization,
    record_consent, CreatePendingAuthorizationInput, PendingAuthorization,
};
pub use pkce::verify_pkce;
pub use refresh::{
    consume_refresh_token, create_refresh_token, revoke_refresh_token, ConsumeRefreshOutcome, CreateRefreshInput,
    RefreshToken,
};
pub use token::{generate_opaque_token, hash_token};

/// Space-separated scope strings, RFC 6749 §3.3 — the wire format every `scope` column/claim/
/// query-param in this crate and its HTTP surface uses. Not a `Vec<String>` newtype: every
/// caller either splits it once (to check subset/membership) or passes it straight through
/// unopened (storage, the JWT claim), so a plain `&str`/`String` avoids a wrapper type that
/// would need `Display`/`FromStr` glue for no real benefit.
pub fn scope_tokens(scope: &str) -> Vec<&str> {
    scope.split_whitespace().collect()
}

/// `true` iff every token in `requested` also appears in `allowed` — the one rule
/// `POST /oauth/authorize` and `POST /oauth/token`'s (`refresh_token` grant's optional narrower
/// `scope` param) both apply. Order-independent, duplicate-tolerant (a requested scope repeated
/// twice is still a subset check, not a multiset one — nothing here needs to reject a client for
/// asking twice).
pub fn scope_is_subset(requested: &str, allowed: &str) -> bool {
    let allowed_tokens = scope_tokens(allowed);
    scope_tokens(requested).iter().all(|t| allowed_tokens.contains(t))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scope_subset_accepts_equal_or_narrower() {
        assert!(scope_is_subset("read:zones", "read:zones write:zones"));
        assert!(scope_is_subset("read:zones write:zones", "write:zones read:zones"));
        assert!(scope_is_subset("", "read:zones"));
        assert!(scope_is_subset("", ""));
    }

    #[test]
    fn scope_subset_rejects_anything_not_granted() {
        assert!(!scope_is_subset("read:zones write:zones", "read:zones"));
        assert!(!scope_is_subset("delete:zones", "read:zones write:zones"));
    }
}
