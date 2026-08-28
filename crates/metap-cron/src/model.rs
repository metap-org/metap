//! `CronJob`/`CronJobRun` — the metadata for a scheduled action (`docs/roadmap.md` Phase 13)
//! and its execution history. Not modeled as an `EntityDefinition`/generic `records` row like
//! a business entity: this is platform/ops configuration (who runs what, when), same category
//! as `policies`/`user_roles`, not tenant business data.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// What a due job actually does. `WorkflowTransition`/`BulkQueryAction` both call back into
/// the owning `crm-server`'s own `/api/:entity/...` HTTP surface (reusing its permission
/// checks, validation, and audit trail for free) rather than this crate or `cron-scheduler`
/// linking `metap-crud`/`metap-metadata` directly — doing so would give an ops binary
/// business-entity knowledge, which `CLAUDE.md`'s boundary rules forbid. `Webhook` calls an
/// arbitrary external URL instead.
#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum TargetType {
    /// `target_config`: `{ entity, recordId, action }` — a single fixed record's transition.
    WorkflowTransition,
    /// `target_config`: `{ entity, filter, action }` — apply `action` to every record
    /// `filter` matches (a `QueryPlanner`-compatible filter object, resolved by `crm-server`).
    BulkQueryAction,
    /// `target_config`: `{ url, method, headers?, bodyTemplate? }`.
    Webhook,
    /// `target_config`: `{ to: string | string[], subject: string, body: string }` — sent via
    /// SMTP (`cron-scheduler`'s `run_email`), configured entity-agnostically by an admin rather
    /// than hardcoded per business case (`notification-worker` stays a fixed, unconfigurable
    /// stdout log of every transition — this is the "admin picks which entity/event mails whom"
    /// path Phase 39 adds instead).
    Email,
    /// `target_config`: `{ steps: [{ targetType, targetConfig }, ...] }` — a sequential chain of
    /// activities run one after another within a single dispatch (`docs/features/02-workflow-
    /// engine.md` Increment 2). Each step's `targetType` must be one of the other four variants
    /// (nesting `"steps"` inside a step is rejected, not silently flattened — this increment is
    /// deliberately "no branching/wait", not recursive composition). A step failing stops the
    /// chain there; the whole firing is `Failed` and retried from step 0 by the same
    /// retry-with-backoff Increment 1 already built (no partial-resume — that's Increment 3's
    /// `wait_event`/durable-pause territory, not this one's). Progress is tracked in the
    /// `workflow_runs` table (one row per firing, `current_step_index`/`context` updated as each
    /// step completes) — purely for observability/audit; no step reads a prior step's output,
    /// matching this increment's static (not templated) `target_config`.
    Steps,
    /// `target_config`: `{ entity: String, action: Option<String>, event: Option<String> }`
    /// (`WaitEventTargetConfig`, exactly one of `action`/`event` set) — Increment 3's durable
    /// pause. Valid **only** as a step inside a `Steps` chain (rejected as a job's own top-level
    /// `target_type` at creation time, same posture as nesting `"steps"` inside a step), never
    /// dispatched through `run_one_step` like the other four variants. Hitting this step pauses
    /// the whole chain: `cron-scheduler::executor::run_steps` writes `workflow_runs.status =
    /// "waiting"` (plus `wait_entity`/`wait_action`/`wait_record_event`) and
    /// `cron_job_runs.status = "waiting"`, then returns without running any further step.
    /// `cron-scheduler::trigger`'s listener (already subscribed to every
    /// `<entity>.workflow.transitioned`/`<entity>.record.*` event since Increment 1/Phase 38)
    /// additionally checks each incoming event against every `waiting` chain's wait criteria and
    /// resumes a match at `current_step_index + 1` — same "one process, no new consumer" reuse
    /// Increment 1 established for firing brand-new jobs. A step failing after resume re-fails
    /// the whole chain through the exact same `finish_run_with_retry` (retry from step 0, not
    /// partial-resume) Increment 2 already built — no new retry semantics needed here either.
    WaitEvent,
}

impl TargetType {
    pub fn as_str(self) -> &'static str {
        match self {
            TargetType::WorkflowTransition => "workflow_transition",
            TargetType::BulkQueryAction => "bulk_query_action",
            TargetType::Webhook => "webhook",
            TargetType::Email => "email",
            TargetType::Steps => "steps",
            TargetType::WaitEvent => "wait_event",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "workflow_transition" => Some(TargetType::WorkflowTransition),
            "bulk_query_action" => Some(TargetType::BulkQueryAction),
            "webhook" => Some(TargetType::Webhook),
            "email" => Some(TargetType::Email),
            "steps" => Some(TargetType::Steps),
            "wait_event" => Some(TargetType::WaitEvent),
            _ => None,
        }
    }
}

