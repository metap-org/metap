//go:build integration

package cronscheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/cron"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	return pool
}

func webhookJob(tenantID uuid.UUID, dispatchMode, url string) cron.NewCronJob {
	expr := "0 */5 * * * *"
	targetConfig, _ := json.Marshal(map[string]any{"url": url})
	return cron.NewCronJob{
		Name: "test-" + uuid.New().String(), TriggerType: string(cron.TriggerSchedule), CronExpr: &expr,
		Timezone: "UTC", TargetType: string(cron.TargetWebhook), TargetConfig: targetConfig,
		DispatchMode: dispatchMode, MaxAttempts: 1, RetryBackoffSeconds: 30, Enabled: true,
	}
}

// makeDueNow pushes a job's next_run_at into the past, so the next ClaimDueJobs call claims it.
func makeDueNow(t *testing.T, pool *pgxpool.Pool, jobID uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "UPDATE cron_jobs SET next_run_at = now() - interval '1 minute' WHERE id = $1", jobID)
	require.NoError(t, err)
}

func testExecutorConfig() *ExecutorConfig {
	return &ExecutorConfig{Webhook: WebhookPolicy{AllowPrivateTargets: true}}
}

// Execute, run against a real Postgres: a webhook job's firing is recorded as a success with the
// webhook's response captured as response_summary.
func TestExecuteRunsAWebhookJobAndRecordsSuccess(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	job, err := cron.CreateJob(ctx, pool, tenantID, webhookJob(tenantID, string(cron.DispatchOutbox), srv.URL), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })

	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, now())",
		runID, tenantID, job.ID, string(cron.RunEnqueued))
	require.NoError(t, err)

	payload := &cron.CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
		Attempt: 1, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds, DispatchMode: job.DispatchMode,
	}
	Execute(ctx, pool, testExecutorConfig(), payload)

	require.EqualValues(t, 1, atomic.LoadInt32(&calls), "expected exactly 1 webhook call, got %d", calls)

	status, ok, err := cron.RunStatusOf(ctx, pool, runID)
	require.NoError(t, err)
	require.True(t, ok, "got status=%v ok=%v, want success", status, ok)
	require.Equal(t, cron.RunSuccess, status, "got status=%v ok=%v, want success", status, ok)

	// Idempotency: calling Execute again on an already-finished run must not re-dispatch.
	Execute(ctx, pool, testExecutorConfig(), payload)
	require.EqualValues(t, 1, atomic.LoadInt32(&calls), "expected the second Execute call to skip re-dispatching (redelivered message), got %d total calls", calls)
}

// Execute, run against a real Postgres: a webhook job's firing failure is recorded with the
// error message, and a retry row is scheduled per max_attempts/retry_backoff_seconds.
func TestExecuteRecordsFailureAndSchedulesARetry(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	job, err := cron.CreateJob(ctx, pool, tenantID, cron.NewCronJob{
		Name: "test-fail-" + uuid.New().String(), TriggerType: string(cron.TriggerSchedule),
		CronExpr: strPtr("0 */5 * * * *"), Timezone: "UTC", TargetType: string(cron.TargetWebhook),
		TargetConfig: mustJSON(map[string]any{"url": srv.URL}), DispatchMode: string(cron.DispatchOutbox),
		MaxAttempts: 3, RetryBackoffSeconds: 30, Enabled: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })

	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for, attempt) VALUES ($1, $2, $3, $4, now(), 1)",
		runID, tenantID, job.ID, string(cron.RunEnqueued))
	require.NoError(t, err)

	payload := &cron.CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
		Attempt: 1, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds, DispatchMode: job.DispatchMode,
	}
	Execute(ctx, pool, testExecutorConfig(), payload)

	status, ok, err := cron.RunStatusOf(ctx, pool, runID)
	require.NoError(t, err)
	require.True(t, ok, "got status=%v ok=%v, want failed", status, ok)
	require.Equal(t, cron.RunFailed, status, "got status=%v ok=%v, want failed", status, ok)

	var retryCount int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM cron_job_runs WHERE job_id = $1 AND attempt = 2", job.ID).Scan(&retryCount))
	require.Equal(t, 1, retryCount, "expected exactly 1 scheduled retry row, got %d", retryCount)
}

// RunSteps, run against a real Postgres: a 2-step webhook chain runs both steps in order and
// records the combined summary, with workflow_runs tracking progress to completion.
func TestRunStepsExecutesEveryStepInOrderAndFinishes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()

	var callOrder []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n, ok := body["step"].(float64); ok {
			callOrder = append(callOrder, int(n))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stepsConfigJSON := mustJSON(map[string]any{
		"steps": []map[string]any{
			{"targetType": "webhook", "targetConfig": map[string]any{"url": srv.URL, "body": map[string]any{"step": 1}}},
			{"targetType": "webhook", "targetConfig": map[string]any{"url": srv.URL, "body": map[string]any{"step": 2}}},
		},
	})

	job, err := cron.CreateJob(ctx, pool, tenantID, cron.NewCronJob{
		Name: "test-steps-" + uuid.New().String(), TriggerType: string(cron.TriggerSchedule),
		CronExpr: strPtr("0 */5 * * * *"), Timezone: "UTC", TargetType: string(cron.TargetSteps),
		TargetConfig: stepsConfigJSON, DispatchMode: string(cron.DispatchOutbox),
		MaxAttempts: 1, RetryBackoffSeconds: 30, Enabled: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })

	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, now())",
		runID, tenantID, job.ID, string(cron.RunEnqueued))
	require.NoError(t, err)

	payload := &cron.CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
		Attempt: 1, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds, DispatchMode: job.DispatchMode,
	}
	Execute(ctx, pool, testExecutorConfig(), payload)

	require.Len(t, callOrder, 2, "expected steps to run in order [1, 2], got %v", callOrder)
	require.Equal(t, 1, callOrder[0], "expected steps to run in order [1, 2], got %v", callOrder)
	require.Equal(t, 2, callOrder[1], "expected steps to run in order [1, 2], got %v", callOrder)

	status, ok, err := cron.RunStatusOf(ctx, pool, runID)
	require.NoError(t, err)
	require.True(t, ok, "got status=%v ok=%v, want success", status, ok)
	require.Equal(t, cron.RunSuccess, status, "got status=%v ok=%v, want success", status, ok)

	run, err := cron.GetWorkflowRunByCronJobRun(ctx, pool, tenantID, runID)
	require.NoError(t, err)
	require.NotNil(t, run, "expected a workflow_runs row")
	require.EqualValues(t, string(cron.WorkflowSuccess), run.Status, "got workflow run status %q, want success", run.Status)
	require.EqualValues(t, 2, run.CurrentStepIndex, "got current_step_index %d, want 2", run.CurrentStepIndex)
}

// RunTicker's direct-dispatch path, run against a real Postgres: a DispatchMode "direct" job
// due now is executed in the same tick that claimed it, with no outbox event involved.
func TestTickRunsADirectModeJobImmediately(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()

	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	job, err := cron.CreateJob(ctx, pool, tenantID, webhookJob(tenantID, string(cron.DispatchDirect), srv.URL), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })
	makeDueNow(t, pool, job.ID)

	require.NoError(t, tick(ctx, pool, testExecutorConfig(), 50))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "expected the direct-mode job's webhook to have been called during tick()")
	}

	var runCount int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM cron_job_runs WHERE job_id = $1 AND status = $2", job.ID, string(cron.RunSuccess)).Scan(&runCount))
	require.Equal(t, 1, runCount, "expected exactly 1 successful run row, got %d", runCount)
}

func strPtr(s string) *string { return &s }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
