// Running a firing: claim it, keep the claim alive, record the result as the one who holds it.
package cron

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClaimOutcome is what asking to run a firing came to.
type ClaimOutcome int

const (
	// ClaimWon: the caller holds the run and must run it.
	ClaimWon ClaimOutcome = iota
	// ClaimSettled: there is nothing to run (finished, paused on wait_event, or deleted).
	ClaimSettled
	// ClaimHeld: someone else holds a live lease. The caller must neither run it nor forget it:
	// the holder may die, and then this is what runs it again.
	ClaimHeld
)

// ClaimRun takes a run for ttl. One statement decides it, so of any number of concurrent callers one
// wins. Times are the database's, so workers with different clocks agree.
func ClaimRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, ttl time.Duration) (uuid.UUID, ClaimOutcome, error) {
	token := uuid.New()
	tag, err := pool.Exec(ctx,
		`UPDATE cron_job_runs SET started_at = now(), lease_token = $2, lease_expires_at = now() + make_interval(secs => $3)
		 WHERE id = $1 AND status = $4 AND (lease_expires_at IS NULL OR lease_expires_at <= now())`,
		runID, token, ttl.Seconds(), string(RunEnqueued))
	if err != nil {
		return uuid.Nil, ClaimHeld, err
	}
	if tag.RowsAffected() == 1 {
		return token, ClaimWon, nil
	}
	status, ok, err := RunStatusOf(ctx, pool, runID)
	if err != nil {
		return uuid.Nil, ClaimHeld, err
	}
	if ok && status == RunEnqueued {
		return uuid.Nil, ClaimHeld, nil
	}
	return uuid.Nil, ClaimSettled, nil
}

// RenewRunLease pushes the expiry of a held run forward. false: the caller no longer holds it.
func RenewRunLease(ctx context.Context, pool *pgxpool.Pool, runID, token uuid.UUID, ttl time.Duration) (bool, error) {
	tag, err := pool.Exec(ctx,
		"UPDATE cron_job_runs SET lease_expires_at = now() + make_interval(secs => $3) WHERE id = $1 AND lease_token = $2",
		runID, token, ttl.Seconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FinishClaimedRun is FinishRunWithRetry for the holder of token: it records the result, and the
// retry a failure asks for, only if the run is still the caller's. false: it is not, and nothing was
// written.
func FinishClaimedRun(ctx context.Context, pool *pgxpool.Pool, payload *CronJobDuePayload, token uuid.UUID,
	status RunStatus, errMsg *string, responseSummary json.RawMessage) (bool, error) {
	return finishAndScheduleRetry(ctx, pool, payload, &token, status, errMsg, responseSummary)
}
