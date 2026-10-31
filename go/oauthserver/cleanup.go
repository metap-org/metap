// Deletes expired/spent rows from oauth_authorization_codes/oauth_refresh_tokens/
// oauth_pending_authorizations — none of the 3 had a cleanup path before this (the Rust
// original's own roadmap doc flagged it as a known follow-up), so all three grew forever:
// oauth_authorization_codes accumulates a row per authorize request (60s TTL) whether or not
// it's ever redeemed, oauth_refresh_tokens accumulates one per issue *and* one per rotation even
// after revoked_at is set, and oauth_pending_authorizations (10min TTL) leaks one row per
// consent screen shown but never decided (tab closed, browser crash).
//
// Deliberately a plain age-based sweep, not a "delete the instant it's no longer valid" trigger:
// a short grace period past expires_at/revoked_at keeps a just-expired row around long enough to
// show up in an operator's own ad-hoc query while debugging a client's failed request, without
// the tables growing without bound the way this file exists to prevent.
package oauthserver

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CleanupGracePeriodSeconds: kept short — these rows carry no audit/compliance value once spent
// (unlike metadata.audit_trail_entries, which is never pruned by design), only short-lived
// operational value while debugging a client integration issue.
const CleanupGracePeriodSeconds = 24 * 3600

type CleanupCounts struct {
	AuthorizationCodesDeleted    int64
	RefreshTokensDeleted         int64
	PendingAuthorizationsDeleted int64
}

// DeleteBatchSize: rows deleted per statement. The first sweep after this ever runs (or after a
// long outage of the sweeper) can face millions of accumulated rows, and one unbounded DELETE
// over that many holds its row locks and bloats WAL for the whole statement; bounded batches
// keep each transaction short. Large enough that a steady-state sweep (a few thousand rows an
// hour) is one statement per table.
const DeleteBatchSize int64 = 5_000

// DeleteExpired runs one sweep, in batches of DeleteBatchSize. Safe to call repeatedly/
// concurrently (no state to coordinate — a concurrent sweep skips the rows the other one holds
// locked) — the caller decides the cadence, see Run for the standard loop shape.
func DeleteExpired(ctx context.Context, pool *pgxpool.Pool) (CleanupCounts, error) {
	return DeleteExpiredInBatches(ctx, pool, DeleteBatchSize)
}

// DeleteExpiredInBatches is DeleteExpired with an explicit batch size (clamped to at least 1) —
// exists so a test can prove the batch loop covers every row without inserting thousands.
func DeleteExpiredInBatches(ctx context.Context, pool *pgxpool.Pool, batchSize int64) (CleanupCounts, error) {
	if batchSize < 1 {
		batchSize = 1
	}
	// One cutoff for the whole sweep, so a long multi-batch run can't drift its own definition of
	// "expired" between batches.
	cutoff := time.Now().UTC().Add(-CleanupGracePeriodSeconds * time.Second)

	// Every authorization code is single-use and short-lived (60s) — once its own expires_at plus
	// the grace period has passed it's worthless whether or not used_at was ever set, so this
	// doesn't need to distinguish "expired unused" from "already redeemed".
	codes, err := deleteInBatches(ctx, pool, "oauth_authorization_codes", "expires_at < $1", cutoff, batchSize)
	if err != nil {
		return CleanupCounts{}, err
	}

	// A refresh token is worth deleting once either path makes it permanently unusable: it aged
	// past its own TTL, or it was explicitly revoked (a direct revoke call, a rotation, or
	// reuse-detection's chain-wide revoke in ConsumeRefreshToken) — in both cases keeping the row
	// around past the grace period serves no purpose ConsumeRefreshToken's own reuse check needs
	// (that check only ever looks up a *presented* token, and a deleted row is indistinguishable
	// from one that never existed — both correctly answer RefreshInvalid).
	tokens, err := deleteInBatches(ctx, pool, "oauth_refresh_tokens",
		"expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $1)", cutoff, batchSize)
	if err != nil {
		return CleanupCounts{}, err
	}

	// Already single-use and short-lived (10min) like authorization codes above — a pending
	// authorization abandoned mid-decision (tab closed, browser crash) is worthless once expired,
	// whether or not the user ever saw the screen.
	pending, err := deleteInBatches(ctx, pool, "oauth_pending_authorizations", "expires_at < $1", cutoff, batchSize)
	if err != nil {
		return CleanupCounts{}, err
	}

	return CleanupCounts{
		AuthorizationCodesDeleted:    codes,
		RefreshTokensDeleted:         tokens,
		PendingAuthorizationsDeleted: pending,
	}, nil
}

// deleteInBatches deletes every row of table matching predicate (a $1-parameterised condition on
// cutoff), batchSize rows per statement, until a statement deletes fewer than that. table/
// predicate are always caller literals from this file — never request input. SKIP LOCKED lets
// two concurrent sweeps split the work instead of one blocking on the other's row locks. Every
// table here has a uuid primary key id.
func deleteInBatches(ctx context.Context, pool *pgxpool.Pool, table, predicate string, cutoff time.Time, batchSize int64) (int64, error) {
	sql := "DELETE FROM " + table + " WHERE id IN (SELECT id FROM " + table + " WHERE " + predicate +
		" LIMIT $2 FOR UPDATE SKIP LOCKED)"
	var total int64
	for {
		tag, err := pool.Exec(ctx, sql, cutoff, batchSize)
		if err != nil {
			return total, err
		}
		deleted := tag.RowsAffected()
		total += deleted
		if deleted < batchSize {
			return total, nil
		}
	}
}

// Run runs DeleteExpired on a fixed interval until ctx is canceled — same inline-or-standalone
// shape as outboxpublisher.Run/notificationworker.Run: a host binary spawns this directly
// against its own already-resolved pool (no separate process needed, this sweep is cheap and
// infrequent) rather than this package shipping its own cmd/ binary, since there is exactly one
// of these per deployment regardless of how many tenants or services it has (oauth_clients/
// oauth_authorization_codes/oauth_refresh_tokens are deliberately not tenant-schema-split, see
// this package's own doc comment), so a dedicated standalone binary would just be one more
// process to deploy for a job that costs nothing to run inline wherever the token endpoint
// itself already lives.
func Run(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		counts, err := DeleteExpired(ctx, pool)
		switch {
		case err != nil:
			slog.Warn("oauth2 expired-token cleanup sweep failed", "error", err)
		case counts.AuthorizationCodesDeleted > 0 || counts.RefreshTokensDeleted > 0 || counts.PendingAuthorizationsDeleted > 0:
			slog.Info("oauth2 expired-token cleanup swept rows",
				"authorization_codes_deleted", counts.AuthorizationCodesDeleted,
				"refresh_tokens_deleted", counts.RefreshTokensDeleted,
				"pending_authorizations_deleted", counts.PendingAuthorizationsDeleted)
		}

		select {
		case <-ctx.Done():
			slog.Info("shutdown signal received, exiting oauth2 token cleanup loop")
			return
		case <-ticker.C:
		}
	}
}
