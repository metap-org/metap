//! Local username/password auth (`docs/roadmap.md` Phase 15) — the first thing in this repo
//! that stores a credential; everything else (`AuthContext`) only ever verified a bearer JWT
//! someone else minted. Two responsibilities kept together because they're both "identity",
//! not because they're logically inseparable:
//!
//! - `create_user`/`verify_credentials`: the `users` table (argon2id password hashing).
//! - `mint_jwt`: the *only* JWT-encoding implementation in the repo — both `dev-tools
//!   mint-token` and `crates/metap-http`'s `POST /auth/login` handler call this instead of
//!   each hand-rolling `jsonwebtoken::encode` with their own `Claims` struct, so the two
//!   paths can't drift on claim shape (`sub`/`tenantId`/`exp` — see `crates/metap-http/src/auth.rs`'s
//!   `Claims` on the decode side, which this must keep matching).

use std::sync::OnceLock;

use argon2::password_hash::rand_core::OsRng;
use argon2::password_hash::{PasswordHash, PasswordHasher, PasswordVerifier, SaltString};
use argon2::Argon2;
use jsonwebtoken::{decode, encode, Algorithm, DecodingKey, EncodingKey, Header, Validation};
use serde::{Deserialize, Serialize};
use sqlx::{PgExecutor, Row};
use uuid::Uuid;

/// `iss`/`aud` claim values every token this repo mints carries, and the only values
/// `crates/metap-http/src/auth.rs`'s `Validation` accepts on verify — that crate imports these
/// same constants rather than duplicating the literals, so mint and verify can't drift.
pub const JWT_ISSUER: &str = "metap";
pub const JWT_AUDIENCE: &str = "metap-api";

#[derive(Debug, Clone)]
pub struct AuthUser {
    pub id: Uuid,
    pub tenant_id: Uuid,
    pub email: String,
}

fn hash_password(password: &str) -> anyhow::Result<String> {
    let salt = SaltString::generate(&mut OsRng);
    Argon2::default()
        .hash_password(password.as_bytes(), &salt)
        .map(|hash| hash.to_string())
        .map_err(|e| anyhow::anyhow!("failed to hash password: {e}"))
}

fn verify_password(password: &str, hash: &str) -> anyhow::Result<bool> {
    let parsed = PasswordHash::new(hash).map_err(|e| anyhow::anyhow!("invalid stored password hash: {e}"))?;
    Ok(Argon2::default().verify_password(password.as_bytes(), &parsed).is_ok())
}

/// A precomputed hash `verify_credentials` checks against when no user matches — so "email
/// doesn't exist" and "email exists, wrong password" both pay the same (deliberately slow)
/// argon2 verify cost instead of the former returning near-instantly, which would otherwise
/// let a caller enumerate registered emails by timing.
fn dummy_hash() -> &'static str {
    static HASH: OnceLock<String> = OnceLock::new();
    HASH.get_or_init(|| hash_password("dummy-password-for-timing-safety").expect("hashing a fixed string cannot fail"))
}

pub async fn create_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    email: &str,
    password: &str,
) -> anyhow::Result<AuthUser> {
    let password_hash = hash_password(password)?;
    let row = sqlx::query(
        "INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, $3) \
         RETURNING id, tenant_id, email",
    )
    .bind(tenant_id)
    .bind(email)
    .bind(password_hash)
    .fetch_one(executor)
    .await?;
    Ok(AuthUser {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        email: row.try_get("email")?,
    })
}

/// One user by id, scoped to the tenant so an id from another tenant can never resolve. Backs
/// `GET /auth/me`'s `email` field (2026-09-03): the JWT carries only `sub` (a user id), so the
/// frontend used to recover its own email by pulling the *entire* tenant user list via
/// `list_tenant_users` below and searching it client-side — fine for a handful of users, a real
/// waste for a large tenant, and it ran on every page
/// (`platform-ui/docs/audits/02-auth-permission-workflow-diagram-audit.md` finding B8).
pub async fn find_user_by_id<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    user_id: Uuid,
) -> anyhow::Result<Option<AuthUser>> {
    let row = sqlx::query("SELECT id, tenant_id, email FROM users WHERE id = $1 AND tenant_id = $2")
        .bind(user_id)
        .bind(tenant_id)
        .fetch_optional(executor)
        .await?;
    row.map(|row| {
        Ok(AuthUser {
            id: row.try_get("id")?,
            tenant_id: row.try_get("tenant_id")?,
            email: row.try_get("email")?,
        })
    })
    .transpose()
}

