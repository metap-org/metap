package cron

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// TargetType is what a due job actually does.
type TargetType string

const (
	TargetWorkflowTransition TargetType = "workflow_transition"
	TargetBulkQueryAction    TargetType = "bulk_query_action"
	TargetWebhook            TargetType = "webhook"
	TargetEmail              TargetType = "email"
	TargetSteps              TargetType = "steps"
	TargetWaitEvent          TargetType = "wait_event"
)

func ParseTargetType(s string) (TargetType, bool) {
	switch TargetType(s) {
	case TargetWorkflowTransition, TargetBulkQueryAction, TargetWebhook, TargetEmail, TargetSteps, TargetWaitEvent:
		return TargetType(s), true
	}
	return "", false
}

// WaitEventTargetConfig is the target_config shape for a TargetType::WaitEvent step — exactly
// one of Action/Event must be set.
type WaitEventTargetConfig struct {
	Entity string  `json:"entity"`
	Action *string `json:"action,omitempty"`
	Event  *string `json:"event,omitempty"`
}

// DispatchMode is how a due firing gets from "claimed" to "executed".
type DispatchMode string

const (
	DispatchOutbox DispatchMode = "outbox"
	DispatchDirect DispatchMode = "direct"
)

func ParseDispatchMode(s string) (DispatchMode, bool) {
	switch DispatchMode(s) {
	case DispatchOutbox, DispatchDirect:
		return DispatchMode(s), true
	}
	return "", false
}

// TriggerType is what fires a job.
type TriggerType string

const (
	TriggerSchedule      TriggerType = "schedule"
	TriggerOnTransition  TriggerType = "on_transition"
	TriggerOnRecordEvent TriggerType = "on_record_event"
)

func ParseTriggerType(s string) (TriggerType, bool) {
	switch TriggerType(s) {
	case TriggerSchedule, TriggerOnTransition, TriggerOnRecordEvent:
		return TriggerType(s), true
	}
	return "", false
}

// OnTransitionTriggerConfig is the trigger_config shape when trigger_type = on_transition.
type OnTransitionTriggerConfig struct {
	Entity string `json:"entity"`
	Action string `json:"action"`
}

// OnRecordEventTriggerConfig is the trigger_config shape when trigger_type = on_record_event.
type OnRecordEventTriggerConfig struct {
	Entity string `json:"entity"`
	Event  string `json:"event"` // "created" | "updated" | "deleted"
}

