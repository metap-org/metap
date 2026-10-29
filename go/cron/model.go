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

// CronJob mirrors the Rust CronJob.
type CronJob struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	Name                string
	Enabled             bool
	TriggerType         string
	TriggerConfig       json.RawMessage
	CronExpr            *string
	Timezone            string
	TargetType          string
	TargetConfig        json.RawMessage
	DispatchMode        string
	MaxAttempts         int32
	RetryBackoffSeconds int32
	NextRunAt           *time.Time
	LastRunAt           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CreatedBy           *uuid.UUID
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

// CronJobRun mirrors the Rust CronJobRun.
type CronJobRun struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	JobID           uuid.UUID
	Status          string
	Attempt         int32
	ScheduledFor    time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	Error           *string
	ResponseSummary json.RawMessage
	CreatedAt       time.Time
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

// WorkflowRun mirrors the Rust WorkflowRun.
type WorkflowRun struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	JobID            uuid.UUID
	CronJobRunID     uuid.UUID
	Status           string
	CurrentStepIndex int32
	TotalSteps       int32
	Context          json.RawMessage
	Error            *string
	WaitEntity       *string
	WaitAction       *string
	WaitRecordEvent  *string
	StartedAt        time.Time
	FinishedAt       *time.Time
	CreatedAt        time.Time
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
