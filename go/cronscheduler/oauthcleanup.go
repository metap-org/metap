// Runs oauthserver's expired-token sweep alongside the cron loops, so
// oauth_authorization_codes/oauth_refresh_tokens/oauth_pending_authorizations stop growing
// without bound. oauthserver ships the sweep as a plain function (Run) rather than its own
// binary — this process already runs for exactly this kind of periodic housekeeping, so it
// hosts it. Ported from metap-cron-scheduler's oauth_cleanup.rs.
//
// The pool passed in must be the one POST /oauth/token itself writes through (the platform's
// main DATABASE_URL) — not necessarily the outbox pool, which cmd/cron-scheduler's main resolves
// separately, since the OAuth tables are deliberately not tenant-schema-split and live wherever
// migration 0034_oauth2.sql was applied.
package cronscheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/oauthserver"
)

// RunOAuthCleanup probes for the OAuth tables first: the oauth2 route group is toggleable and
// migration 0034 is only applied by deployments that mount it, so a deployment without it must
// not log a failed DELETE on every interval forever. Returns once ctx is canceled, or
// immediately if the tables are absent.
func RunOAuthCleanup(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) {
	present, err := oauthTablesPresent(ctx, pool)
	switch {
	case err != nil:
		slog.Warn("could not check for oauth2 tables — expired-token cleanup disabled", "error", err)
		<-ctx.Done()
		return
	case !present:
		slog.Info("oauth2 tables not present (migration 0034 not applied) — expired-token cleanup disabled")
		<-ctx.Done()
		return
	}

	slog.Info("oauth2 expired-token cleanup running", "interval_secs", int(interval.Seconds()))
	oauthserver.Run(ctx, pool, interval)
}

// oauthTablesPresent uses the same unqualified-name resolution (search_path) the sweep's own
// DELETEs use, so this probe can't say "present" for a table the sweep then fails to find.
func oauthTablesPresent(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var present bool
	err := pool.QueryRow(ctx,
		`SELECT to_regclass('oauth_authorization_codes') IS NOT NULL
		 AND to_regclass('oauth_refresh_tokens') IS NOT NULL
		 AND to_regclass('oauth_pending_authorizations') IS NOT NULL`).Scan(&present)
	return present, err
}