// CronJob mirrors the Rust CronJob, including its #[serde(rename = "...")] wire shape — JSON
// tags matter here, not just documentation, since this struct is what the admin REST surface
// serializes directly (routes_cron.go wraps it in {"data": job} with no further reshaping, same
// as the Rust handler's own doc comment describes).
type CronJob struct {
	ID                  uuid.UUID       `json:"id"`
	TenantID            uuid.UUID       `json:"tenantId"`
	Name                string          `json:"name"`
	Enabled             bool            `json:"enabled"`
	TriggerType         string          `json:"triggerType"`
	TriggerConfig       json.RawMessage `json:"triggerConfig,omitempty"`
	CronExpr            *string         `json:"cronExpr,omitempty"`
	Timezone            string          `json:"timezone"`
	TargetType          string          `json:"targetType"`
	TargetConfig        json.RawMessage `json:"targetConfig"`
	DispatchMode        string          `json:"dispatchMode"`
	MaxAttempts         int32           `json:"maxAttempts"`
	RetryBackoffSeconds int32           `json:"retryBackoffSeconds"`
	NextRunAt           *time.Time      `json:"nextRunAt,omitempty"`
	LastRunAt           *time.Time      `json:"lastRunAt,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
	CreatedBy           *uuid.UUID      `json:"createdBy,omitempty"`
}

// RunStatus is the cron_job_runs.status a run is currently at.
type RunStatus string

const (
	RunEnqueued RunStatus = "enqueued"
	RunSuccess  RunStatus = "success"
	RunFailed   RunStatus = "failed"
	// RunWaiting: a TargetType::Steps firing paused at a wait_event step.
	RunWaiting RunStatus = "waiting"
)

func ParseRunStatus(s string) (RunStatus, bool) {
	switch RunStatus(s) {
	case RunEnqueued, RunSuccess, RunFailed, RunWaiting:
		return RunStatus(s), true
	}
	return "", false
}

// CronJobRun mirrors the Rust CronJobRun, including its camelCase wire shape — see CronJob's own
// doc comment for why the JSON tags matter here.
type CronJobRun struct {
	ID              uuid.UUID       `json:"id"`
	TenantID        uuid.UUID       `json:"tenantId"`
	JobID           uuid.UUID       `json:"jobId"`
	Status          string          `json:"status"`
	Attempt         int32           `json:"attempt"`
	ScheduledFor    time.Time       `json:"scheduledFor"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
	Error           *string         `json:"error,omitempty"`
	ResponseSummary json.RawMessage `json:"responseSummary,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// CronJobDuePayload is the cron.job.due outbox/RabbitMQ payload shape.
type CronJobDuePayload struct {
	RunID               uuid.UUID       `json:"runId"`
	JobID               uuid.UUID       `json:"jobId"`
	TenantID            uuid.UUID       `json:"tenantId"`
	TargetType          string          `json:"targetType"`
	TargetConfig        json.RawMessage `json:"targetConfig"`
	Attempt             int32           `json:"attempt"`
	MaxAttempts         int32           `json:"maxAttempts"`
	RetryBackoffSeconds int32           `json:"retryBackoffSeconds"`
	DispatchMode        string          `json:"dispatchMode"`
	TriggerRecordID     *uuid.UUID      `json:"triggerRecordId,omitempty"`
	TriggerEntity       *string         `json:"triggerEntity,omitempty"`
}

const RoutingKey = "cron.job.due"

// ClaimedDirectJob is a job claim_due_jobs claimed with DispatchMode::Direct — returned to the
// caller (the ticker) instead of going out via outbox.
type ClaimedDirectJob struct {
	RunID               uuid.UUID
	JobID               uuid.UUID
	TenantID            uuid.UUID
	TargetType          string
	TargetConfig        json.RawMessage
	Attempt             int32
	MaxAttempts         int32
	RetryBackoffSeconds int32
	DispatchMode        string
	TriggerRecordID     *uuid.UUID
	TriggerEntity       *string
}

// WorkflowRunStatus is progress/audit state for one TargetType::Steps firing.
type WorkflowRunStatus string

const (
	WorkflowRunning WorkflowRunStatus = "running"
	WorkflowSuccess WorkflowRunStatus = "success"
	WorkflowFailed  WorkflowRunStatus = "failed"
	WorkflowWaiting WorkflowRunStatus = "waiting"
)

// WorkflowRun mirrors the Rust WorkflowRun, including its camelCase wire shape — see CronJob's
// own doc comment for why the JSON tags matter here.
type WorkflowRun struct {
	ID               uuid.UUID       `json:"id"`
	TenantID         uuid.UUID       `json:"tenantId"`
	JobID            uuid.UUID       `json:"jobId"`
	CronJobRunID     uuid.UUID       `json:"cronJobRunId"`
	Status           string          `json:"status"`
	CurrentStepIndex int32           `json:"currentStepIndex"`
	TotalSteps       int32           `json:"totalSteps"`
	Context          json.RawMessage `json:"context,omitempty"`
	Error            *string         `json:"error,omitempty"`
	WaitEntity       *string         `json:"waitEntity,omitempty"`
	WaitAction       *string         `json:"waitAction,omitempty"`
	WaitRecordEvent  *string         `json:"waitRecordEvent,omitempty"`
	StartedAt        time.Time       `json:"startedAt"`
	FinishedAt       *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
}

// ResumedWorkflowRun is one workflow_runs row a dispatch_on_wait_event_*_matches call resumed.
type ResumedWorkflowRun struct {
	WorkflowRunID       uuid.UUID
	CronJobRunID        uuid.UUID
	JobID               uuid.UUID
	TenantID            uuid.UUID
	ResumeFromStepIndex int32
	TargetConfig        json.RawMessage
	DispatchMode        string
	MaxAttempts         int32
	RetryBackoffSeconds int32
	Attempt             int32
	ResumingRecordID    uuid.UUID
	ResumingEntity      string
}
