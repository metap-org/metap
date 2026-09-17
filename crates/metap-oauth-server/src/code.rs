use chrono::{DateTime, Utc};
use sqlx::{PgExecutor, Row};
use uuid::Uuid;

use crate::token::{generate_opaque_token, hash_token};

/// RFC 6749 doesn't mandate a lifetime; 60s is the value most real IdPs converge on — long
/// enough for a browser redirect round trip, short enough that a code leaked into a server log
/// or `Referer` header is worthless by the time anyone could act on it.
pub const AUTHORIZATION_CODE_TTL_SECONDS: i64 = 60;

pub struct AuthorizationCode {
    pub id: Uuid,
    pub client_id: Uuid,
    pub tenant_id: Uuid,
    pub user_id: Uuid,
    pub redirect_uri: String,
    pub scope: String,
    pub code_challenge: Option<String>,
    pub code_challenge_method: Option<String>,
}

pub struct CreateCodeInput {
    pub client_id: Uuid,
    pub tenant_id: Uuid,
    pub user_id: Uuid,
    pub redirect_uri: String,
    pub scope: String,
    pub code_challenge: Option<String>,
    pub code_challenge_method: Option<String>,
}

fn row_to_code(row: &sqlx::postgres::PgRow) -> Result<AuthorizationCode, sqlx::Error> {
    Ok(AuthorizationCode {
        id: row.try_get("id")?,
        client_id: row.try_get("client_id")?,
        tenant_id: row.try_get("tenant_id")?,
        user_id: row.try_get("user_id")?,
        redirect_uri: row.try_get("redirect_uri")?,
        scope: row.try_get("scope")?,
        code_challenge: row.try_get("code_challenge")?,
        code_challenge_method: row.try_get("code_challenge_method")?,
    })
}

/// Issued by `GET /oauth/authorize` once the caller (an already-authenticated `AuthContext`
/// session) is treated as approving the client's request — see this crate's own doc comment for
/// why there's no separate interactive consent step yet. Returns `(row, raw_code)`: `raw_code`
/// is what actually goes on the redirect URL, never persisted — only its hash is.
pub async fn create_authorization_code<'e>(
    executor: impl PgExecutor<'e>,
    input: CreateCodeInput,
) -> anyhow::Result<(AuthorizationCode, String)> {
    let raw_code = generate_opaque_token();
    let expires_at: DateTime<Utc> = Utc::now() + chrono::Duration::seconds(AUTHORIZATION_CODE_TTL_SECONDS);

    let row = sqlx::query(
        "INSERT INTO oauth_authorization_codes \
            (code_hash, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, \
             code_challenge_method, expires_at) \
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) \
         RETURNING id, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method",
    )
    .bind(hash_token(&raw_code))
    .bind(input.client_id)
    .bind(input.tenant_id)
    .bind(input.user_id)
    .bind(&input.redirect_uri)
    .bind(&input.scope)
    .bind(&input.code_challenge)
    .bind(&input.code_challenge_method)
    .bind(expires_at)
    .fetch_one(executor)
    .await?;

    Ok((row_to_code(&row)?, raw_code))
}

/// Atomically marks the code used and returns it — `used_at IS NULL AND expires_at > now()` in
/// the same `UPDATE` that sets `used_at`, so two concurrent redemption attempts (a real replay,
/// or a client retrying a timed-out request) can never both succeed: exactly one `UPDATE`
/// matches the row, the other sees `used_at IS NULL` already false.
///
/// `redirect_uri` is checked here too (RFC 6749 §4.1.3: the token request's `redirect_uri` must
/// exactly match the one used to obtain the code) — folded into the same atomic query rather
/// than compared after the fact, so a mismatched redirect can't itself burn a code that a
/// legitimate retry with the *correct* `redirect_uri` might otherwise still need.
///
/// PKCE verification is **not** done here — this only returns the stored `code_challenge`/
/// `code_challenge_method` for the caller (`metap-http`) to check against a presented
/// `code_verifier` via [`crate::verify_pkce`]. The code is already consumed by the time that
/// check runs, which is correct: a code is single-use regardless of whether the PKCE check that
/// follows passes.
pub async fn consume_authorization_code<'e>(
    executor: impl PgExecutor<'e>,
    raw_code: &str,
    client_id: Uuid,
    redirect_uri: &str,
) -> anyhow::Result<Option<AuthorizationCode>> {
    let row = sqlx::query(
        "UPDATE oauth_authorization_codes SET used_at = now() \
         WHERE code_hash = $1 AND client_id = $2 AND redirect_uri = $3 \
           AND used_at IS NULL AND expires_at > now() \
         RETURNING id, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method",
    )
    .bind(hash_token(raw_code))
    .bind(client_id)
    .bind(redirect_uri)
    .fetch_optional(executor)
    .await?;
    row.as_ref().map(row_to_code).transpose().map_err(anyhow::Error::from)
}
