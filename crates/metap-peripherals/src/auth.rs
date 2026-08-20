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
use jsonwebtoken::{encode, Algorithm, EncodingKey, Header};
use serde::Serialize;
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

/// `Ok(None)` for either "no user with this email" or "wrong password" — deliberately not
/// distinguished, so a caller can't use this to enumerate registered emails.
///
/// Generic over `executor` (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20) so
/// `POST /auth/login` can run this against a `Router::begin(tenantId)`-opened transaction when
/// the caller supplies a `tenantId` — required for a `DedicatedDb`-strategy tenant, whose
/// `users` table lives only in that tenant's own database, never in the shared control-plane
/// pool this used to be pinned to.
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
}

/// Mints an RS256 JWT with the exact claim shape `crates/metap-http/src/auth.rs`'s
/// `AuthContext` extractor decodes (`sub`, `tenantId`, `exp` — no `functionId`, which that
/// extractor already treats as optional). `private_key_pem` is the raw PEM text, not a
/// pre-parsed key, so callers (both of which mint infrequently — an interactive CLI command
/// and a login request) don't need to hold a `jsonwebtoken::EncodingKey` in state just for
/// this.
pub fn mint_jwt(private_key_pem: &str, tenant_id: Uuid, user_id: Uuid, ttl_seconds: u64) -> anyhow::Result<String> {
    let key = EncodingKey::from_rsa_pem(private_key_pem.as_bytes())?;
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?;
    let claims = Claims {
        sub: user_id.to_string(),
        tenant_id: tenant_id.to_string(),
        exp: (now.as_secs() + ttl_seconds) as usize,
        iss: JWT_ISSUER.to_string(),
        aud: JWT_AUDIENCE.to_string(),
    };
    Ok(encode(&Header::new(Algorithm::RS256), &claims, &key)?)
}
