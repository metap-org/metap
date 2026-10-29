package cron

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/infra"
)

// ClaimResult mirrors the Rust ClaimResult.
type ClaimResult struct {
	// Claimed is the total jobs claimed this batch, Outbox and Direct combined.
	Claimed int
	// DirectJobs is the Direct-mode subset the caller must execute itself right now.
	DirectJobs []ClaimedDirectJob
}

// retryClaimStaleAfter: a claimed retry that never finishes is reclaimed after this long.
const retryClaimStaleAfter = 300 * time.Second

// ClaimDueJobs claims every job due at or before now (SELECT ... FOR UPDATE SKIP LOCKED),
// advances each one's next_run_at, and inserts a cron_job_runs row — all in one transaction per
// claimed batch.
func ClaimDueJobs(ctx context.Context, pool *pgxpool.Pool, now time.Time, batchSize int64) (*ClaimResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		"SELECT "+jobColumns+" FROM cron_jobs WHERE enabled AND next_run_at <= $1 ORDER BY next_run_at LIMIT $2 FOR UPDATE SKIP LOCKED",
		now, batchSize)
	if err != nil {
		return nil, err
	}
	var jobs []*CronJob
	for rows.Next() {
		j, err := jobFromRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &ClaimResult{Claimed: len(jobs)}

	for _, job := range jobs {
		if job.CronExpr == nil {
			slog.Error("disabling cron job: schedule job with no cron_expr", "job_id", job.ID)
			if _, err := tx.Exec(ctx, "UPDATE cron_jobs SET enabled = false, updated_at = now() WHERE id = $1", job.ID); err != nil {
				return nil, err
			}
			continue
		}
		next, err := NextRunAt(*job.CronExpr, job.Timezone, now)
		if err != nil {
			slog.Error("disabling cron job: cannot compute next run", "job_id", job.ID, "error", err)
			if _, err := tx.Exec(ctx, "UPDATE cron_jobs SET enabled = false, updated_at = now() WHERE id = $1", job.ID); err != nil {
				return nil, err
			}
			continue
		}

		if _, err := tx.Exec(ctx, "UPDATE cron_jobs SET next_run_at = $1, last_run_at = $2, updated_at = now() WHERE id = $3",
			next, now, job.ID); err != nil {
			return nil, err
		}

		runID := uuid.New()
		if _, err := tx.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
			runID, job.TenantID, job.ID, string(RunEnqueued), job.NextRunAt); err != nil {
			return nil, err
		}

		if err := dispatchClaimed(ctx, tx, job, runID, 1, nil, nil, result); err != nil {
			return nil, err
		}

		slog.Info("cron job claimed", "job_id", job.ID, "run_id", runID, "name", job.Name, "dispatch_mode", job.DispatchMode)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// DispatchOnTransitionMatches fires every enabled trigger_type = "on_transition" job whose
// trigger_config matches (tenantID, entity, action).
func DispatchOnTransitionMatches(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity, action string, recordID uuid.UUID) (*ClaimResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT `+jobColumns+` FROM cron_jobs
		 WHERE tenant_id = $1 AND enabled AND trigger_type = 'on_transition'
		 AND trigger_config ->> 'entity' = $2 AND trigger_config ->> 'action' = $3`,
		tenantID, entity, action)
	if err != nil {
		return nil, err
	}
	jobs, err := collectJobs(rows)
	if err != nil {
		return nil, err
	}
	result, err := fireMatchedJobs(ctx, tx, jobs, "on_transition", entity, action, &recordID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// DispatchOnRecordEventMatches fires every enabled trigger_type = "on_record_event" job whose
// trigger_config matches (tenantID, entity, event).
func DispatchOnRecordEventMatches(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity, event string, recordID uuid.UUID) (*ClaimResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT `+jobColumns+` FROM cron_jobs
		 WHERE tenant_id = $1 AND enabled AND trigger_type = 'on_record_event'
		 AND trigger_config ->> 'entity' = $2 AND trigger_config ->> 'event' = $3`,
		tenantID, entity, event)
	if err != nil {
		return nil, err
	}
	jobs, err := collectJobs(rows)
	if err != nil {
		return nil, err
	}
	result, err := fireMatchedJobs(ctx, tx, jobs, "on_record_event", entity, event, &recordID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func collectJobs(rows pgx.Rows) ([]*CronJob, error) {
	defer rows.Close()
	var jobs []*CronJob
	for rows.Next() {
		j, err := jobFromRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func fireMatchedJobs(ctx context.Context, tx pgx.Tx, jobs []*CronJob, triggerKind, entity, detail string, recordID *uuid.UUID) (*ClaimResult, error) {
	result := &ClaimResult{Claimed: len(jobs)}
	now := time.Now().UTC()

	for _, job := range jobs {
		runID := uuid.New()
		if _, err := tx.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
			runID, job.TenantID, job.ID, string(RunEnqueued), now); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "UPDATE cron_jobs SET last_run_at = $1, updated_at = now() WHERE id = $2", now, job.ID); err != nil {
			return nil, err
		}

		if err := dispatchClaimed(ctx, tx, job, runID, 1, &entity, recordID, result); err != nil {
			return nil, err
		}

		slog.Info("cron job triggered", "job_id", job.ID, "run_id", runID, "name", job.Name,
			"entity", entity, "trigger_kind", triggerKind, "detail", detail)
	}
	return result, nil
}

// ClaimDueRetries claims every retry due at or before now — a cron_job_runs row with attempt >
// 1 that a prior failed attempt scheduled.
func ClaimDueRetries(ctx context.Context, pool *pgxpool.Pool, now time.Time, batchSize int64) (*ClaimResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	staleBefore := now.Add(-retryClaimStaleAfter)
	rows, err := tx.Query(ctx,
		`SELECT r.id AS run_id, r.attempt, r.started_at AS run_started_at,
		 j.id, j.tenant_id, j.name, j.enabled, j.trigger_type, j.trigger_config, j.cron_expr, j.timezone,
		 j.target_type, j.target_config, j.dispatch_mode, j.max_attempts, j.retry_backoff_seconds,
		 j.next_run_at, j.last_run_at, j.created_at, j.updated_at, j.created_by
		 FROM cron_job_runs r
		 JOIN cron_jobs j ON j.id = r.job_id
		 WHERE r.attempt > 1 AND r.status = $1 AND (r.started_at IS NULL OR r.started_at < $4)
		 AND r.scheduled_for <= $2 AND j.enabled
		 ORDER BY r.scheduled_for LIMIT $3 FOR UPDATE OF r SKIP LOCKED`,
		string(RunEnqueued), now, batchSize, staleBefore)
	if err != nil {
		return nil, err
	}

	type claimedRetry struct {
		runID     uuid.UUID
		attempt   int32
		reclaimed bool
		job       *CronJob
	}
	var retries []claimedRetry
	for rows.Next() {
		var runID uuid.UUID
		var attempt int32
		var runStartedAt *time.Time
		var j CronJob
		if err := rows.Scan(&runID, &attempt, &runStartedAt,
			&j.ID, &j.TenantID, &j.Name, &j.Enabled, &j.TriggerType, &j.TriggerConfig, &j.CronExpr,
			&j.Timezone, &j.TargetType, &j.TargetConfig, &j.DispatchMode, &j.MaxAttempts, &j.RetryBackoffSeconds,
			&j.NextRunAt, &j.LastRunAt, &j.CreatedAt, &j.UpdatedAt, &j.CreatedBy); err != nil {
			rows.Close()
			return nil, err
		}
		retries = append(retries, claimedRetry{runID: runID, attempt: attempt, reclaimed: runStartedAt != nil, job: &j})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &ClaimResult{Claimed: len(retries)}
	for _, retry := range retries {
		if err := dispatchClaimed(ctx, tx, retry.job, retry.runID, retry.attempt, nil, nil, result); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "UPDATE cron_job_runs SET started_at = now() WHERE id = $1", retry.runID); err != nil {
			return nil, err
		}
		if retry.reclaimed {
			slog.Warn("cron job retry reclaimed (previous claim never finished — stale outbox or crashed executor)",
				"job_id", retry.job.ID, "run_id", retry.runID, "attempt", retry.attempt)
		} else {
			slog.Info("cron job retry claimed", "job_id", retry.job.ID, "run_id", retry.runID, "attempt", retry.attempt)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func dispatchClaimed(ctx context.Context, tx pgx.Tx, job *CronJob, runID uuid.UUID, attempt int32, triggerEntity *string, triggerRecordID *uuid.UUID, result *ClaimResult) error {
	mode, ok := ParseDispatchMode(job.DispatchMode)
	if ok && mode == DispatchDirect {
		result.DirectJobs = append(result.DirectJobs, ClaimedDirectJob{
			RunID: runID, JobID: job.ID, TenantID: job.TenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
			Attempt: attempt, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds,
			DispatchMode: job.DispatchMode, TriggerRecordID: triggerRecordID, TriggerEntity: triggerEntity,
		})
		return nil
	}

	// Unknown dispatch_mode (shouldn't happen) falls back to the reliable path.
	payload := CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: job.TenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
		Attempt: attempt, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds,
		DispatchMode: job.DispatchMode, TriggerRecordID: triggerRecordID, TriggerEntity: triggerEntity,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return infra.EnqueueOutboxEvent(ctx, tx, &infra.OutboxEvent{
		Topic: RoutingKey, AggregateType: "cron_job", AggregateID: job.ID, Payload: payloadJSON,
	})
}
