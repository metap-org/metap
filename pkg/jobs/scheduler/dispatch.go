// The consume-and-run loop (RunExecutor), one firing's idempotency-checked execution (Execute), and
// the per-TargetType router (dispatch) that calls into the Run*/RunSteps functions.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/jobs/cron"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/messaging/eventbus"
)

const ExecutorQueue = "cron.executor"

// executorDeferDelay is how long a delivery whose run cannot be taken yet waits before it comes
// back. It is the TTL of a durable queue: changing it means deleting cron.executor.retry.1 first.
const executorDeferDelay = 15 * time.Second

// RunExecutor runs the consume-execute loop until ctx is canceled — a thin wrapper around
// eventbus.RunResilientConsumer (reconnects with backoff on any connection loss). A failed job has
// its own DB-scheduled backoff (cron.FinishClaimedRun/ClaimDueRetries), so a failure is not retried
// at the message level. The one thing the message waits for is a run it cannot take yet: it is
// deferred until the holder finishes (then it is acked as a duplicate) or the holder's lease runs
// out (then it runs the job).
func RunExecutor(ctx context.Context, connect func() (eventbus.EventBus, error), pool *pgxpool.Pool, config *ExecutorConfig) error {
	policy := &eventbus.RetryPolicy{Delays: []time.Duration{executorDeferDelay}}
	return eventbus.RunResilientConsumer(ctx, ExecutorQueue, cron.RoutingKey, policy, connect,
		func(ctx context.Context, event *eventbus.ConsumedEvent) {
			var payload cron.CronJobDuePayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				slog.Warn("malformed cron.job.due payload, routed to dead-letter queue", standard.LogError, err)
				event.NackLogged(false)
				return
			}
			if Execute(ctx, pool, config, &payload) != ExecuteDeferred {
				event.AckLogged()
				return
			}
			if err := event.Defer(ctx, policy); err != nil {
				slog.Warn("failed to defer cron.job.due, leaving it to be redelivered", standard.LogRunID, payload.RunID, standard.LogError, err)
				event.NackLogged(true)
			}
		})
}

// ExecuteResult is what became of one request to run a firing.
type ExecuteResult int

const (
	// ExecuteRan: this call ran the job and its outcome is recorded.
	ExecuteRan ExecuteResult = iota
	// ExecuteSkipped: there was nothing to run (a duplicate of a finished or paused firing).
	ExecuteSkipped
	// ExecuteDeferred: the firing is not settled and this call could not settle it: another worker
	// holds it, or the database did not take the claim or the result. Whoever asked must ask again.
	ExecuteDeferred
)

