//go:build integration

package cron

import (
	"context"
	"testing"
	"time"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	return pool
}

func scheduleJob(name string, enabled bool) NewCronJob {
	expr := "0 */5 * * * *"
	return NewCronJob{
		Name: name, TriggerType: string(TriggerSchedule), CronExpr: &expr, Timezone: "UTC",
		TargetType: string(TargetWebhook), TargetConfig: []byte(`{"url":"https://example.com"}`),
		DispatchMode: string(DispatchOutbox), MaxAttempts: 3, RetryBackoffSeconds: 30, Enabled: enabled,
	}
}

func TestCreateJobComputesNextRunAtForAScheduleJob(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()

	job, err := CreateJob(ctx, pool, tenant, scheduleJob("test job", true), nil)
	require.NoError(t, err)
	require.NotNil(t, job.NextRunAt, "expected next_run_at to be computed")
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })
}

func TestCreateJobRejectsScheduleTypeWithNoCronExpr(t *testing.T) {
	pool := testPool(t)
	input := scheduleJob("bad job", true)
	input.CronExpr = nil
	_, err := CreateJob(context.Background(), pool, uuid.New(), input, nil)
	require.Error(t, err, "expected an error")
}

func TestGetJobReturnsNilWhenNotFound(t *testing.T) {
	pool := testPool(t)
	job, err := GetJob(context.Background(), pool, uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Nil(t, job, "expected nil, got %+v", job)
}

func TestUpdateJobReturnsNilWhenNotFound(t *testing.T) {
	pool := testPool(t)
	job, err := UpdateJob(context.Background(), pool, uuid.New(), uuid.New(), JobUpdate{})
	require.NoError(t, err)
	require.Nil(t, job, "expected nil")
}

func TestUpdateJobRecomputesNextRunAtOnScheduleChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("to update", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	newExpr := "0 0 */1 * * *"
	updated, err := UpdateJob(ctx, pool, tenant, job.ID, JobUpdate{CronExpr: &newExpr})
	require.NoError(t, err)
	require.NotNil(t, updated, "expected a recomputed next_run_at")
	require.NotNil(t, updated.NextRunAt, "expected a recomputed next_run_at")
}

func TestDeleteJobThenGetReturnsNil(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("to delete", true), nil)
	require.NoError(t, err)
	require.NoError(t, DeleteJob(ctx, pool, tenant, job.ID))
	got, err := GetJob(ctx, pool, tenant, job.ID)
	require.NoError(t, err)
	require.Nil(t, got, "expected job to be gone")
}

func TestListJobsReturnsOnlyThisTenantsJobs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	jobA, err := CreateJob(ctx, pool, tenantA, scheduleJob("job a", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenantA, jobA.ID) })
	jobB, err := CreateJob(ctx, pool, tenantB, scheduleJob("job b", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenantB, jobB.ID) })

	jobsA, err := ListJobs(ctx, pool, tenantA)
	require.NoError(t, err)
	require.Len(t, jobsA, 1, "got %+v", jobsA)
	require.Equal(t, jobA.ID, jobsA[0].ID, "got %+v", jobsA)
}

func TestClaimDueJobsClaimsAnEnabledDueJobAndAdvancesNextRunAt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("due job", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	// Force it due now regardless of the real schedule.
	past := time.Now().UTC().Add(-time.Hour)
	_, err = pool.Exec(ctx, "UPDATE cron_jobs SET next_run_at = $1 WHERE id = $2", past, job.ID)
	require.NoError(t, err)

	result, err := ClaimDueJobs(ctx, pool, time.Now().UTC(), 10)
	require.NoError(t, err)
	require.False(t, result.Claimed < 1, "expected at least 1 claimed, got %d", result.Claimed)

	updated, err := GetJob(ctx, pool, tenant, job.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.NextRunAt, "expected next_run_at to be advanced past the due occurrence")
	require.True(t, updated.NextRunAt.After(past), "expected next_run_at to be advanced past the due occurrence")
	require.NotNil(t, updated.LastRunAt, "expected last_run_at to be set")

	runs, err := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1, "got %+v", runs)
	require.EqualValues(t, string(RunEnqueued), runs[0].Status, "got %+v", runs)
}

func TestClaimDueJobsSkipsDisabledJobs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("disabled job", false), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })
	past := time.Now().UTC().Add(-time.Hour)
	_, err = pool.Exec(ctx, "UPDATE cron_jobs SET next_run_at = $1 WHERE id = $2", past, job.ID)
	require.NoError(t, err)

	runs, _ := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	beforeCount := len(runs)

	_, err = ClaimDueJobs(ctx, pool, time.Now().UTC(), 10)
	require.NoError(t, err)

	runsAfter, _ := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	require.Len(t, runsAfter, beforeCount, "a disabled job must never be claimed")
}

func TestRunLifecycleStartFinishAndRetry(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("lifecycle job", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC())
	require.NoError(t, err)

	status, ok, err := RunStatusOf(ctx, pool, runID)
	require.NoError(t, err, "got (%v, %v, %v)", status, ok, err)
	require.True(t, ok, "got (%v, %v, %v)", status, ok, err)
	require.Equal(t, RunEnqueued, status, "got (%v, %v, %v)", status, ok, err)

	require.NoError(t, StartRun(ctx, pool, runID))

	payload := &CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenant, TargetType: string(TargetWebhook),
		TargetConfig: job.TargetConfig, Attempt: 1, MaxAttempts: 3, RetryBackoffSeconds: 30,
		DispatchMode: string(DispatchOutbox),
	}
	errMsg := "boom"
	require.NoError(t, FinishRunWithRetry(ctx, pool, payload, RunFailed, &errMsg, nil))

	runs, err := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2, "expected a retry row to be scheduled, got %d runs", len(runs))
	var retryFound bool
	for _, r := range runs {
		if r.Attempt == 2 && r.Status == string(RunEnqueued) {
			retryFound = true
		}
	}
	require.True(t, retryFound, "expected an attempt=2 enqueued retry row, got %+v", runs)
}