/// `target_config` shape for a `TargetType::WaitEvent` step — exactly one of `action`/`event`
/// must be set (validated at job-creation time by `metap-http::routes::cron::validate_target_config`
/// and defensively again by `cron-scheduler::executor::run_steps`), matching which of the two
/// wait kinds it is: `action` waits for `<entity>.workflow.transitioned` with that `action`
/// (mirrors `OnTransitionTriggerConfig`); `event` waits for `<entity>.record.{created,updated,
/// deleted}` (mirrors `OnRecordEventTriggerConfig`).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WaitEventTargetConfig {
    pub entity: String,
    #[serde(default)]
    pub action: Option<String>,
    #[serde(default)]
    pub event: Option<String>,
}

/// How a due firing gets from "claimed" to "executed". `Outbox` (default) is the reliable
/// path this crate originally shipped with: `claim_due_jobs` writes a `cron.job.due` outbox
/// event, `outbox-publisher` drains it onto RabbitMQ, `cron-scheduler`'s executor consumes it
/// — at-least-once, survives a `cron-scheduler` crash between claim and execution (the
/// message is durable and unacked until the executor actually runs it). `Direct` skips all of
/// that: the ticker calls the same execution logic in-process, in the same tick that claimed
/// the job. Cheaper and lower-latency, but genuinely fire-and-forget — if `cron-scheduler`
/// crashes between claiming and finishing execution, that firing is simply lost (no
/// redelivery, nothing durable outside the already-written `cron_job_runs` row). Not every
/// job needs the outbox's durability guarantee; a job an operator is fine losing an
/// occasional firing of (a low-stakes webhook ping, a best-effort cache warm) shouldn't pay
/// for it.
#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum DispatchMode {
    #[default]
    Outbox,
    Direct,
}

impl DispatchMode {
    pub fn as_str(self) -> &'static str {
        match self {
            DispatchMode::Outbox => "outbox",
            DispatchMode::Direct => "direct",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "outbox" => Some(DispatchMode::Outbox),
            "direct" => Some(DispatchMode::Direct),
            _ => None,
        }
    }
}

/// What fires a job. `Schedule` (default) is the original cron-expression firing this crate
/// shipped with. `OnTransition` (`docs/features/02-workflow-engine.md` Increment 1) fires
/// instead when a `<entity>.workflow.transitioned` event matches `trigger_config`'s
/// `entity`/`action` for this job's `tenant_id` — `cron_expr`/`next_run_at` are meaningless for
/// it and stay `None`. Same `cron_jobs` row, same `target_type`/`target_config`/`dispatch_mode`
/// dispatch path either way — only what *causes* the firing differs, matching the "evolve
/// metap-cron in place, don't fork a parallel system" decision recorded in that brief.
#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum TriggerType {
    #[default]
    Schedule,
    OnTransition,
    /// `docs/roadmap/38-generic-record-event-triggers.md` — fires on a plain
    /// `<entity>.record.{created,updated,deleted}` event instead of a workflow transition. Kept
    /// as its own trigger type rather than folding into `OnTransition` — `<entity>.record.*`
    /// events carry no `action`, so `OnTransitionTriggerConfig`'s shape doesn't fit, and
    /// existing `on_transition` rows must keep matching exactly as before.
    OnRecordEvent,
}

impl TriggerType {
    pub fn as_str(self) -> &'static str {
        match self {
            TriggerType::Schedule => "schedule",
            TriggerType::OnTransition => "on_transition",
            TriggerType::OnRecordEvent => "on_record_event",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "schedule" => Some(TriggerType::Schedule),
            "on_transition" => Some(TriggerType::OnTransition),
            "on_record_event" => Some(TriggerType::OnRecordEvent),
            _ => None,
        }
    }
}

/// `trigger_config` shape when `trigger_type = on_transition` — matched against the entity name
/// derived from a `<entity>.workflow.transitioned` routing key and the `action` field of its
/// payload. Exact match only (no wildcards) — a job fires for one specific transition action on
/// one specific entity, matching how `WorkflowTransition.action` itself is a single fixed string.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OnTransitionTriggerConfig {
    pub entity: String,
    pub action: String,
}

