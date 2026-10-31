package cron

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func workflowRunFromRow(row pgx.Row) (*WorkflowRun, error) {
	var w WorkflowRun
	if err := row.Scan(&w.ID, &w.TenantID, &w.JobID, &w.CronJobRunID, &w.Status, &w.CurrentStepIndex, &w.TotalSteps,
		&w.Context, &w.Error, &w.WaitEntity, &w.WaitAction, &w.WaitRecordEvent, &w.StartedAt, &w.FinishedAt, &w.CreatedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

const workflowRunColumns = `id, tenant_id, job_id, cron_job_run_id, status, current_step_index, total_steps,
	context, error, wait_entity, wait_action, wait_record_event, started_at, finished_at, created_at`

// StartWorkflowRun creates the workflow_runs row for a TargetType::Steps firing — called once
// at the start of the executor's run_steps, before the first step executes.
func StartWorkflowRun(ctx context.Context, pool *pgxpool.Pool, tenantID, jobID, cronJobRunID uuid.UUID, totalSteps int32) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx,
		"INSERT INTO workflow_runs (tenant_id, job_id, cron_job_run_id, status, total_steps) VALUES ($1, $2, $3, $4, $5) RETURNING id",
		tenantID, jobID, cronJobRunID, string(WorkflowRunning), totalSteps).Scan(&id)
	return id, err
}

// AdvanceWorkflowRun records step stepIndex's successful result and advances
// current_step_index past it. result is merged into context under key "step_<index>".
func AdvanceWorkflowRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, stepIndex int32, result json.RawMessage) error {
	_, err := pool.Exec(ctx, "UPDATE workflow_runs SET current_step_index = $1, context = jsonb_set(context, $2, $3) WHERE id = $4",
		stepIndex+1, []string{fmt.Sprintf("step_%d", stepIndex)}, result, runID)
	return err
}

// FinishWorkflowRun marks the whole chain Success once every step has completed.
func FinishWorkflowRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) error {
	_, err := pool.Exec(ctx, "UPDATE workflow_runs SET status = $1, finished_at = now() WHERE id = $2", string(WorkflowSuccess), runID)
	return err
}

// FailWorkflowRun marks the chain Failed at stepIndex (current_step_index is left pointing at
// the step that failed, not advanced past it).
func FailWorkflowRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, stepIndex int32, errMsg string) error {
	_, err := pool.Exec(ctx,
		"UPDATE workflow_runs SET status = $1, current_step_index = $2, error = $3, finished_at = now() WHERE id = $4",
		string(WorkflowFailed), stepIndex, errMsg, runID)
	return err
}

// GetWorkflowRunByCronJobRun looks up the workflow_runs row for a given cron_job_runs.id.
func GetWorkflowRunByCronJobRun(ctx context.Context, pool *pgxpool.Pool, tenantID, cronJobRunID uuid.UUID) (*WorkflowRun, error) {
	row := pool.QueryRow(ctx, "SELECT "+workflowRunColumns+" FROM workflow_runs WHERE tenant_id = $1 AND cron_job_run_id = $2",
		tenantID, cronJobRunID)
	w, err := workflowRunFromRow(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return w, err
}

// PauseWorkflowRun pauses a TargetType::Steps chain at a wait_event step — marks both
// workflow_runs (status = "waiting") and cron_job_runs (status = "waiting") in one transaction.
func PauseWorkflowRun(ctx context.Context, pool *pgxpool.Pool, workflowRunID, cronJobRunID uuid.UUID, stepIndex int32, wait WaitEventTargetConfig) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE workflow_runs SET status = $1, current_step_index = $2, wait_entity = $3, wait_action = $4,
		 wait_record_event = $5 WHERE id = $6`,
		string(WorkflowWaiting), stepIndex, wait.Entity, wait.Action, wait.Event, workflowRunID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE cron_job_runs SET status = $1 WHERE id = $2", string(RunWaiting), cronJobRunID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// resumeMatching finds every workflow_runs row waiting on (tenantID, entity, detail), flips
// each to "running" inside the same FOR UPDATE SKIP LOCKED transaction it was matched in, and
// joins to cron_jobs/cron_job_runs for everything the resume path needs.
func resumeMatching(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity, waitColumn, detail string, resumingRecordID uuid.UUID) ([]ResumedWorkflowRun, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	query := fmt.Sprintf(
		`SELECT w.id AS workflow_run_id, w.cron_job_run_id, w.job_id, w.tenant_id, w.current_step_index,
		 j.target_config, j.dispatch_mode, j.max_attempts, j.retry_backoff_seconds, r.attempt
		 FROM workflow_runs w
		 JOIN cron_jobs j ON j.id = w.job_id
		 JOIN cron_job_runs r ON r.id = w.cron_job_run_id
		 WHERE w.tenant_id = $1 AND w.status = 'waiting' AND w.wait_entity = $2 AND w.%s = $3
		 FOR UPDATE OF w SKIP LOCKED`, waitColumn)
	rows, err := tx.Query(ctx, query, tenantID, entity, detail)
	if err != nil {
		return nil, err
	}

	type matched struct {
		workflowRunID    uuid.UUID
		cronJobRunID     uuid.UUID
		jobID            uuid.UUID
		tenantID         uuid.UUID
		currentStepIndex int32
		targetConfig     json.RawMessage
		dispatchMode     string
		maxAttempts      int32
		retryBackoff     int32
		attempt          int32
	}
	var items []matched
	for rows.Next() {
		var m matched
		if err := rows.Scan(&m.workflowRunID, &m.cronJobRunID, &m.jobID, &m.tenantID, &m.currentStepIndex,
			&m.targetConfig, &m.dispatchMode, &m.maxAttempts, &m.retryBackoff, &m.attempt); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	resumed := make([]ResumedWorkflowRun, 0, len(items))
	for _, m := range items {
		if _, err := tx.Exec(ctx, "UPDATE workflow_runs SET status = $1 WHERE id = $2", string(WorkflowRunning), m.workflowRunID); err != nil {
			return nil, err
		}
		resumed = append(resumed, ResumedWorkflowRun{
			WorkflowRunID: m.workflowRunID, CronJobRunID: m.cronJobRunID, JobID: m.jobID, TenantID: m.tenantID,
			ResumeFromStepIndex: m.currentStepIndex + 1, TargetConfig: m.targetConfig, DispatchMode: m.dispatchMode,
			MaxAttempts: m.maxAttempts, RetryBackoffSeconds: m.retryBackoff, Attempt: m.attempt,
			ResumingRecordID: resumingRecordID, ResumingEntity: entity,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resumed, nil
}

// DispatchOnWaitEventTransitionMatches resumes every wait_event step waiting on (tenantID,
// entity, action).
func DispatchOnWaitEventTransitionMatches(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity, action string, recordID uuid.UUID) ([]ResumedWorkflowRun, error) {
	return resumeMatching(ctx, pool, tenantID, entity, "wait_action", action, recordID)
}

// DispatchOnWaitEventRecordMatches resumes every wait_event step waiting on (tenantID, entity,
// event).
func DispatchOnWaitEventRecordMatches(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity, event string, recordID uuid.UUID) ([]ResumedWorkflowRun, error) {
	return resumeMatching(ctx, pool, tenantID, entity, "wait_record_event", event, recordID)
}