/// Every user in the tenant, id+email only — the "pick a user" primitive an app-level assignee/
/// reporter picker needs (`GET /users`, `crates/metap-http/src/routes/users.rs`), distinct from
/// `role_assignment::list_users` (which returns role assignments for `/admin/users`, an admin-
/// only concern). Any authenticated user can call the route this backs — picking who to assign
/// an issue to isn't an admin action, unlike granting a role.
pub async fn list_tenant_users<'e>(executor: impl PgExecutor<'e>, tenant_id: Uuid) -> anyhow::Result<Vec<AuthUser>> {
    let rows = sqlx::query("SELECT id, tenant_id, email FROM users WHERE tenant_id = $1 ORDER BY email")
        .bind(tenant_id)
        .fetch_all(executor)
        .await?;
    rows.into_iter()
        .map(|row| {
            Ok(AuthUser {
                id: row.try_get("id")?,
                tenant_id: row.try_get("tenant_id")?,
                email: row.try_get("email")?,
            })
        })
        .collect()
}

/// `Ok(None)` for either "no user with this email" or "wrong password" — deliberately not
/// distinguished, so a caller can't use this to enumerate registered emails.
///
/// Generic over `executor` (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20) so
/// `POST /auth/login` can run this against a `Router::begin(tenantId)`-opened transaction when
/// the caller supplies a `tenantId` — required for a `DedicatedDb`-strategy tenant, whose
/// `users` table lives only in that tenant's own database, never in the shared control-plane
/// pool this used to be pinned to.
///
/// **Looks up `email` globally, not `(tenant_id, email)` (audit 04 finding A#2, confirmed
/// intentional 2026-09-13, not a gap to close).** `crates/migrations/0009_users.sql`'s
/// `users_email_unique` index is a plain global unique index for the same reason: `POST
/// /auth/login` has no tenant picker — a user types only email+password, and this lookup is what
/// resolves which tenant they belong to. Scoping the index (and this query) to
/// `(tenant_id, email)` would let the same email exist in two tenants, which sounds like a
/// feature until this login flow is asked "which tenant do you mean" with no UI that currently
/// asks it — that would be a real product change (a tenant-picker step, or a per-tenant login
/// subdomain), not a query tweak. Revisit if/when that UI exists; until then, one email means one
/// account platform-wide, by design.
pub async fn verify_credentials<'e>(
    executor: impl PgExecutor<'e>,
    email: &str,
    password: &str,
) -> anyhow::Result<Option<AuthUser>> {
    let row = sqlx::query("SELECT id, tenant_id, email, password_hash FROM users WHERE email = $1")
        .bind(email)
        .fetch_optional(executor)
        .await?;

    let Some(row) = row else {
        let _ = verify_password(password, dummy_hash());
        return Ok(None);
    };

    let stored_hash: String = row.try_get("password_hash")?;
    if !verify_password(password, &stored_hash)? {
        return Ok(None);
    }

    Ok(Some(AuthUser {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        email: row.try_get("email")?,
    }))
}

#[derive(Serialize)]
struct Claims {
    sub: String,
    #[serde(rename = "tenantId")]
    tenant_id: String,
    exp: usize,
    iss: String,
    aud: String,
    jti: String,
    /// Set only by [`mint_oauth_access_token`] — an ordinary session token never carries this,
    /// same "absent, not null" optionality as `metap-jwks::mint`'s `functionId`.
    #[serde(skip_serializing_if = "Option::is_none")]
    scope: Option<String>,
    #[serde(rename = "clientId", skip_serializing_if = "Option::is_none")]
    client_id: Option<String>,
}

/// Mints an RS256 JWT with the exact claim shape `crates/metap-http/src/auth.rs`'s
/// `AuthContext` extractor decodes (`sub`, `tenantId`, `exp` — no `functionId`, which that
/// extractor already treats as optional). `private_key_pem` is the raw PEM text, not a
/// pre-parsed key, so callers (both of which mint infrequently — an interactive CLI command
/// and a login request) don't need to hold a `jsonwebtoken::EncodingKey` in state just for
/// this.
///
/// **`jti` (audit 04 finding A#8, added 2026-09-13):** a random per-token id, cheap and
/// forward-compatible, added ahead of any consumer of it — there is no revocation-checking
/// infrastructure in this platform today (no denylist table, no lookup on decode), and building
/// one speculatively without a real "log out this device"/"revoke this session" feature driving
/// its shape would be exactly the premature abstraction this codebase avoids elsewhere. Having
/// every token already carry a stable identity for that lookup means a future revocation feature
/// is a purely additive change (a new table plus one check in `decode_access_token`), not a
/// breaking claim-shape migration touching every already-issued token.
pub fn mint_jwt(private_key_pem: &str, tenant_id: Uuid, user_id: Uuid, ttl_seconds: u64) -> anyhow::Result<String> {
    mint_claims(private_key_pem, tenant_id, user_id, ttl_seconds, None, None)
}

