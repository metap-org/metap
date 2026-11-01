// The consume-and-run loop (RunExecutor), one firing's idempotency-checked execution (Execute),
// and the per-TargetType router (dispatch) that calls into the Run*/RunSteps functions. Ported
// from metap-cron-scheduler's executor/dispatch.rs.
package cronscheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/cron"
	"github.com/metap-org/metap-go/infra/eventbus"
	"github.com/metap-org/metap-go/runtime"
)

const ExecutorQueue = "cron.executor"

// RunExecutor runs the consume-execute loop until ctx is canceled — a thin wrapper around
// eventbus.RunResilientConsumer (reconnects with backoff on any connection loss). No retry
// policy here — a failed job execution already has its own DB-scheduled backoff
// (cron.FinishRunWithRetry/ClaimDueRetries), so a second, message-level retry on top of it
// isn't needed for this consumer.
func RunExecutor(ctx context.Context, connect func() (eventbus.EventBus, error), pool *pgxpool.Pool, config *ExecutorConfig) error {
	return eventbus.RunResilientConsumer(ctx, ExecutorQueue, cron.RoutingKey, nil, connect,
		func(ctx context.Context, event *eventbus.ConsumedEvent) {
			var payload cron.CronJobDuePayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				slog.Warn("malformed cron.job.due payload, routed to dead-letter queue", "error", err)
				_ = event.Nack(false)
				return
			}
			Execute(ctx, pool, config, &payload)
			_ = event.Ack()
		})
}

// Execute runs one job to completion and records its result — shared by the outbox-consuming
// loop above and the ticker's Direct-dispatch path (ticker.go), so "how a job actually gets
// executed" has exactly one implementation regardless of which DispatchMode got it here.
//
// Idempotency check first: cron.job.due is at-least-once (a crash between dispatch finishing and
// the message's ack landing redelivers it) — run_id's cron_job_runs.status starts "enqueued" and
// only ever leaves that state once via FinishRun/FinishRunWithRetry at the end of this function,
// so a status that's already success/failed/waiting means this exact firing already ran (or is
// durably paused) — skip re-dispatching and let the caller ack the (now known-duplicate)
// delivery as normal.
//
// Wrapped in a fresh root trace context: this process consumes from RabbitMQ, not from an HTTP
// request, so there is no ambient trace otherwise — a fresh root trace means every outbound
// workflow_transition/bulk_query_action/webhook call this job makes propagates onward, and a
// record written by a cron job is traceable back to the run that caused it.
func Execute(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, payload *cron.CronJobDuePayload) {
	tc := runtime.TraceContextFromHeaders(http.Header{})
	ctx = runtime.WithTraceContext(ctx, tc)

	status, ok, err := cron.RunStatusOf(ctx, pool, payload.RunID)
	if err != nil {
		slog.Error("failed to check cron job run status, proceeding anyway", "run_id", payload.RunID, "error", err)
	} else if ok && (status == cron.RunSuccess || status == cron.RunFailed || status == cron.RunWaiting) {
		slog.Warn("cron job run already completed or waiting, skipping duplicate dispatch (redelivered message)",
			"run_id", payload.RunID, "job_id", payload.JobID)
		return
	}

	if err := cron.StartRun(ctx, pool, payload.RunID); err != nil {
		slog.Error("failed to mark cron job run started", "run_id", payload.RunID, "error", err)
	}

	summary, waiting, dispatchErr := dispatch(ctx, pool, config, payload)
	if waiting {
		// run_steps already wrote cron_job_runs.status = "waiting" (via PauseWorkflowRun) before
		// returning this — nothing left to record here, and calling FinishRunWithRetry would
		// incorrectly mark the run finished while it's still durably paused.
		slog.Info("cron job chain paused on wait_event", "job_id", payload.JobID, "run_id", payload.RunID)
		return
	}

	status2 := cron.RunSuccess
	var errMsg *string
	var responseSummary json.RawMessage
	if dispatchErr != nil {
		status2 = cron.RunFailed
		msg := dispatchErr.Error()
		errMsg = &msg
		slog.Warn("cron job execution failed", "job_id", payload.JobID, "run_id", payload.RunID, "error", msg)
	} else {
		responseSummary = summary
		slog.Info("cron job executed", "job_id", payload.JobID, "run_id", payload.RunID)
	}

	if err := cron.FinishRunWithRetry(ctx, pool, payload, status2, errMsg, responseSummary); err != nil {
		slog.Error("failed to record cron job run result", "run_id", payload.RunID, "error", err)
	}
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
		summary, err := RunWorkflowTransition(ctx, config.TargetGRPCBackend, payload.TargetConfig)
		return summary, false, err
	case cron.TargetBulkQueryAction:
		summary, err := RunBulkQueryAction(ctx, config.TargetGRPCBackend, payload.TargetConfig)
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
