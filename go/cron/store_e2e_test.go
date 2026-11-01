package cron

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO metadata, public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
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
	if err != nil {
		t.Fatal(err)
	}
	if job.NextRunAt == nil {
		t.Fatal("expected next_run_at to be computed")
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })
}

func TestCreateJobRejectsScheduleTypeWithNoCronExpr(t *testing.T) {
	pool := testPool(t)
	input := scheduleJob("bad job", true)
	input.CronExpr = nil
	_, err := CreateJob(context.Background(), pool, uuid.New(), input, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestGetJobReturnsNilWhenNotFound(t *testing.T) {
	pool := testPool(t)
	job, err := GetJob(context.Background(), pool, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatalf("expected nil, got %+v", job)
	}
}

func TestUpdateJobReturnsNilWhenNotFound(t *testing.T) {
	pool := testPool(t)
	job, err := UpdateJob(context.Background(), pool, uuid.New(), uuid.New(), JobUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatal("expected nil")
	}
}

func TestUpdateJobRecomputesNextRunAtOnScheduleChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("to update", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	newExpr := "0 0 */1 * * *"
	updated, err := UpdateJob(ctx, pool, tenant, job.ID, JobUpdate{CronExpr: &newExpr})
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.NextRunAt == nil {
		t.Fatal("expected a recomputed next_run_at")
	}
}

func TestDeleteJobThenGetReturnsNil(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("to delete", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteJob(ctx, pool, tenant, job.ID); err != nil {
		t.Fatal(err)
	}
	got, err := GetJob(ctx, pool, tenant, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expected job to be gone")
	}
}

func TestListJobsReturnsOnlyThisTenantsJobs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	jobA, err := CreateJob(ctx, pool, tenantA, scheduleJob("job a", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenantA, jobA.ID) })
	jobB, err := CreateJob(ctx, pool, tenantB, scheduleJob("job b", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenantB, jobB.ID) })

	jobsA, err := ListJobs(ctx, pool, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobsA) != 1 || jobsA[0].ID != jobA.ID {
		t.Fatalf("got %+v", jobsA)
	}
}

func TestClaimDueJobsClaimsAnEnabledDueJobAndAdvancesNextRunAt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("due job", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	// Force it due now regardless of the real schedule.
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE cron_jobs SET next_run_at = $1 WHERE id = $2", past, job.ID); err != nil {
		t.Fatal(err)
	}

	result, err := ClaimDueJobs(ctx, pool, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Claimed < 1 {
		t.Fatalf("expected at least 1 claimed, got %d", result.Claimed)
	}

	updated, err := GetJob(ctx, pool, tenant, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.After(past) {
		t.Fatal("expected next_run_at to be advanced past the due occurrence")
	}
	if updated.LastRunAt == nil {
		t.Fatal("expected last_run_at to be set")
	}

	runs, err := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != string(RunEnqueued) {
		t.Fatalf("got %+v", runs)
	}
}

func TestClaimDueJobsSkipsDisabledJobs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("disabled job", false), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE cron_jobs SET next_run_at = $1 WHERE id = $2", past, job.ID); err != nil {
		t.Fatal(err)
	}

	runs, _ := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	beforeCount := len(runs)

	if _, err := ClaimDueJobs(ctx, pool, time.Now().UTC(), 10); err != nil {
		t.Fatal(err)
	}

	runsAfter, _ := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	if len(runsAfter) != beforeCount {
		t.Fatal("a disabled job must never be claimed")
	}
}

func TestRunLifecycleStartFinishAndRetry(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("lifecycle job", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	status, ok, err := RunStatusOf(ctx, pool, runID)
	if err != nil || !ok || status != RunEnqueued {
		t.Fatalf("got (%v, %v, %v)", status, ok, err)
	}

	if err := StartRun(ctx, pool, runID); err != nil {
		t.Fatal(err)
	}

	payload := &CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenant, TargetType: string(TargetWebhook),
		TargetConfig: job.TargetConfig, Attempt: 1, MaxAttempts: 3, RetryBackoffSeconds: 30,
		DispatchMode: string(DispatchOutbox),
	}
	errMsg := "boom"
	if err := FinishRunWithRetry(ctx, pool, payload, RunFailed, &errMsg, nil); err != nil {
		t.Fatal(err)
	}

	runs, err := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected a retry row to be scheduled, got %d runs", len(runs))
	}
	var retryFound bool
	for _, r := range runs {
		if r.Attempt == 2 && r.Status == string(RunEnqueued) {
			retryFound = true
		}
	}
	if !retryFound {
		t.Fatalf("expected an attempt=2 enqueued retry row, got %+v", runs)
	}
}

