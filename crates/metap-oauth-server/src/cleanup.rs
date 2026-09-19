//! Deletes expired/spent rows from `oauth_authorization_codes`/`oauth_refresh_tokens`/
//! `oauth_pending_authorizations` — none of the 3 had a cleanup path before this (Phase 88's
//! roadmap doc flagged it as a known follow-up), so all three grew forever: `oauth_authorization_codes`
//! accumulates a row per `GET /oauth/authorize` (60s TTL, `code::AUTHORIZATION_CODE_TTL_SECONDS`)
//! whether or not it's ever redeemed, `oauth_refresh_tokens` accumulates one per issue *and* one
//! per rotation (`refresh::consume_refresh_token`) even after `revoked_at` is set, and
//! `oauth_pending_authorizations` (added by `consent`, 10min TTL) leaks one row per consent screen
//! shown but never decided (tab closed, browser crash).
//!
//! Deliberately a plain age-based sweep, not a "delete the instant it's no longer valid" trigger:
//! a short grace period past `expires_at`/`revoked_at` keeps a just-expired row around long enough
//! to show up in an operator's own ad-hoc query while debugging a client's failed request, without
//! the two tables growing without bound the way this module exists to prevent.

use chrono::{Duration as ChronoDuration, Utc};
use sqlx::PgPool;

/// Kept short — these rows carry no audit/compliance value once spent (unlike
/// `metadata.audit_trail_entries`, which is never pruned by design, see `metap-audit`), only
/// short-lived operational value while debugging a client integration issue.
pub const CLEANUP_GRACE_PERIOD_SECONDS: i64 = 24 * 3600;

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct CleanupCounts {
    pub authorization_codes_deleted: u64,
    pub refresh_tokens_deleted: u64,
    pub pending_authorizations_deleted: u64,
}

/// One sweep. Safe to call repeatedly/concurrently (plain `DELETE ... WHERE`, no state to
/// coordinate) — the caller decides the cadence, see [`run`] for the standard loop shape.
pub async fn delete_expired(pool: &PgPool) -> anyhow::Result<CleanupCounts> {
    let cutoff = Utc::now() - ChronoDuration::seconds(CLEANUP_GRACE_PERIOD_SECONDS);

    // Every authorization code is single-use and short-lived (60s) — once its own `expires_at`
    // plus the grace period has passed it's worthless whether or not `used_at` was ever set, so
    // this doesn't need to distinguish "expired unused" from "already redeemed".
    let codes = sqlx::query("DELETE FROM oauth_authorization_codes WHERE expires_at < $1")
        .bind(cutoff)
        .execute(pool)
        .await?;

    // A refresh token is worth deleting once either path makes it permanently unusable: it aged
    // past its own TTL, or it was explicitly revoked (a direct `POST /oauth/revoke`, a rotation,
    // or reuse-detection's chain-wide revoke in `consume_refresh_token`) — in both cases keeping
    // the row around past the grace period serves no purpose `consume_refresh_token`'s own reuse
    // check needs (that check only ever looks up a *presented* token, and a deleted row is
    // indistinguishable from one that never existed — both correctly answer `Invalid`).
    let tokens = sqlx::query(
        "DELETE FROM oauth_refresh_tokens WHERE expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $1)",
    )
    .bind(cutoff)
    .execute(pool)
    .await?;

    // Already single-use and short-lived (10min) like authorization codes above — a pending
    // authorization abandoned mid-decision (tab closed, browser crash) is worthless once expired,
    // whether or not the user ever saw the screen.
    let pending = sqlx::query("DELETE FROM oauth_pending_authorizations WHERE expires_at < $1")
        .bind(cutoff)
        .execute(pool)
        .await?;

    Ok(CleanupCounts {
        authorization_codes_deleted: codes.rows_affected(),
        refresh_tokens_deleted: tokens.rows_affected(),
        pending_authorizations_deleted: pending.rows_affected(),
    })
}

/// Runs [`delete_expired`] on a fixed interval until `shutdown` resolves — same inline-or-
/// standalone shape as `outbox_publisher::run`/`notification_worker::run`: a host binary spawns
/// this directly against its own already-resolved pool (no separate process needed, this sweep is
/// cheap and infrequent) rather than this crate shipping its own `main.rs`, since — unlike the
/// outbox/notification workers — there is exactly one of these per deployment regardless of how
/// many tenants or services it has (`oauth_clients`/`oauth_authorization_codes`/
/// `oauth_refresh_tokens` are deliberately not tenant-schema-split, see this crate's own doc
/// comment), so a dedicated standalone binary would just be one more process to deploy for a job
/// that costs nothing to run inline wherever `POST /oauth/token` itself already lives.
pub async fn run(pool: PgPool, interval: std::time::Duration, shutdown: impl std::future::Future<Output = ()>) {
    let mut shutdown = std::pin::pin!(shutdown);
    loop {
        match delete_expired(&pool).await {
            Ok(counts)
                if counts.authorization_codes_deleted > 0
                    || counts.refresh_tokens_deleted > 0
                    || counts.pending_authorizations_deleted > 0 =>
            {
                tracing::info!(
                    authorization_codes_deleted = counts.authorization_codes_deleted,
                    refresh_tokens_deleted = counts.refresh_tokens_deleted,
                    pending_authorizations_deleted = counts.pending_authorizations_deleted,
                    "oauth2 expired-token cleanup swept rows"
                );
            }
            Ok(_) => {}
            Err(err) => tracing::warn!(error = %err, "oauth2 expired-token cleanup sweep failed"),
        }

        if !metap_infra::sleep_or_shutdown(interval, &mut shutdown).await {
            tracing::info!("shutdown signal received, exiting oauth2 token cleanup loop");
            return;
        }
    }
}
