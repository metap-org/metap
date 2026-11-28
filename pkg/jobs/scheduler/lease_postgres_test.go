//go:build integration

package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/jobs/cron"
)

// slowTarget is a webhook target that holds every call until release is closed.
type slowTarget struct {
	url     string
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func newSlowTarget(t *testing.T) *slowTarget {
	s := &slowTarget{started: make(chan struct{}, 100), release: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.started <- struct{}{}
		select {
		case <-s.release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *slowTarget) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the target was not called")
	}
}

// enqueuedRun is one firing of a webhook job to url, as cron.job.due carries it.
func enqueuedRun(t *testing.T, pool *pgxpool.Pool, url string, maxAttempts int32) *cron.CronJobDuePayload {
	t.Helper()
	ctx := context.Background()
	tenantID := uuid.New()
	spec := webhookJob(tenantID, string(cron.DispatchOutbox), url)
	spec.MaxAttempts = maxAttempts
	job, err := cron.CreateJob(ctx, pool, tenantID, spec, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cron.DeleteJob(context.Background(), pool, tenantID, job.ID) })
	runID := uuid.New()
	_, err = pool.Exec(ctx, "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) VALUES ($1, $2, $3, $4, now())",
		runID, tenantID, job.ID, string(cron.RunEnqueued))
	require.NoError(t, err)
	return &cron.CronJobDuePayload{RunID: runID, JobID: job.ID, TenantID: tenantID, TargetType: job.TargetType,
		TargetConfig: job.TargetConfig, Attempt: 1, MaxAttempts: maxAttempts, RetryBackoffSeconds: 30, DispatchMode: job.DispatchMode}
}

func statusOf(t *testing.T, pool *pgxpool.Pool, runID uuid.UUID) cron.RunStatus {
	t.Helper()
	status, ok, err := cron.RunStatusOf(context.Background(), pool, runID)
	require.NoError(t, err)
	require.True(t, ok)
	return status
}

// One firing delivered to many workers at once is run by one. The others are told to come back, not
// that it is done: until the holder finishes, nothing says it will.
func TestAFiringDeliveredToManyWorkersRunsOnce(t *testing.T) {
	pool := testPool(t)
	target := newSlowTarget(t)
	payload := enqueuedRun(t, pool, target.url, 1)
	config := testExecutorConfig()

	const workers = 8
	results := make(chan ExecuteResult, workers)
	for range workers {
		go func() { results <- Execute(context.Background(), pool, config, payload) }()
	}
	target.waitStarted(t)
	for range workers - 1 {
		assert.Equal(t, ExecuteDeferred, <-results)
	}
	close(target.release)
	assert.Equal(t, ExecuteRan, <-results)
	assert.EqualValues(t, 1, target.calls.Load())
	assert.Equal(t, cron.RunSuccess, statusOf(t, pool, payload.RunID))
	assert.Equal(t, ExecuteSkipped, Execute(context.Background(), pool, config, payload), "a later delivery is a duplicate")
}

// A job longer than the lease stays its worker's, because the lease is renewed while it runs.
func TestALongJobKeepsItsLease(t *testing.T) {
	pool := testPool(t)
	target := newSlowTarget(t)
	payload := enqueuedRun(t, pool, target.url, 1)
	config := testExecutorConfig()
	// A renewal has a third of the lease to answer in. Short enough to keep the test quick is too
	// short for a database busy with the rest of the suite: at 600ms a stall of under a second lost
	// the lease.
	config.RunLease = 3 * time.Second

	first := make(chan ExecuteResult, 1)
	go func() { first <- Execute(context.Background(), pool, config, payload) }()
	target.waitStarted(t)
	time.Sleep(config.RunLease + config.RunLease/2) // past the first expiry: only renewal keeps it
	assert.Equal(t, ExecuteDeferred, Execute(context.Background(), pool, config, payload))
	close(target.release)
	assert.Equal(t, ExecuteRan, <-first)
	assert.EqualValues(t, 1, target.calls.Load())
}

// A worker that died holding a run stops renewing: once its lease ends, the firing is run again.
func TestTheRunOfADeadWorkerIsRunAgain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	target := newSlowTarget(t)
	close(target.release)
	payload := enqueuedRun(t, pool, target.url, 1)

	_, outcome, err := cron.ClaimRun(ctx, pool, payload.RunID, time.Second) // and nothing more: it died
	require.NoError(t, err)
	require.Equal(t, cron.ClaimWon, outcome)
	assert.Equal(t, ExecuteDeferred, Execute(ctx, pool, testExecutorConfig(), payload))
	require.Eventually(t, func() bool { return Execute(ctx, pool, testExecutorConfig(), payload) == ExecuteRan },
		10*time.Second, 200*time.Millisecond)
	assert.EqualValues(t, 1, target.calls.Load())
	assert.Equal(t, cron.RunSuccess, statusOf(t, pool, payload.RunID))
}