func TestFinishRunWithRetryDoesNotRetryOnceMaxAttemptsExhausted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("exhausted job", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for, attempt) VALUES ($1, $2, $3, $4, $5, $6)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC(), 3)
	require.NoError(t, err)

	payload := &CronJobDuePayload{RunID: runID, JobID: job.ID, TenantID: tenant, Attempt: 3, MaxAttempts: 3, RetryBackoffSeconds: 30}
	errMsg := "final failure"
	require.NoError(t, FinishRunWithRetry(ctx, pool, payload, RunFailed, &errMsg, nil))

	runs, err := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1, "expected no retry row once max_attempts is exhausted, got %d runs", len(runs))
}

func TestDispatchOnTransitionMatchesFiresMatchingJobsOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()

	triggerConfig := []byte(`{"entity":"crm.customers","action":"approve"}`)
	job, err := CreateJob(ctx, pool, tenant, NewCronJob{
		Name: "on transition", TriggerType: string(TriggerOnTransition), TriggerConfig: triggerConfig,
		Timezone: "UTC", TargetType: string(TargetWebhook), TargetConfig: []byte(`{"url":"https://example.com"}`),
		DispatchMode: string(DispatchOutbox), MaxAttempts: 1, RetryBackoffSeconds: 30, Enabled: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	result, err := DispatchOnTransitionMatches(ctx, pool, tenant, "crm.customers", "approve", uuid.New())
	require.NoError(t, err)
	require.Equal(t, 1, result.Claimed, "expected 1 match, got %d", result.Claimed)

	noMatch, err := DispatchOnTransitionMatches(ctx, pool, tenant, "crm.customers", "reject", uuid.New())
	require.NoError(t, err)
	require.Equal(t, 0, noMatch.Claimed, "expected 0 matches for a different action, got %d", noMatch.Claimed)
}

func TestDirectDispatchModeReturnsClaimedDirectJobsInsteadOfOutboxEvent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	expr := "0 */5 * * * *"
	job, err := CreateJob(ctx, pool, tenant, NewCronJob{
		Name: "direct job", TriggerType: string(TriggerSchedule), CronExpr: &expr, Timezone: "UTC",
		TargetType: string(TargetWebhook), TargetConfig: []byte(`{"url":"https://example.com"}`),
		DispatchMode: string(DispatchDirect), MaxAttempts: 1, RetryBackoffSeconds: 30, Enabled: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	past := time.Now().UTC().Add(-time.Hour)
	_, err = pool.Exec(ctx, "UPDATE cron_jobs SET next_run_at = $1 WHERE id = $2", past, job.ID)
	require.NoError(t, err)

	result, err := ClaimDueJobs(ctx, pool, time.Now().UTC(), 10)
	require.NoError(t, err)
	var found bool
	for _, dj := range result.DirectJobs {
		if dj.JobID == job.ID {
			found = true
		}
	}
	require.True(t, found, "expected %s in DirectJobs, got %+v", job.ID, result.DirectJobs)
}

func TestWorkflowRunLifecycleStartAdvanceFinish(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("steps job", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC())
	require.NoError(t, err)

	workflowRunID, err := StartWorkflowRun(ctx, pool, tenant, job.ID, runID, 2)
	require.NoError(t, err)

	require.NoError(t, AdvanceWorkflowRun(ctx, pool, workflowRunID, 0, []byte(`{"ok":true}`)))
	require.NoError(t, FinishWorkflowRun(ctx, pool, workflowRunID))

	got, err := GetWorkflowRunByCronJobRun(ctx, pool, tenant, runID)
	require.NoError(t, err)
	require.NotNil(t, got, "got %+v", got)
	require.EqualValues(t, string(WorkflowSuccess), got.Status, "got %+v", got)
	require.EqualValues(t, 1, got.CurrentStepIndex, "got %+v", got)
}

func TestWorkflowRunPauseAndResumeOnTransition(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("waiting job", true), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC())
	require.NoError(t, err)
	workflowRunID, err := StartWorkflowRun(ctx, pool, tenant, job.ID, runID, 2)
	require.NoError(t, err)

	action := "approve"
	wait := WaitEventTargetConfig{Entity: "crm.orders", Action: &action}
	require.NoError(t, PauseWorkflowRun(ctx, pool, workflowRunID, runID, 1, wait))

	paused, err := GetWorkflowRunByCronJobRun(ctx, pool, tenant, runID)
	require.NoError(t, err)
	require.EqualValues(t, string(WorkflowWaiting), paused.Status, "expected waiting, got %s", paused.Status)

	resumed, err := DispatchOnWaitEventTransitionMatches(ctx, pool, tenant, "crm.orders", "approve", uuid.New())
	require.NoError(t, err)
	require.Len(t, resumed, 1, "got %+v", resumed)
	require.Equal(t, workflowRunID, resumed[0].WorkflowRunID, "got %+v", resumed)
	require.EqualValues(t, 2, resumed[0].ResumeFromStepIndex, "got %+v", resumed)

	afterResume, err := GetWorkflowRunByCronJobRun(ctx, pool, tenant, runID)
	require.NoError(t, err)
	require.EqualValues(t, string(WorkflowRunning), afterResume.Status, "expected running after resume, got %s", afterResume.Status)
}
