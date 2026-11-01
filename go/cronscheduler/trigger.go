// Consumes every event on the shared exchange (routing key "#") and dispatches any cron_jobs row
// whose trigger matches — trigger_type "on_transition" for a <entity>.workflow.transitioned
// event, or "on_record_event" for a <entity>.record.{created,updated,deleted} event. A routing
// key that matches neither shape (e.g. cron.job.due, meant for RunExecutor's own queue) is acked
// silently — receiving it is expected under a catch-all subscription, not a data error worth a
// dead-letter. Ported from metap-cron-scheduler's trigger.rs.
package cronscheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/cron"
	"github.com/metap-org/metap-go/infra/eventbus"
)

const (
	TriggerQueue      = "cron.workflow-trigger"
	TriggerRoutingKey = "#"
)

// triggerRetryPolicy: a trigger-match attempt failing is (almost always) a transient DB error,
// not a permanent one — worth a few backed-off retries before giving up. 5s/30s/2m: enough
// spacing that a brief DB hiccup clears well within the first tier, capped low enough that a
// genuinely broken DB doesn't sit retrying for hours before landing in the dead-letter queue.
func triggerRetryPolicy() *eventbus.RetryPolicy {
	return &eventbus.RetryPolicy{Delays: []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}}
}

type topicKind int

const (
	topicNone topicKind = iota
	topicTransitioned
	topicRecordEvent
)

// topic is what kind of trigger-worthy event a routing key names — topicNone for anything else
// (not an error, just not this listener's concern).
type topic struct {
	kind   topicKind
	entity string
	event  string // only set for topicRecordEvent
}

func classifyTopic(routingKey string) topic {
	if entity, ok := strings.CutSuffix(routingKey, ".workflow.transitioned"); ok {
		return topic{kind: topicTransitioned, entity: entity}
	}
	for _, pair := range [...]struct{ suffix, event string }{
		{".record.created", "created"},
		{".record.updated", "updated"},
		{".record.deleted", "deleted"},
	} {
		if entity, ok := strings.CutSuffix(routingKey, pair.suffix); ok {
			return topic{kind: topicRecordEvent, entity: entity, event: pair.event}
		}
	}
	return topic{kind: topicNone}
}

// RunTriggerListener runs the consume-dispatch loop until ctx is canceled — a thin wrapper
// around eventbus.RunResilientConsumer. This and RunExecutor each reconnect independently
// (separate EventBus connections) rather than sharing one, so one consumer reconnecting doesn't
// interrupt the other's in-flight stream.
func RunTriggerListener(ctx context.Context, connect func() (eventbus.EventBus, error), pool *pgxpool.Pool, config *ExecutorConfig) error {
	policy := triggerRetryPolicy()
	return eventbus.RunResilientConsumer(ctx, TriggerQueue, TriggerRoutingKey, policy, connect,
		func(ctx context.Context, event *eventbus.ConsumedEvent) {
			t := classifyTopic(event.RoutingKey)
			switch t.kind {
			case topicTransitioned:
				tenantID, okT := parseTenantID(event)
				recordID, okR := parseRecordID(event)
				if !okT || !okR {
					rejectMalformed(event, "missing/invalid `tenantId`/`recordId`")
					return
				}
				action, ok := parsePayloadString(event, "action")
				if !ok {
					rejectMalformed(event, "missing/invalid `action`")
					return
				}
				dispatchTransition(ctx, pool, config, policy, event, tenantID, t.entity, action, recordID)
			case topicRecordEvent:
				tenantID, okT := parseTenantID(event)
				recordID, okR := parseRecordID(event)
				if !okT || !okR {
					rejectMalformed(event, "missing/invalid `tenantId`/`recordId`")
					return
				}
				dispatchRecordEvent(ctx, pool, config, policy, event, tenantID, t.entity, t.event, recordID)
			default:
				// Not a topic this listener cares about — acking is correct here, not a
				// fallback: under a catch-all subscription, receiving other topics is the
				// expected case.
				_ = event.Ack()
			}
		})
}

func rejectMalformed(event *eventbus.ConsumedEvent, reason string) {
	slog.Warn("malformed trigger-worthy payload, routed to dead-letter queue", "routing_key", event.RoutingKey, "reason", reason)
	_ = event.Nack(false)
}

// parseTenantID: tenantId is the one field every trigger-worthy payload carries, shared
// regardless of which topic the routing key classified as.
func parseTenantID(event *eventbus.ConsumedEvent) (uuid.UUID, bool) {
	s, ok := parsePayloadString(event, "tenantId")
	if !ok {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

// parseRecordID: recordId is likewise present on every trigger-worthy payload — threaded
// through to CronJobDuePayload.TriggerRecordID so a target can reference which record actually
// caused the firing.
func parseRecordID(event *eventbus.ConsumedEvent) (uuid.UUID, bool) {
	s, ok := parsePayloadString(event, "recordId")
	if !ok {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

func parsePayloadString(event *eventbus.ConsumedEvent, key string) (string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &m); err != nil {
		return "", false
	}
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
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

	_ = event.Ack()
}