// A worker whose run was taken over stops its job, and writes neither a result nor a retry over its
// successor's.
func TestAWorkerThatLostItsRunRecordsNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	target := newSlowTarget(t)
	defer close(target.release)
	payload := enqueuedRun(t, pool, target.url, 3)
	config := testExecutorConfig()
	config.RunLease = 600 * time.Millisecond

	first := make(chan ExecuteResult, 1)
	go func() { first <- Execute(ctx, pool, config, payload) }()
	target.waitStarted(t)
	successor := uuid.New()
	_, err := pool.Exec(ctx, "UPDATE cron_job_runs SET lease_token = $2, lease_expires_at = now() + interval '1 minute' WHERE id = $1",
		payload.RunID, successor)
	require.NoError(t, err)

	select {
	case result := <-first:
		assert.Equal(t, ExecuteDeferred, result)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the job went on after its run was taken over")
	}
	assert.Equal(t, cron.RunEnqueued, statusOf(t, pool, payload.RunID))
	var runs int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM cron_job_runs WHERE job_id = $1", payload.JobID).Scan(&runs))
	assert.Equal(t, 1, runs, "no retry was scheduled")

	recorded, err := cron.FinishClaimedRun(ctx, pool, payload, uuid.New(), cron.RunFailed, nil, nil)
	require.NoError(t, err)
	assert.False(t, recorded)
}

// A retry that has been running longer than the reclaim age is not claimed again while its lease is
// live.
func TestARunningRetryIsNotReclaimed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	payload := enqueuedRun(t, pool, "http://127.0.0.1:1", 3)
	_, err := pool.Exec(ctx, "UPDATE cron_job_runs SET attempt = 2 WHERE id = $1", payload.RunID)
	require.NoError(t, err)
	_, outcome, err := cron.ClaimRun(ctx, pool, payload.RunID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, cron.ClaimWon, outcome)
	_, err = pool.Exec(ctx, "UPDATE cron_job_runs SET started_at = now() - interval '1 hour' WHERE id = $1", payload.RunID)
	require.NoError(t, err)

	claimed, err := cron.ClaimDueRetries(ctx, pool, time.Now().UTC().Add(time.Second), 10)
	require.NoError(t, err)
	assert.Zero(t, claimed.Claimed)

	_, err = pool.Exec(ctx, "UPDATE cron_job_runs SET lease_expires_at = now() - interval '1 second' WHERE id = $1", payload.RunID)
	require.NoError(t, err)
	claimed, err = cron.ClaimDueRetries(ctx, pool, time.Now().UTC().Add(time.Second), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, claimed.Claimed, "once the lease has ended the retry is taken up again")
}

// A slow Direct job does not hold up the loop that claimed it, and no more than DirectConcurrency
// run at once.
func TestDirectJobsRunInTheBackgroundUpToTheBound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	target := newSlowTarget(t)
	config := testExecutorConfig()
	config.DirectConcurrency = 2
	direct := newBackground(config)

	var jobs []cron.ClaimedDirectJob
	var runs []uuid.UUID
	for range 3 {
		p := enqueuedRun(t, pool, target.url, 1)
		runs = append(runs, p.RunID)
		jobs = append(jobs, cron.ClaimedDirectJob{RunID: p.RunID, JobID: p.JobID, TenantID: p.TenantID, TargetType: p.TargetType,
			TargetConfig: p.TargetConfig, Attempt: 1, MaxAttempts: 1, RetryBackoffSeconds: 30, DispatchMode: string(cron.DispatchDirect)})
	}

	returned := make(chan struct{})
	go func() { runDirectJobs(ctx, pool, direct, jobs[:2]); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "the caller waited for the jobs")
	}
	target.waitStarted(t)
	target.waitStarted(t)

	third := make(chan struct{})
	go func() { runDirectJobs(ctx, pool, direct, jobs[2:]); close(third) }()
	select {
	case <-third:
		require.FailNow(t, "a third job started while both slots were busy")
	case <-time.After(300 * time.Millisecond):
	}
	assert.EqualValues(t, 2, target.calls.Load())

	close(target.release)
	<-third
	direct.wait()
	for _, id := range runs {
		assert.Equal(t, cron.RunSuccess, statusOf(t, pool, id))
	}
}
