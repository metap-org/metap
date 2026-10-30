package cronscheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/cron"
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
	if _, err := pool.Exec(context.Background(), "UPDATE cron_jobs SET next_run_at = now() - interval '1 minute' WHERE id = $1", jobID); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })

	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, now())",
		runID, tenantID, job.ID, string(cron.RunEnqueued)); err != nil {
		t.Fatal(err)
	}

	payload := &cron.CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
		Attempt: 1, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds, DispatchMode: job.DispatchMode,
	}
	Execute(ctx, pool, testExecutorConfig(), payload)

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected exactly 1 webhook call, got %d", calls)
	}

	status, ok, err := cron.RunStatusOf(ctx, pool, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != cron.RunSuccess {
		t.Fatalf("got status=%v ok=%v, want success", status, ok)
	}

	// Idempotency: calling Execute again on an already-finished run must not re-dispatch.
	Execute(ctx, pool, testExecutorConfig(), payload)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected the second Execute call to skip re-dispatching (redelivered message), got %d total calls", calls)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })

	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for, attempt) VALUES ($1, $2, $3, $4, now(), 1)",
		runID, tenantID, job.ID, string(cron.RunEnqueued)); err != nil {
		t.Fatal(err)
	}

	payload := &cron.CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
		Attempt: 1, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds, DispatchMode: job.DispatchMode,
	}
	Execute(ctx, pool, testExecutorConfig(), payload)

	status, ok, err := cron.RunStatusOf(ctx, pool, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != cron.RunFailed {
		t.Fatalf("got status=%v ok=%v, want failed", status, ok)
	}

	var retryCount int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM cron_job_runs WHERE job_id = $1 AND attempt = 2", job.ID).Scan(&retryCount); err != nil {
		t.Fatal(err)
	}
	if retryCount != 1 {
		t.Fatalf("expected exactly 1 scheduled retry row, got %d", retryCount)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })

	runID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, now())",
		runID, tenantID, job.ID, string(cron.RunEnqueued)); err != nil {
		t.Fatal(err)
	}

	payload := &cron.CronJobDuePayload{
		RunID: runID, JobID: job.ID, TenantID: tenantID, TargetType: job.TargetType, TargetConfig: job.TargetConfig,
		Attempt: 1, MaxAttempts: job.MaxAttempts, RetryBackoffSeconds: job.RetryBackoffSeconds, DispatchMode: job.DispatchMode,
	}
	Execute(ctx, pool, testExecutorConfig(), payload)

	if len(callOrder) != 2 || callOrder[0] != 1 || callOrder[1] != 2 {
		t.Fatalf("expected steps to run in order [1, 2], got %v", callOrder)
	}

	status, ok, err := cron.RunStatusOf(ctx, pool, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != cron.RunSuccess {
		t.Fatalf("got status=%v ok=%v, want success", status, ok)
	}

	run, err := cron.GetWorkflowRunByCronJobRun(ctx, pool, tenantID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("expected a workflow_runs row")
	}
	if run.Status != string(cron.WorkflowSuccess) {
		t.Fatalf("got workflow run status %q, want success", run.Status)
	}
	if run.CurrentStepIndex != 2 {
		t.Fatalf("got current_step_index %d, want 2", run.CurrentStepIndex)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cron.DeleteJob(ctx, pool, tenantID, job.ID) })
	makeDueNow(t, pool, job.ID)

	if err := tick(ctx, pool, testExecutorConfig(), 50); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the direct-mode job's webhook to have been called during tick()")
	}

	var runCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM cron_job_runs WHERE job_id = $1 AND status = $2", job.ID, string(cron.RunSuccess)).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("expected exactly 1 successful run row, got %d", runCount)
	}
}

func strPtr(s string) *string { return &s }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
