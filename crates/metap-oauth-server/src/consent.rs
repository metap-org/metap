//! Real interactive consent for `GET /oauth/authorize` — before this module existed, any already-
//! authenticated caller silently "approved" any registered client's requested scope with no screen
//! shown at all (flagged, not built, in `../metap-docs/docs/roadmap/88-oauth2-authorization-server.md`).
//! See `crates/migrations/0035_oauth2_consent.sql` for the two tables this owns and why they're
//! shaped the way they are; `crates/metap-http/src/routes/oauth2.rs` is the HTTP surface (renders
//! the actual consent screen, since this crate has no HTTP of its own).

use chrono::{DateTime, Duration as ChronoDuration, Utc};
use sqlx::{PgExecutor, Row};
use uuid::Uuid;

use crate::scope_tokens;

/// 10 minutes — long enough for a real person to read the screen and click, short enough that an
/// abandoned consent screen (tab closed, never decided) doesn't linger.
pub const PENDING_AUTHORIZATION_TTL_SECONDS: i64 = 10 * 60;

pub struct PendingAuthorization {
    pub id: Uuid,
    pub client_id: Uuid,
    pub tenant_id: Uuid,
    pub user_id: Uuid,
    pub redirect_uri: String,
    pub scope: String,
    pub code_challenge: Option<String>,
    pub code_challenge_method: Option<String>,
    pub client_state: Option<String>,
}

pub struct CreatePendingAuthorizationInput {
    pub client_id: Uuid,
    pub tenant_id: Uuid,
    pub user_id: Uuid,
    pub redirect_uri: String,
    pub scope: String,
    pub code_challenge: Option<String>,
    pub code_challenge_method: Option<String>,
    pub client_state: Option<String>,
}

fn row_to_pending(row: &sqlx::postgres::PgRow) -> Result<PendingAuthorization, sqlx::Error> {
    Ok(PendingAuthorization {
        id: row.try_get("id")?,
        client_id: row.try_get("client_id")?,
        tenant_id: row.try_get("tenant_id")?,
        user_id: row.try_get("user_id")?,
        redirect_uri: row.try_get("redirect_uri")?,
        scope: row.try_get("scope")?,
        code_challenge: row.try_get("code_challenge")?,
        code_challenge_method: row.try_get("code_challenge_method")?,
        client_state: row.try_get("client_state")?,
    })
}

/// Persists the full, already-validated `GET /oauth/authorize` request so the consent screen's
/// approve/deny POST never has to re-trust caller-supplied query params a second time — it only
/// ever reads back what this row already validated when the screen was first shown.
pub async fn create_pending_authorization<'e>(
    executor: impl PgExecutor<'e>,
    input: CreatePendingAuthorizationInput,
) -> anyhow::Result<PendingAuthorization> {
    let expires_at: DateTime<Utc> = Utc::now() + ChronoDuration::seconds(PENDING_AUTHORIZATION_TTL_SECONDS);
    let row = sqlx::query(
        "INSERT INTO oauth_pending_authorizations \
            (client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method, \
             client_state, expires_at) \
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) \
         RETURNING id, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method, \
                   client_state",
    )
    .bind(input.client_id)
    .bind(input.tenant_id)
    .bind(input.user_id)
    .bind(&input.redirect_uri)
    .bind(&input.scope)
    .bind(&input.code_challenge)
    .bind(&input.code_challenge_method)
    .bind(&input.client_state)
    .bind(expires_at)
    .fetch_one(executor)
    .await?;
    Ok(row_to_pending(&row)?)
}

/// Read-only lookup for rendering the consent screen — does **not** consume the row (a page
/// reload/back-button before deciding must still show the same screen). Returns `None` for an
/// unknown or expired id; the caller (`metap-http`) turns that into a plain "this request has
/// expired, start again" response rather than a 500.
pub async fn get_pending_authorization<'e>(
    executor: impl PgExecutor<'e>,
    id: Uuid,
) -> anyhow::Result<Option<PendingAuthorization>> {
    let row = sqlx::query(
        "SELECT id, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method, \
                client_state \
         FROM oauth_pending_authorizations WHERE id = $1 AND expires_at > now()",
    )
    .bind(id)
    .fetch_optional(executor)
    .await?;
    row.as_ref()
        .map(row_to_pending)
        .transpose()
        .map_err(anyhow::Error::from)
}

