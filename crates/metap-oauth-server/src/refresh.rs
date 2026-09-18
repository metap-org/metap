use chrono::{DateTime, Utc};
use sqlx::{PgExecutor, Postgres, Row, Transaction};
use uuid::Uuid;

use crate::token::{generate_opaque_token, hash_token};

/// 30 days — no config surface for this in v1 (`../metap-docs/docs/roadmap/88-oauth2-authorization-server.md` notes it as a
/// follow-up alongside `client_credentials`), long enough that a real integration doesn't need
/// to re-run the authorization-code dance constantly, short enough that a refresh token that's
/// simply never used again eventually stops being a standing credential.
pub const REFRESH_TOKEN_TTL_SECONDS: i64 = 30 * 24 * 3600;

pub struct RefreshToken {
    pub id: Uuid,
    pub client_id: Uuid,
    pub tenant_id: Uuid,
    pub user_id: Uuid,
    pub scope: String,
}

pub struct CreateRefreshInput {
    pub client_id: Uuid,
    pub tenant_id: Uuid,
    pub user_id: Uuid,
    pub scope: String,
}

/// What presenting a refresh token to `POST /oauth/token`'s `refresh_token` grant resolved to.
pub enum ConsumeRefreshOutcome {
    /// The token was live; it's now revoked and replaced by the returned fresh one
    /// (`(record, raw_token)`) — the caller mints a new access token from `record`'s fields and
    /// returns `raw_token` to the client as the new `refresh_token`.
    Rotated(RefreshToken, String),
    /// No matching, unrevoked, unexpired row for this `(token, client_id)` — a wrong/garbage
    /// token, a client mismatch, or one that's simply expired. The caller responds
    /// `invalid_grant`; deliberately not distinguished further, same reasoning
    /// `verify_credentials` gives for not telling a caller *why* a login failed.
    Invalid,
    /// The presented token was already consumed by an earlier rotation — only possible if it
    /// was captured and replayed by a second party after the legitimate holder already rotated
    /// past it (or a legitimate caller retried after losing the rotated response, an accepted
    /// false-positive this crate treats identically, matching standard refresh-token-rotation
    /// guidance: fail closed rather than try to distinguish the two). The entire live chain for
    /// this `(client_id, user_id)` has already been revoked by the time this variant is
    /// returned — see this function's doc comment.
    Reused,
}

/// Issued right after a successful `authorization_code` exchange, and again on every
/// [`consume_refresh_token`] rotation — never any other time.
pub async fn create_refresh_token(
    tx: &mut Transaction<'_, Postgres>,
    input: CreateRefreshInput,
) -> anyhow::Result<(RefreshToken, String)> {
    let raw_token = generate_opaque_token();
    let expires_at: DateTime<Utc> = Utc::now() + chrono::Duration::seconds(REFRESH_TOKEN_TTL_SECONDS);

    let row = sqlx::query(
        "INSERT INTO oauth_refresh_tokens (token_hash, client_id, tenant_id, user_id, scope, expires_at) \
         VALUES ($1, $2, $3, $4, $5, $6) \
         RETURNING id, client_id, tenant_id, user_id, scope",
    )
    .bind(hash_token(&raw_token))
    .bind(input.client_id)
    .bind(input.tenant_id)
    .bind(input.user_id)
    .bind(&input.scope)
    .bind(expires_at)
    .fetch_one(&mut **tx)
    .await?;

    Ok((
        RefreshToken {
            id: row.try_get("id")?,
            client_id: row.try_get("client_id")?,
            tenant_id: row.try_get("tenant_id")?,
            user_id: row.try_get("user_id")?,
            scope: row.try_get("scope")?,
        },
        raw_token,
    ))
}

/// Takes an open transaction (not a generic `PgExecutor`) because rotation genuinely needs
/// multiple statements to succeed or fail together: a caller who fails to mint the new access
/// token after this returns `Rotated` must roll the whole thing back, or the client would be
/// left holding a rotated refresh token with no access token to show for it. The caller
/// (`crates/metap-http/src/routes/oauth2.rs`) commits only after successfully minting the new
/// access token JWT.
///
/// **Reuse detection**: a presented token whose row is already `revoked_at IS NOT NULL` did not
/// come from the legitimate holder's last response (they'd have the *new* token from that
/// rotation, not this one) — this is exactly the signature of a stolen-and-replayed refresh
/// token. Every other live (unrevoked, unexpired) token for this `(client_id, user_id)` pair is
/// revoked immediately, cutting off whichever party — attacker or legitimate holder, this
/// function can't tell which — is still holding a token from the same compromised chain. A real
/// integration should treat `Reused` as "re-run the authorization_code flow from scratch", not
/// retry the refresh.
pub async fn consume_refresh_token(
    tx: &mut Transaction<'_, Postgres>,
    raw_token: &str,
    client_id: Uuid,
) -> anyhow::Result<ConsumeRefreshOutcome> {
    let token_hash = hash_token(raw_token);

    let row = sqlx::query(
        "SELECT id, client_id, tenant_id, user_id, scope, revoked_at, expires_at \
         FROM oauth_refresh_tokens WHERE token_hash = $1 AND client_id = $2",
    )
    .bind(&token_hash)
    .bind(client_id)
    .fetch_optional(&mut **tx)
    .await?;

    let Some(row) = row else {
        return Ok(ConsumeRefreshOutcome::Invalid);
    };

    let revoked_at: Option<DateTime<Utc>> = row.try_get("revoked_at")?;
    let user_id: Uuid = row.try_get("user_id")?;

    if revoked_at.is_some() {
        sqlx::query(
            "UPDATE oauth_refresh_tokens SET revoked_at = now() \
             WHERE client_id = $1 AND user_id = $2 AND revoked_at IS NULL",
        )
        .bind(client_id)
        .bind(user_id)
        .execute(&mut **tx)
        .await?;
        return Ok(ConsumeRefreshOutcome::Reused);
    }

    let expires_at: DateTime<Utc> = row.try_get("expires_at")?;
    if expires_at <= Utc::now() {
        return Ok(ConsumeRefreshOutcome::Invalid);
    }

    let old_id: Uuid = row.try_get("id")?;
    let tenant_id: Uuid = row.try_get("tenant_id")?;
    let scope: String = row.try_get("scope")?;

    let (new_record, raw_new_token) = create_refresh_token(
        tx,
        CreateRefreshInput {
            client_id,
            tenant_id,
            user_id,
            scope,
        },
    )
    .await?;

    sqlx::query("UPDATE oauth_refresh_tokens SET revoked_at = now(), replaced_by = $1 WHERE id = $2")
        .bind(new_record.id)
        .bind(old_id)
        .execute(&mut **tx)
        .await?;

    Ok(ConsumeRefreshOutcome::Rotated(new_record, raw_new_token))
}

/// `POST /oauth/revoke` (RFC 7009) — the refresh-token half of revocation (see this crate's own
/// doc comment for why an already-issued access token can't be revoked the same way). Silent
/// no-op if nothing matches (wrong client, unknown token, already revoked): RFC 7009 §2.2 treats
/// an unrecognized/already-invalid token as success, so a caller can't use this endpoint's
/// response to probe whether a guessed token was ever valid.
pub async fn revoke_refresh_token<'e>(
    executor: impl PgExecutor<'e>,
    raw_token: &str,
    client_id: Uuid,
) -> anyhow::Result<()> {
    sqlx::query("UPDATE oauth_refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND client_id = $2 AND revoked_at IS NULL")
        .bind(hash_token(raw_token))
        .bind(client_id)
        .execute(executor)
        .await?;
    Ok(())
}
