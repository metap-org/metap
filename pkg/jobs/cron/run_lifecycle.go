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

// FinishRun records a run's final outcome.
func FinishRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, status RunStatus, errMsg *string, responseSummary json.RawMessage) error {
	_, err := pool.Exec(ctx,
		`UPDATE cron_job_runs SET status = $1, error = $2, response_summary = $3, finished_at = now(),
		 lease_token = NULL, lease_expires_at = NULL WHERE id = $4`,
		string(status), errMsg, responseSummary, runID)
	return err
}

// FinishRunWithRetry records the outcome of payload's firing, and — on failure with attempts
// remaining — also schedules the next retry as a new cron_job_runs row. It is for a run nobody
// claims (a resumed wait_event chain, owned through its workflow_runs row); the executor uses
// FinishClaimedRun.
func FinishRunWithRetry(ctx context.Context, pool *pgxpool.Pool, payload *CronJobDuePayload, status RunStatus, errMsg *string, responseSummary json.RawMessage) error {
	_, err := finishAndScheduleRetry(ctx, pool, payload, nil, status, errMsg, responseSummary)
	return err
}

// finishAndScheduleRetry writes the result and the retry in one transaction, so a failure is never
// recorded without the retry it asks for. With a token, only while the run is still held by it.
func finishAndScheduleRetry(ctx context.Context, pool *pgxpool.Pool, payload *CronJobDuePayload, token *uuid.UUID,
	status RunStatus, errMsg *string, responseSummary json.RawMessage) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`UPDATE cron_job_runs SET status = $1, error = $2, response_summary = $3, finished_at = now(),
		 lease_token = NULL, lease_expires_at = NULL WHERE id = $4 AND ($5::uuid IS NULL OR lease_token = $5)`,
		string(status), errMsg, responseSummary, payload.RunID, token)
	if err != nil {
		return false, err
	}
	if token != nil && tag.RowsAffected() == 0 {
		return false, nil
	}

	if status == RunFailed && payload.Attempt < payload.MaxAttempts {
		scheduledFor := time.Now().UTC().Add(backoffDelay(payload.RetryBackoffSeconds, payload.Attempt))
		if _, err := tx.Exec(ctx,
			"INSERT INTO cron_job_runs (id, tenant_id, job_id, status, attempt, scheduled_for) VALUES ($1, $2, $3, $4, $5, $6)",
			uuid.New(), payload.TenantID, payload.JobID, string(RunEnqueued), payload.Attempt+1, scheduledFor); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}
