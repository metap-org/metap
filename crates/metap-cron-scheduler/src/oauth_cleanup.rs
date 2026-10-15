//! Runs `metap_oauth_server`'s expired-token sweep alongside the cron loops, so
//! `oauth_authorization_codes`/`oauth_refresh_tokens`/`oauth_pending_authorizations` stop growing
//! without bound. `metap-oauth-server` deliberately ships the sweep as a library loop
//! (`run_cleanup`) rather than its own binary — this process already runs for exactly this kind
//! of periodic housekeeping, so it hosts it.
//!
//! The pool passed in must be the one `POST /oauth/token` itself writes through (the platform's
//! main `DATABASE_URL`) — not necessarily `OUTBOX_DATABASE_URL`, which `main.rs` resolves
//! separately, since the OAuth tables are deliberately not tenant-schema-split and live wherever
//! migration `0034_oauth2.sql` was applied.

use std::future::Future;
use std::time::Duration;

use sqlx::PgPool;

/// Probes for the OAuth tables first: `RouteGroups.oauth2` is toggleable and migration `0034`
/// is only applied by deployments that mount it, so a deployment without it must not log a failed
/// `DELETE` on every interval forever. Returns once `shutdown` resolves, or immediately if the
/// tables are absent.
pub async fn run(pool: PgPool, interval: Duration, shutdown: impl Future<Output = ()>) {
    match oauth_tables_present(&pool).await {
        Ok(true) => {}
        Ok(false) => {
            tracing::info!("oauth2 tables not present (migration 0034 not applied) — expired-token cleanup disabled");
            shutdown.await;
            return;
        }
        Err(e) => {
            tracing::warn!(error = %e, "could not check for oauth2 tables — expired-token cleanup disabled");
            shutdown.await;
            return;
        }
    }

    tracing::info!(
        interval_secs = interval.as_secs(),
        "oauth2 expired-token cleanup running"
    );
    metap_oauth_server::run_cleanup(pool, interval, shutdown).await;
}

/// Same unqualified-name resolution (`search_path`) `delete_expired`'s own `DELETE`s use, so this
/// probe can't say "present" for a table the sweep then fails to find.
async fn oauth_tables_present(pool: &PgPool) -> Result<bool, sqlx::Error> {
    sqlx::query_scalar(
        "SELECT to_regclass('oauth_authorization_codes') IS NOT NULL \
         AND to_regclass('oauth_refresh_tokens') IS NOT NULL \
         AND to_regclass('oauth_pending_authorizations') IS NOT NULL",
    )
    .fetch_one(pool)
    .await
}