/// Atomically deletes-and-returns — the approve/deny decision consumes the pending row exactly
/// once, same single-use discipline `consume_authorization_code` uses for the same reason (a
/// double-submitted decision, e.g. a double click, must not both succeed).
///
/// `tenant_id`/`user_id` are matched **in the query itself**, not checked after the fact — a
/// mismatched caller (a different, legitimately-authenticated user in the same tenant probing
/// someone else's pending request) must not be able to delete a row it can't actually decide,
/// which an "consume unconditionally, then reject after the fact" version would do (destroying the
/// real owner's chance to ever approve it — a self-inflicted denial of service, not a data leak,
/// but avoidable at zero extra cost here).
pub async fn consume_pending_authorization<'e>(
    executor: impl PgExecutor<'e>,
    id: Uuid,
    tenant_id: Uuid,
    user_id: Uuid,
) -> anyhow::Result<Option<PendingAuthorization>> {
    let row = sqlx::query(
        "DELETE FROM oauth_pending_authorizations \
         WHERE id = $1 AND tenant_id = $2 AND user_id = $3 AND expires_at > now() \
         RETURNING id, client_id, tenant_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method, \
                   client_state",
    )
    .bind(id)
    .bind(tenant_id)
    .bind(user_id)
    .fetch_optional(executor)
    .await?;
    row.as_ref()
        .map(row_to_pending)
        .transpose()
        .map_err(anyhow::Error::from)
}

/// The scope a `(client, user)` pair has already approved, if any — `authorize` skips the consent
/// screen entirely when the newly requested scope is a subset of this (the standard "you've
/// already granted this app access" IdP behavior), re-prompting only when the client asks for
/// something new.
pub async fn get_consent_scope<'e>(
    executor: impl PgExecutor<'e>,
    client_id: Uuid,
    user_id: Uuid,
) -> anyhow::Result<Option<String>> {
    let scope: Option<String> =
        sqlx::query_scalar("SELECT scope FROM oauth_consents WHERE client_id = $1 AND user_id = $2")
            .bind(client_id)
            .bind(user_id)
            .fetch_optional(executor)
            .await?;
    Ok(scope)
}

/// Records an approval — widens the stored scope to the union of whatever was already granted
/// plus this approval, rather than replacing it, so approving a *narrower* re-request (a client
/// that happens to ask for less this time) never silently shrinks a previously wider grant.
///
/// Takes `E: PgExecutor<'e> + Copy` rather than the usual `impl PgExecutor<'e>` — this needs 2
/// statements (read the prior scope, then upsert the merged one), and only a `Copy` executor
/// (every real caller passes `&PgPool`, trivially `Copy`) can be used twice without the caller
/// having to open a transaction just for a read-then-write that doesn't need one atomically.
pub async fn record_consent<'e, E: PgExecutor<'e> + Copy>(
    executor: E,
    tenant_id: Uuid,
    client_id: Uuid,
    user_id: Uuid,
    newly_approved_scope: &str,
) -> anyhow::Result<()> {
    let existing = get_consent_scope(executor, client_id, user_id).await?;
    let merged = match existing {
        Some(prior) => {
            let mut tokens: Vec<&str> = scope_tokens(&prior);
            for t in scope_tokens(newly_approved_scope) {
                if !tokens.contains(&t) {
                    tokens.push(t);
                }
            }
            tokens.join(" ")
        }
        None => newly_approved_scope.to_string(),
    };

    sqlx::query(
        "INSERT INTO oauth_consents (client_id, tenant_id, user_id, scope) VALUES ($1, $2, $3, $4) \
         ON CONFLICT (client_id, user_id) DO UPDATE SET scope = $4, updated_at = now()",
    )
    .bind(client_id)
    .bind(tenant_id)
    .bind(user_id)
    .bind(&merged)
    .execute(executor)
    .await?;
    Ok(())
}