// Execute runs one job to completion and records its result — shared by the outbox-consuming
// loop above and the Direct-dispatch path, so "how a job actually gets executed" has exactly one
// implementation regardless of which DispatchMode got it here.
//
// It claims the run first (cron.ClaimRun): cron.job.due is at-least-once, and one firing can reach
// two workers at once (a redelivery while the first is still running, a second executor). Only the
// holder of the claim runs the job and records the result. A worker that dies stops renewing its
// claim and the run is taken again, so a job runs at least once, not exactly once: a target must
// tolerate being called again for a run id it has seen.
//
// Wrapped in a fresh root trace context: this process consumes from RabbitMQ, not from an HTTP
// request, so there is no ambient trace otherwise — a fresh root trace means every outbound
// workflow_transition/bulk_query_action/webhook call this job makes propagates onward, and a
// record written by a cron job is traceable back to the run that caused it.
func Execute(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, payload *cron.CronJobDuePayload) ExecuteResult {
	tc := httpx.TraceContextFromHeaders(http.Header{})
	ctx = httpx.WithTraceContext(ctx, tc)

	token, claim, err := cron.ClaimRun(ctx, pool, payload.RunID, config.runLease())
	switch {
	case err != nil:
		slog.Error("failed to claim cron job run", standard.LogRunID, payload.RunID, standard.LogError, err)
		return ExecuteDeferred
	case claim == cron.ClaimSettled:
		slog.Warn("cron job run already completed or waiting, skipping duplicate dispatch (redelivered message)",
			standard.LogRunID, payload.RunID, standard.LogJobID, payload.JobID)
		return ExecuteSkipped
	case claim == cron.ClaimHeld:
		slog.Info("cron job run is held by another worker", standard.LogRunID, payload.RunID, standard.LogJobID, payload.JobID)
		return ExecuteDeferred
	}

	runCtx, release := holdLease(ctx, pool, payload.RunID, token, config.runLease())
	summary, waiting, dispatchErr := dispatch(runCtx, pool, config, payload)
	if release() {
		return ExecuteDeferred
	}
	if waiting {
		// run_steps already wrote cron_job_runs.status = "waiting" (via PauseWorkflowRun) before
		// returning this — nothing left to record here, and finishing the run would
		// incorrectly mark it finished while it's still durably paused.
		slog.Info("cron job chain paused on wait_event", standard.LogJobID, payload.JobID, standard.LogRunID, payload.RunID)
		return ExecuteRan
	}

	status := cron.RunSuccess
	var errMsg *string
	var responseSummary json.RawMessage
	if dispatchErr != nil {
		status = cron.RunFailed
		msg := dispatchErr.Error()
		errMsg = &msg
		slog.Warn("cron job execution failed", standard.LogJobID, payload.JobID, standard.LogRunID, payload.RunID, standard.LogError, msg)
	} else {
		responseSummary = summary
		slog.Info("cron job executed", standard.LogJobID, payload.JobID, standard.LogRunID, payload.RunID)
	}

	recordCtx, end := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer end()
	recorded, err := cron.FinishClaimedRun(recordCtx, pool, payload, token, status, errMsg, responseSummary)
	if err != nil {
		slog.Error("failed to record cron job run result", standard.LogRunID, payload.RunID, standard.LogError, err)
		return ExecuteDeferred
	}
	if !recorded {
		slog.Warn("cron job run was taken over before its result was recorded", standard.LogRunID, payload.RunID)
		return ExecuteDeferred
	}
	return ExecuteRan
}

// dispatch routes payload.TargetType to the matching Run*/RunSteps function. Returns
// (summary, waiting, err) — waiting is a Steps-only outcome (see RunSteps's doc comment).
func dispatch(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, payload *cron.CronJobDuePayload) (json.RawMessage, bool, error) {
	targetType, ok := cron.ParseTargetType(payload.TargetType)
	if !ok {
		return nil, false, fmt.Errorf("unknown target_type %q", payload.TargetType)
	}
	switch targetType {
	case cron.TargetWorkflowTransition:
		summary, err := RunWorkflowTransition(ctx, config.TargetBackend, payload.TargetConfig)
		return summary, false, err
	case cron.TargetBulkQueryAction:
		summary, err := RunBulkQueryAction(ctx, config.TargetBackend, payload.TargetConfig)
		return summary, false, err
	case cron.TargetWebhook:
		summary, err := RunWebhook(ctx, config.Webhook, config.Secrets, payload.TenantID, payload.JobID, payload.RunID,
			payload.TriggerEntity, payload.TriggerRecordID, payload.TargetConfig)
		return summary, false, err
	case cron.TargetEmail:
		summary, err := RunEmail(config.SMTP, payload.TriggerEntity, payload.TriggerRecordID, payload.TargetConfig)
		return summary, false, err
	case cron.TargetSteps:
		return RunSteps(ctx, pool, config, payload)
	case cron.TargetWaitEvent:
		return nil, false, fmt.Errorf(`"wait_event" is not a valid top-level targetType, only a step inside a "steps" chain`)
	}
	return nil, false, fmt.Errorf("unknown target_type %q", payload.TargetType)
}
