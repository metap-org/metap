package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func jobFromRow(row pgx.Row) (*CronJob, error) {
	var j CronJob
	if err := row.Scan(&j.ID, &j.TenantID, &j.Name, &j.Enabled, &j.TriggerType, &j.TriggerConfig, &j.CronExpr,
		&j.Timezone, &j.TargetType, &j.TargetConfig, &j.DispatchMode, &j.MaxAttempts, &j.RetryBackoffSeconds,
		&j.NextRunAt, &j.LastRunAt, &j.CreatedAt, &j.UpdatedAt, &j.CreatedBy); err != nil {
		return nil, err
	}
	return &j, nil
}

const jobColumns = `id, tenant_id, name, enabled, trigger_type, trigger_config, cron_expr, timezone, target_type,
	target_config, dispatch_mode, max_attempts, retry_backoff_seconds, next_run_at, last_run_at, created_at,
	updated_at, created_by`

func runFromRow(row pgx.Row) (*CronJobRun, error) {
	var r CronJobRun
	if err := row.Scan(&r.ID, &r.TenantID, &r.JobID, &r.Status, &r.Attempt, &r.ScheduledFor, &r.StartedAt,
		&r.FinishedAt, &r.Error, &r.ResponseSummary, &r.CreatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

const runColumns = `id, tenant_id, job_id, status, attempt, scheduled_for, started_at, finished_at, error,
	response_summary, created_at`

// ListJobs returns every cron job for a tenant, oldest first.
func ListJobs(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) ([]*CronJob, error) {
	rows, err := pool.Query(ctx, "SELECT "+jobColumns+" FROM cron_jobs WHERE tenant_id = $1 ORDER BY created_at", tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// make(..., 0), not var out []*CronJob — an empty Go nil slice marshals to JSON null, not
	// [], diverging from the Rust Vec<CronJob> wire shape a REST client (iterating the result
	// with .map()) would otherwise break on. Same fix applied to every list function below.
	out := make([]*CronJob, 0)
	for rows.Next() {
		j, err := jobFromRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetJob returns one job by (tenantID, id), or nil if not found.
func GetJob(ctx context.Context, pool *pgxpool.Pool, tenantID, id uuid.UUID) (*CronJob, error) {
	row := pool.QueryRow(ctx, "SELECT "+jobColumns+" FROM cron_jobs WHERE tenant_id = $1 AND id = $2", tenantID, id)
	j, err := jobFromRow(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// NewCronJob mirrors the Rust NewCronJob.
type NewCronJob struct {
	Name                string
	TriggerType         string
	TriggerConfig       json.RawMessage
	CronExpr            *string // required only when TriggerType == "schedule"
	Timezone            string
	TargetType          string
	TargetConfig        json.RawMessage
	DispatchMode        string
	MaxAttempts         int32
	RetryBackoffSeconds int32
	Enabled             bool
}

func computeNextRun(triggerType string, cronExpr *string, timezone string, now time.Time) (*time.Time, error) {
	tt, _ := ParseTriggerType(triggerType)
	if tt != TriggerSchedule {
		return nil, nil
	}
	if cronExpr == nil {
		return nil, fmt.Errorf("`cronExpr` is required when triggerType is \"schedule\"")
	}
	next, err := NextRunAt(*cronExpr, timezone, now)
	if err != nil {
		return nil, err
	}
	return &next, nil
}

// CreateJob inserts a new cron job.
func CreateJob(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, input NewCronJob, createdBy *uuid.UUID) (*CronJob, error) {
	nextRun, err := computeNextRun(input.TriggerType, input.CronExpr, input.Timezone, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	row := pool.QueryRow(ctx,
		`INSERT INTO cron_jobs
		 (tenant_id, name, enabled, trigger_type, trigger_config, cron_expr, timezone, target_type,
		  target_config, dispatch_mode, max_attempts, retry_backoff_seconds, next_run_at, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING `+jobColumns,
		tenantID, input.Name, input.Enabled, input.TriggerType, input.TriggerConfig, input.CronExpr, input.Timezone,
		input.TargetType, input.TargetConfig, input.DispatchMode, input.MaxAttempts, input.RetryBackoffSeconds,
		nextRun, createdBy)
	return jobFromRow(row)
}

// JobUpdate mirrors the Rust JobUpdate — every field optional, nil means "keep existing".
type JobUpdate struct {
	Name                *string
	TriggerType         *string
	TriggerConfig       json.RawMessage
	CronExpr            *string
	Timezone            *string
	TargetType          *string
	TargetConfig        json.RawMessage
	DispatchMode        *string
	MaxAttempts         *int32
	RetryBackoffSeconds *int32
	Enabled             *bool
}

func strOr(override *string, existing string) string {
	if override != nil {
		return *override
	}
	return existing
}
func i32Or(override *int32, existing int32) int32 {
	if override != nil {
		return *override
	}
	return existing
}
func boolOr(override *bool, existing bool) bool {
	if override != nil {
		return *override
	}
	return existing
}
func rawOr(override json.RawMessage, existing json.RawMessage) json.RawMessage {
	if override != nil {
		return override
	}
	return existing
}

// UpdateJob returns (nil, nil) if no job with id exists for tenantID (not found, not an error).
func UpdateJob(ctx context.Context, pool *pgxpool.Pool, tenantID, id uuid.UUID, update JobUpdate) (*CronJob, error) {
	existing, err := GetJob(ctx, pool, tenantID, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}

	name := strOr(update.Name, existing.Name)
	triggerType := strOr(update.TriggerType, existing.TriggerType)
	triggerConfig := rawOr(update.TriggerConfig, existing.TriggerConfig)
	cronExpr := existing.CronExpr
	if update.CronExpr != nil {
		cronExpr = update.CronExpr
	}
	timezone := strOr(update.Timezone, existing.Timezone)
	targetType := strOr(update.TargetType, existing.TargetType)
	targetConfig := rawOr(update.TargetConfig, existing.TargetConfig)
	dispatchMode := strOr(update.DispatchMode, existing.DispatchMode)
	maxAttempts := i32Or(update.MaxAttempts, existing.MaxAttempts)
	retryBackoffSeconds := i32Or(update.RetryBackoffSeconds, existing.RetryBackoffSeconds)
	enabled := boolOr(update.Enabled, existing.Enabled)

	// Re-derive next_run_at whenever the schedule might have changed, so an edit takes effect
	// on its new schedule immediately.
	nextRun, err := computeNextRun(triggerType, cronExpr, timezone, time.Now().UTC())
	if err != nil {
		return nil, err
	}

	row := pool.QueryRow(ctx,
		`UPDATE cron_jobs SET name = $1, trigger_type = $2, trigger_config = $3, cron_expr = $4,
		 timezone = $5, target_type = $6, target_config = $7, dispatch_mode = $8, max_attempts = $9,
		 retry_backoff_seconds = $10, enabled = $11, next_run_at = $12, updated_at = now()
		 WHERE tenant_id = $13 AND id = $14 RETURNING `+jobColumns,
		name, triggerType, triggerConfig, cronExpr, timezone, targetType, targetConfig, dispatchMode,
		maxAttempts, retryBackoffSeconds, enabled, nextRun, tenantID, id)
	j, err := jobFromRow(row)
	if err != nil {
		return nil, err
	}
	return j, nil
}

// DeleteJob deletes a cron job by (tenantID, id). Deleting a non-existent one is not an error.
func DeleteJob(ctx context.Context, pool *pgxpool.Pool, tenantID, id uuid.UUID) error {
	_, err := pool.Exec(ctx, "DELETE FROM cron_jobs WHERE tenant_id = $1 AND id = $2", tenantID, id)
	return err
}

// ListJobRuns returns up to limit run-history rows for a job, newest first.
func ListJobRuns(ctx context.Context, pool *pgxpool.Pool, tenantID, jobID uuid.UUID, limit int64) ([]*CronJobRun, error) {
	rows, err := pool.Query(ctx,
		"SELECT "+runColumns+" FROM cron_job_runs WHERE tenant_id = $1 AND job_id = $2 ORDER BY created_at DESC LIMIT $3",
		tenantID, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*CronJobRun, 0)
	for rows.Next() {
		r, err := runFromRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
