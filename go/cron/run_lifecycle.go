package cron

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// backoffDelay: retry_backoff_seconds * 2^(attempt-1). attempt is the attempt that just failed
// (1-based).
func backoffDelay(retryBackoffSeconds int32, attempt int32) time.Duration {
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 30 {
		exponent = 30
	}
	seconds := int64(retryBackoffSeconds) << exponent
	return time.Duration(seconds) * time.Second
}

// RunStatusOf returns the cron_job_runs.status a run is currently at, or ("", false) if runID
// doesn't exist — the idempotency check before actually dispatching a redelivered message.
func RunStatusOf(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) (RunStatus, bool, error) {
	var status string
	err := pool.QueryRow(ctx, "SELECT status FROM cron_job_runs WHERE id = $1", runID).Scan(&status)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	rs, ok := ParseRunStatus(status)
	return rs, ok, nil
}

// StartRun marks a run as started.
func StartRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) error {
	_, err := pool.Exec(ctx, "UPDATE cron_job_runs SET started_at = now() WHERE id = $1", runID)
	return err
}

// FinishRun records a run's final outcome.
func FinishRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, status RunStatus, errMsg *string, responseSummary json.RawMessage) error {
	_, err := pool.Exec(ctx,
		"UPDATE cron_job_runs SET status = $1, error = $2, response_summary = $3, finished_at = now() WHERE id = $4",
		string(status), errMsg, responseSummary, runID)
	return err
}

// FinishRunWithRetry records the outcome of payload's firing, and — on failure with attempts
// remaining — also schedules the next retry as a new cron_job_runs row.
func FinishRunWithRetry(ctx context.Context, pool *pgxpool.Pool, payload *CronJobDuePayload, status RunStatus, errMsg *string, responseSummary json.RawMessage) error {
	if err := FinishRun(ctx, pool, payload.RunID, status, errMsg, responseSummary); err != nil {
		return err
	}

	if status != RunFailed || payload.Attempt >= payload.MaxAttempts {
		return nil
	}

	nextAttempt := payload.Attempt + 1
	scheduledFor := time.Now().UTC().Add(backoffDelay(payload.RetryBackoffSeconds, payload.Attempt))
	_, err := pool.Exec(ctx,
		"INSERT INTO cron_job_runs (id, tenant_id, job_id, status, attempt, scheduled_for) VALUES ($1, $2, $3, $4, $5, $6)",
		uuid.New(), payload.TenantID, payload.JobID, string(RunEnqueued), nextAttempt, scheduledFor)
	return err
}