/// `trigger_config` shape when `trigger_type = on_record_event` — matched against the entity
/// name and event kind (`"created"`/`"updated"`/`"deleted"`) derived from a
/// `<entity>.record.{created,updated,deleted}` routing key. Exact match only, same posture as
/// `OnTransitionTriggerConfig`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OnRecordEventTriggerConfig {
    pub entity: String,
    /// `"created"` | `"updated"` | `"deleted"` — not its own enum here since the only place
    /// that needs to interpret it is the routing-key classifier in `cron-scheduler::trigger`,
    /// which already works in plain `&str`s (matching a routing key suffix).
    pub event: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct CronJob {
    pub id: Uuid,
    #[serde(rename = "tenantId")]
    pub tenant_id: Uuid,
    pub name: String,
    pub enabled: bool,
    #[serde(rename = "triggerType")]
    pub trigger_type: String,
    #[serde(rename = "triggerConfig")]
    pub trigger_config: Option<serde_json::Value>,
    #[serde(rename = "cronExpr")]
    pub cron_expr: Option<String>,
    pub timezone: String,
    #[serde(rename = "targetType")]
    pub target_type: String,
    #[serde(rename = "targetConfig")]
    pub target_config: serde_json::Value,
    #[serde(rename = "dispatchMode")]
    pub dispatch_mode: String,
    #[serde(rename = "maxAttempts")]
    pub max_attempts: i32,
    #[serde(rename = "retryBackoffSeconds")]
    pub retry_backoff_seconds: i32,
    #[serde(rename = "nextRunAt")]
    pub next_run_at: Option<DateTime<Utc>>,
    #[serde(rename = "lastRunAt")]
    pub last_run_at: Option<DateTime<Utc>>,
    #[serde(rename = "createdAt")]
    pub created_at: DateTime<Utc>,
    #[serde(rename = "updatedAt")]
    pub updated_at: DateTime<Utc>,
    #[serde(rename = "createdBy")]
    pub created_by: Option<Uuid>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RunStatus {
    Enqueued,
    Success,
    Failed,
    /// A `TargetType::Steps` firing paused at a `wait_event` step (Increment 3) —
    /// `executor::execute`'s idempotency check treats this the same as `Success`/`Failed`: a
    /// redelivered `cron.job.due` message for a run already `waiting` must not re-run the chain
    /// from scratch (it would re-execute already-completed steps, and would also collide with
    /// `workflow_runs_cron_job_run_idx`'s uniqueness by trying to `start_workflow_run` twice for
    /// the same `cron_job_run_id`).
    Waiting,
}

impl RunStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            RunStatus::Enqueued => "enqueued",
            RunStatus::Success => "success",
            RunStatus::Failed => "failed",
            RunStatus::Waiting => "waiting",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "enqueued" => Some(RunStatus::Enqueued),
            "success" => Some(RunStatus::Success),
            "failed" => Some(RunStatus::Failed),
            "waiting" => Some(RunStatus::Waiting),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct CronJobRun {
    pub id: Uuid,
    #[serde(rename = "tenantId")]
    pub tenant_id: Uuid,
    #[serde(rename = "jobId")]
    pub job_id: Uuid,
    pub status: String,
    pub attempt: i32,
    #[serde(rename = "scheduledFor")]
    pub scheduled_for: DateTime<Utc>,
    #[serde(rename = "startedAt")]
    pub started_at: Option<DateTime<Utc>>,
    #[serde(rename = "finishedAt")]
    pub finished_at: Option<DateTime<Utc>>,
    pub error: Option<String>,
    #[serde(rename = "responseSummary")]
    pub response_summary: Option<serde_json::Value>,
    #[serde(rename = "createdAt")]
    pub created_at: DateTime<Utc>,
}

/// The `cron.job.due` outbox/RabbitMQ payload shape — `cron-scheduler`'s ticker writes it,
/// its executor reads it. Kept here (not duplicated in `cron-scheduler`) so the two halves
/// can't drift apart on field names. Carries `max_attempts`/`retry_backoff_seconds`/
/// `dispatch_mode` alongside the firing itself so the executor can decide whether/how to
/// schedule a retry on failure without a second DB round-trip to re-fetch the owning job.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CronJobDuePayload {
    #[serde(rename = "runId")]
    pub run_id: Uuid,
    #[serde(rename = "jobId")]
    pub job_id: Uuid,
    #[serde(rename = "tenantId")]
    pub tenant_id: Uuid,
    #[serde(rename = "targetType")]
    pub target_type: String,
    #[serde(rename = "targetConfig")]
    pub target_config: serde_json::Value,
    pub attempt: i32,
    #[serde(rename = "maxAttempts")]
    pub max_attempts: i32,
    #[serde(rename = "retryBackoffSeconds")]
    pub retry_backoff_seconds: i32,
    #[serde(rename = "dispatchMode")]
    pub dispatch_mode: String,
    /// Which record's event caused this firing, when the job's `trigger_type` is
    /// `on_transition`/`on_record_event` — `None` for a plain `schedule` job, which has no
    /// single triggering record. Lets `run_email`/`run_webhook` reference the actual record
    /// (`recordId`/`entity`) rather than only the static `target_config`, e.g. an email body
    /// that says which order/issue/customer just changed.
    #[serde(rename = "triggerRecordId", skip_serializing_if = "Option::is_none")]
    pub trigger_record_id: Option<Uuid>,
    #[serde(rename = "triggerEntity", skip_serializing_if = "Option::is_none")]
    pub trigger_entity: Option<String>,
}

