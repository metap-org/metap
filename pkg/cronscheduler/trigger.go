// Consumes every event on the shared exchange (routing key "#") and dispatches any cron_jobs row
// whose trigger matches — trigger_type "on_transition" for a <entity>.workflow.transitioned
// event, or "on_record_event" for a <entity>.record.{created,updated,deleted} event. A routing
// key that matches neither shape (e.g. cron.job.due, meant for RunExecutor's own queue) is acked
// silently — receiving it is expected under a catch-all subscription, not a data error worth a
// dead-letter. Ported from metap-cron-scheduler's trigger.rs.
package cronscheduler

import (
	"context"
	"github.com/metap-org/metap/pkg/events"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/cron"
	"github.com/metap-org/metap/pkg/infra/eventbus"
)

const (
	TriggerQueue      = "cron.workflow-trigger"
	TriggerRoutingKey = events.All
)

// triggerRetryPolicy: a trigger-match attempt failing is (almost always) a transient DB error,
// not a permanent one — worth a few backed-off retries before giving up. 5s/30s/2m: enough
// spacing that a brief DB hiccup clears well within the first tier, capped low enough that a
// genuinely broken DB doesn't sit retrying for hours before landing in the dead-letter queue.
func triggerRetryPolicy() *eventbus.RetryPolicy {
	return &eventbus.RetryPolicy{Delays: []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}}
}

// RunTriggerListener runs the consume-dispatch loop until ctx is canceled — a thin wrapper
// around eventbus.RunResilientConsumer. This and RunExecutor each reconnect independently
// (separate EventBus connections) rather than sharing one, so one consumer reconnecting doesn't
// interrupt the other's in-flight stream.
func RunTriggerListener(ctx context.Context, connect func() (eventbus.EventBus, error), pool *pgxpool.Pool, config *ExecutorConfig) error {
	policy := triggerRetryPolicy()
	return eventbus.RunResilientConsumer(ctx, TriggerQueue, TriggerRoutingKey, policy, connect,
		func(ctx context.Context, event *eventbus.ConsumedEvent) {
			t := events.ParseTopic(event.RoutingKey)
			switch t.Kind {
			case events.KindTransitioned:
				p, err := events.Decode[events.Transitioned](event.Payload)
				if err != nil || p.TenantID == uuid.Nil || p.RecordID == uuid.Nil {
					rejectMalformed(event, "missing/invalid `tenantId`/`recordId`")
					return
				}
				if p.Action == "" {
					rejectMalformed(event, "missing/invalid `action`")
					return
				}
				dispatchTransition(ctx, pool, config, policy, event, p.TenantID, t.Entity, p.Action, p.RecordID)
			case events.KindRecord:
				p, err := events.Decode[events.Envelope](event.Payload)
				if err != nil || p.TenantID == uuid.Nil || p.RecordID == uuid.Nil {
					rejectMalformed(event, "missing/invalid `tenantId`/`recordId`")
					return
				}
				dispatchRecordEvent(ctx, pool, config, policy, event, p.TenantID, t.Entity, string(t.Action), p.RecordID)
			default:
				// Not a topic this listener cares about — acking is correct here, not a
				// fallback: under a catch-all subscription, receiving other topics is the
				// expected case.
				event.AckLogged()
			}
		})
}

func rejectMalformed(event *eventbus.ConsumedEvent, reason string) {
	slog.Warn("malformed trigger-worthy payload, routed to dead-letter queue", "routing_key", event.RoutingKey, "reason", reason)
	event.NackLogged(false)
}

func dispatchTransition(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, policy *eventbus.RetryPolicy,
	event *eventbus.ConsumedEvent, tenantID uuid.UUID, entity, action string, recordID uuid.UUID) {
	claim, err := cron.DispatchOnTransitionMatches(ctx, pool, tenantID, entity, action, recordID)
	finishDispatch(ctx, pool, config, policy, event, claim, err, func() ([]cron.ResumedWorkflowRun, error) {
		return cron.DispatchOnWaitEventTransitionMatches(ctx, pool, tenantID, entity, action, recordID)
	}, "on_transition", tenantID, entity, action)
}

func dispatchRecordEvent(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, policy *eventbus.RetryPolicy,
	event *eventbus.ConsumedEvent, tenantID uuid.UUID, entity, recordEvent string, recordID uuid.UUID) {
	claim, err := cron.DispatchOnRecordEventMatches(ctx, pool, tenantID, entity, recordEvent, recordID)
	finishDispatch(ctx, pool, config, policy, event, claim, err, func() ([]cron.ResumedWorkflowRun, error) {
		return cron.DispatchOnWaitEventRecordMatches(ctx, pool, tenantID, entity, recordEvent, recordID)
	}, "on_record_event", tenantID, entity, recordEvent)
}

// finishDispatch is the shared ack/nack + direct-dispatch/resume tail for both trigger kinds.
// fireErr's retry semantics are unchanged from before resume support existed: only a fire
// failure (nothing committed, since its own transaction rolled back) retries the whole event.
// Resuming wait_event-paused chains only happens after the fire is known to have succeeded, and
// deliberately never causes a retry of the whole event on its own — doing so would call
// DispatchOn*Matches a second time and double-fire the brand-new jobs already committed above. A
// resume-query failure is safe to simply drop instead: it's naturally idempotent (a chain it
// resumes leaves "waiting" inside the same transaction, so a later attempt won't match it
// again), so a resume failure here just means this specific event didn't get to resume a
// matching chain — the chain stays "waiting" and can still be resumed by any later matching event.
func finishDispatch(ctx context.Context, pool *pgxpool.Pool, config *ExecutorConfig, policy *eventbus.RetryPolicy,
	event *eventbus.ConsumedEvent, claim *cron.ClaimResult, fireErr error,
	resumeQuery func() ([]cron.ResumedWorkflowRun, error), triggerKind string, tenantID uuid.UUID, entity, detail string) {
	if fireErr != nil {
		slog.Warn("failed to dispatch trigger matches, retrying with backoff",
			"tenant_id", tenantID, "entity", entity, "trigger_kind", triggerKind, "detail", detail,
			"error", fireErr, "retry_count", event.RetryCount())
		_ = event.RetryOrGiveUp(ctx, policy)
		return
	}

	for _, directJob := range claim.DirectJobs {
		payload := cron.CronJobDuePayload{
			RunID: directJob.RunID, JobID: directJob.JobID, TenantID: directJob.TenantID, TargetType: directJob.TargetType,
			TargetConfig: directJob.TargetConfig, Attempt: directJob.Attempt, MaxAttempts: directJob.MaxAttempts,
			RetryBackoffSeconds: directJob.RetryBackoffSeconds, DispatchMode: directJob.DispatchMode,
			TriggerRecordID: directJob.TriggerRecordID, TriggerEntity: directJob.TriggerEntity,
		}
		Execute(ctx, pool, config, &payload)
	}

	resumed, err := resumeQuery()
	if err != nil {
		slog.Error("failed to resume wait_event chains for this event — any matching chain stays waiting",
			"tenant_id", tenantID, "entity", entity, "trigger_kind", triggerKind, "detail", detail, "error", err)
	} else {
		for i := range resumed {
			ResumeSteps(ctx, pool, config, &resumed[i])
		}
	}

	event.AckLogged()
}