/// The OAuth2-authorization-server counterpart to [`mint_jwt`] — same trust root, same claim
/// shape plus `scope`/`clientId` (`crates/metap-oauth-server`'s doc comment has the full
/// reasoning: reusing this trust root means `crates/metap-http/src/auth.rs`'s `AuthContext`
/// verifies a token minted here with zero new decode path). A separate function rather than two
/// new parameters on [`mint_jwt`] itself — that signature is called from `dev-tools`, 3 routes in
/// `metap-http`, and every downstream binary's own scaffolding; adding parameters there would be
/// a breaking change to all of them for a capability only the OAuth2 token endpoint needs.
pub fn mint_oauth_access_token(
    private_key_pem: &str,
    tenant_id: Uuid,
    user_id: Uuid,
    ttl_seconds: u64,
    scope: &str,
    client_id: &str,
) -> anyhow::Result<String> {
    mint_claims(
        private_key_pem,
        tenant_id,
        user_id,
        ttl_seconds,
        Some(scope.to_string()),
        Some(client_id.to_string()),
    )
}

fn mint_claims(
    private_key_pem: &str,
    tenant_id: Uuid,
    user_id: Uuid,
    ttl_seconds: u64,
    scope: Option<String>,
    client_id: Option<String>,
) -> anyhow::Result<String> {
    let key = EncodingKey::from_rsa_pem(private_key_pem.as_bytes())?;
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?;
    let claims = Claims {
        sub: user_id.to_string(),
        tenant_id: tenant_id.to_string(),
        exp: (now.as_secs() + ttl_seconds) as usize,
        iss: JWT_ISSUER.to_string(),
        aud: JWT_AUDIENCE.to_string(),
        jti: Uuid::new_v4().to_string(),
        scope,
        client_id,
    };
    Ok(encode(&Header::new(Algorithm::RS256), &claims, &key)?)
}

/// The decode-side counterpart to `mint_jwt`'s claim shape. Deliberately omits `exp`/`iss`/`aud`
/// — `jsonwebtoken::decode` validates those against the raw token payload internally (see
/// `jsonwebtoken::validation::validate`), independent of which fields the target `Deserialize`
/// struct declares, so there is no need to carry them through to callers that never read them
/// again after a successful decode. `jti` is kept (unlike those 3) since a future revocation
/// check — the reason `mint_jwt` added it, see that function's doc comment — would need to read
/// it back out on every decode; no caller consults it today. `Option` (not `String`, unlike
/// `mint_jwt`'s always-set side) so a token minted before `jti` existed still decodes — this
/// platform has no forced-reauth step, so an already-issued, still-unexpired token must keep
/// working across this deploy.
#[derive(Debug, Deserialize)]
pub struct AccessClaims {
    pub sub: String,
    #[serde(rename = "tenantId")]
    pub tenant_id: String,
    #[serde(rename = "functionId")]
    pub function_id: Option<String>,
    pub jti: Option<String>,
    /// Present only on a token minted by [`mint_oauth_access_token`] — `crates/metap-http/src/
    /// auth.rs`'s `AuthContext` folds this into the resolved `RequestContext.context_attributes`
    /// when set. `Option` for the same reason `function_id`/`jti` are: every token minted before
    /// this claim existed still decodes.
    pub scope: Option<String>,
    #[serde(rename = "clientId")]
    pub client_id: Option<String>,
}

/// Verifies a Bearer access token minted by `mint_jwt`: RS256, audience/issuer pinned to
/// `JWT_AUDIENCE`/`JWT_ISSUER`, `leeway` seconds of clock-skew tolerance. Pure — no DB, no role
/// lookup, no `RequestContext` construction (that needs a tenant-scoped `Router`, which this
/// crate deliberately doesn't depend on to avoid a cycle with `metap-control`; see
/// `metap_control::resolve_request_context` for the rest of the pipeline). The single place JWT
/// verification logic lives, so every transport that accepts this platform's bearer tokens
/// (`crates/metap-http/src/auth.rs`'s `AuthContext` today, a future gRPC auth interceptor) calls
/// this instead of re-implementing `Validation`/`Algorithm` setup.
pub fn decode_access_token(
    token: &str,
    decoding_key: &DecodingKey,
    leeway: u64,
) -> Result<AccessClaims, jsonwebtoken::errors::Error> {
    let mut validation = Validation::new(Algorithm::RS256);
    validation.set_audience(&[JWT_AUDIENCE]);
    validation.set_issuer(&[JWT_ISSUER]);
    validation.leeway = leeway;
    decode::<AccessClaims>(token, decoding_key, &validation).map(|data| data.claims)
}