pub const ROUTING_KEY: &str = "cron.job.due";

/// A job `claim_due_jobs` claimed with `DispatchMode::Direct` — returned to the caller (the
/// ticker) instead of going out via outbox, since executing it is the ticker's own job now.
/// Same fields as `CronJobDuePayload` (kept as a separate type rather than reusing it so a
/// future divergence between "what the outbox payload carries" and "what a direct-dispatch
/// caller needs" doesn't have to be threaded back through the wire format).
#[derive(Debug, Clone)]
pub struct ClaimedDirectJob {
    pub run_id: Uuid,
    pub job_id: Uuid,
    pub tenant_id: Uuid,
    pub target_type: String,
    pub target_config: serde_json::Value,
    pub attempt: i32,
    pub max_attempts: i32,
    pub retry_backoff_seconds: i32,
    pub dispatch_mode: String,
    pub trigger_record_id: Option<Uuid>,
    pub trigger_entity: Option<String>,
}

/// Progress/audit state for one `TargetType::Steps` firing — one row per `cron_job_runs` row
/// that dispatched a `"steps"` job, not per step (a step's own outcome lands in `context`
/// instead of its own row). See `TargetType::Steps`'s doc comment for what this is/isn't for.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum WorkflowRunStatus {
    Running,
    Success,
    Failed,
    /// Paused at a `wait_event` step (Increment 3), `wait_entity`/`wait_action`/
    /// `wait_record_event` on the same row say what will resume it. See `RunStatus::Waiting`'s
    /// doc comment for why a matching `cron_job_runs` status exists too.
    Waiting,
}

impl WorkflowRunStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            WorkflowRunStatus::Running => "running",
            WorkflowRunStatus::Success => "success",
            WorkflowRunStatus::Failed => "failed",
            WorkflowRunStatus::Waiting => "waiting",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "running" => Some(WorkflowRunStatus::Running),
            "success" => Some(WorkflowRunStatus::Success),
            "failed" => Some(WorkflowRunStatus::Failed),
            "waiting" => Some(WorkflowRunStatus::Waiting),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct WorkflowRun {
    pub id: Uuid,
    #[serde(rename = "tenantId")]
    pub tenant_id: Uuid,
    #[serde(rename = "jobId")]
    pub job_id: Uuid,
    #[serde(rename = "cronJobRunId")]
    pub cron_job_run_id: Uuid,
    pub status: String,
    #[serde(rename = "currentStepIndex")]
    pub current_step_index: i32,
    #[serde(rename = "totalSteps")]
    pub total_steps: i32,
    pub context: serde_json::Value,
    pub error: Option<String>,
    /// Set only while `status == "waiting"` — the entity a `wait_event` step is pausing on,
    /// paired with exactly one of `wait_action`/`wait_record_event` below (mirrors
    /// `WaitEventTargetConfig`).
    #[serde(rename = "waitEntity")]
    pub wait_entity: Option<String>,
    #[serde(rename = "waitAction")]
    pub wait_action: Option<String>,
    #[serde(rename = "waitRecordEvent")]
    pub wait_record_event: Option<String>,
    #[serde(rename = "startedAt")]
    pub started_at: DateTime<Utc>,
    #[serde(rename = "finishedAt")]
    pub finished_at: Option<DateTime<Utc>>,
    #[serde(rename = "createdAt")]
    pub created_at: DateTime<Utc>,
}

/// One `workflow_runs` row a `dispatch_on_wait_event_*_matches` call resumed — everything
/// `cron-scheduler::executor::resume_steps` needs to continue the chain from
/// `resume_from_step_index` and, on eventual success/failure, close out the owning
/// `cron_job_runs` row (`finish_run`/`finish_run_with_retry`) without a second DB round-trip to
/// re-fetch the job. `record_id`/`entity` are the *resuming* event's, not the original firing's
/// (that context isn't persisted anywhere reachable at resume time — same accepted gap
/// `dispatch_claimed`'s doc comment already notes for a plain retry's trigger context).
#[derive(Debug, Clone)]
pub struct ResumedWorkflowRun {
    pub workflow_run_id: Uuid,
    pub cron_job_run_id: Uuid,
    pub job_id: Uuid,
    pub tenant_id: Uuid,
    pub resume_from_step_index: i32,
    pub target_config: serde_json::Value,
    pub dispatch_mode: String,
    pub max_attempts: i32,
    pub retry_backoff_seconds: i32,
    pub attempt: i32,
    pub resuming_record_id: Uuid,
    pub resuming_entity: String,
}
