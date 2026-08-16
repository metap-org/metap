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
}

impl TargetType {
    pub fn as_str(self) -> &'static str {
        match self {
            TargetType::WorkflowTransition => "workflow_transition",
            TargetType::BulkQueryAction => "bulk_query_action",
            TargetType::Webhook => "webhook",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "workflow_transition" => Some(TargetType::WorkflowTransition),
            "bulk_query_action" => Some(TargetType::BulkQueryAction),
            "webhook" => Some(TargetType::Webhook),
            _ => None,
        }
    }
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

#[derive(Debug, Clone, Serialize)]
pub struct CronJob {
    pub id: Uuid,
    #[serde(rename = "tenantId")]
    pub tenant_id: Uuid,
    pub name: String,
    pub enabled: bool,
    #[serde(rename = "cronExpr")]
    pub cron_expr: String,
    pub timezone: String,
    #[serde(rename = "targetType")]
    pub target_type: String,
    #[serde(rename = "targetConfig")]
    pub target_config: serde_json::Value,
    #[serde(rename = "dispatchMode")]
    pub dispatch_mode: String,
    #[serde(rename = "nextRunAt")]
    pub next_run_at: DateTime<Utc>,
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
}

impl RunStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            RunStatus::Enqueued => "enqueued",
            RunStatus::Success => "success",
            RunStatus::Failed => "failed",
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
/// can't drift apart on field names.
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
}