func TestFinishRunWithRetryDoesNotRetryOnceMaxAttemptsExhausted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("exhausted job", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for, attempt) VALUES ($1, $2, $3, $4, $5, $6)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC(), 3); err != nil {
		t.Fatal(err)
	}

	payload := &CronJobDuePayload{RunID: runID, JobID: job.ID, TenantID: tenant, Attempt: 3, MaxAttempts: 3, RetryBackoffSeconds: 30}
	errMsg := "final failure"
	if err := FinishRunWithRetry(ctx, pool, payload, RunFailed, &errMsg, nil); err != nil {
		t.Fatal(err)
	}

	runs, err := ListJobRuns(ctx, pool, tenant, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected no retry row once max_attempts is exhausted, got %d runs", len(runs))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	result, err := DispatchOnTransitionMatches(ctx, pool, tenant, "crm.customers", "approve", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if result.Claimed != 1 {
		t.Fatalf("expected 1 match, got %d", result.Claimed)
	}

	noMatch, err := DispatchOnTransitionMatches(ctx, pool, tenant, "crm.customers", "reject", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if noMatch.Claimed != 0 {
		t.Fatalf("expected 0 matches for a different action, got %d", noMatch.Claimed)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	past := time.Now().UTC().Add(-time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE cron_jobs SET next_run_at = $1 WHERE id = $2", past, job.ID); err != nil {
		t.Fatal(err)
	}

	result, err := ClaimDueJobs(ctx, pool, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, dj := range result.DirectJobs {
		if dj.JobID == job.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s in DirectJobs, got %+v", job.ID, result.DirectJobs)
	}
}

func TestWorkflowRunLifecycleStartAdvanceFinish(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("steps job", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	workflowRunID, err := StartWorkflowRun(ctx, pool, tenant, job.ID, runID, 2)
	if err != nil {
		t.Fatal(err)
	}

	if err := AdvanceWorkflowRun(ctx, pool, workflowRunID, 0, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := FinishWorkflowRun(ctx, pool, workflowRunID); err != nil {
		t.Fatal(err)
	}

	got, err := GetWorkflowRunByCronJobRun(ctx, pool, tenant, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != string(WorkflowSuccess) || got.CurrentStepIndex != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestWorkflowRunPauseAndResumeOnTransition(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	job, err := CreateJob(ctx, pool, tenant, scheduleJob("waiting job", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteJob(ctx, pool, tenant, job.ID) })

	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, $5)",
		runID, tenant, job.ID, string(RunEnqueued), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	workflowRunID, err := StartWorkflowRun(ctx, pool, tenant, job.ID, runID, 2)
	if err != nil {
		t.Fatal(err)
	}

	action := "approve"
	wait := WaitEventTargetConfig{Entity: "crm.orders", Action: &action}
	if err := PauseWorkflowRun(ctx, pool, workflowRunID, runID, 1, wait); err != nil {
		t.Fatal(err)
	}

	paused, err := GetWorkflowRunByCronJobRun(ctx, pool, tenant, runID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != string(WorkflowWaiting) {
		t.Fatalf("expected waiting, got %s", paused.Status)
	}

	resumed, err := DispatchOnWaitEventTransitionMatches(ctx, pool, tenant, "crm.orders", "approve", uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].WorkflowRunID != workflowRunID || resumed[0].ResumeFromStepIndex != 2 {
		t.Fatalf("got %+v", resumed)
	}

	afterResume, err := GetWorkflowRunByCronJobRun(ctx, pool, tenant, runID)
	if err != nil {
		t.Fatal(err)
	}
	if afterResume.Status != string(WorkflowRunning) {
		t.Fatalf("expected running after resume, got %s", afterResume.Status)
	}
}
