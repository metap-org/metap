//! Consumes `cron.job.due` (`crates/metap-cron`'s `ROUTING_KEY`) and actually runs the job.
//! `workflow_transition`/`bulk_query_action` call back into the owning `crm-server`'s own
//! `/api/:entity/...` HTTP surface — reusing its permission checks, field validation,
//! optimistic-locking, and workflow audit trail for free — rather than this binary linking
//! `metap-crud`/`metap-metadata` directly, which would give an ops binary business-entity
//! knowledge (`CLAUDE.md`'s boundary rules forbid that). `webhook` calls an arbitrary
//! external URL instead.

use std::collections::HashMap;

use futures_util::StreamExt;
use metap_cron::{CronJobDuePayload, RunStatus, TargetType, ROUTING_KEY};
use metap_infra::EventBus;
use serde::Deserialize;
use serde_json::{json, Value};
use sqlx::PgPool;
use uuid::Uuid;

pub const QUEUE: &str = "cron.executor";

#[derive(Debug, Clone)]
pub struct ExecutorConfig {
    /// Base URL of the `crm-server` instance `workflow_transition`/`bulk_query_action` jobs
    /// call back into.
    pub target_base_url: String,
    /// A pre-minted JWT (`pnpm mint-token`) for a service account with whatever role the jobs
    /// it will run need. Its `tenantId` claim fixes which tenant's jobs this executor can
    /// actually run: `crm-server` resolves tenant scope from the token alone, never from a
    /// caller-supplied value, so a job whose `tenant_id` doesn't match this token's tenant
    /// fails at execution time (record/entity not found) rather than silently crossing
    /// tenants — see `docs/roadmap.md` Phase 13 for this constraint.
    pub service_jwt: String,
}

/// Runs the consume-execute loop until `shutdown` resolves. Mirrors
/// `notification_worker::run`'s shape (subscribe, `tokio::select!` with a biased shutdown
/// branch, propagate an error on unexpected stream close so a process manager can restart).
pub async fn run_executor(
    bus: &impl EventBus,
    pool: &PgPool,
    http: &reqwest::Client,
    config: &ExecutorConfig,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()> {
    let mut events = bus.subscribe(QUEUE, ROUTING_KEY).await?;
    let mut shutdown = std::pin::pin!(shutdown);

    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting executor");
                return Ok(());
            }
            event = events.next() => {
                let Some(event) = event else {
                    anyhow::bail!("event stream closed unexpectedly (bus disconnected?)");
                };
                match serde_json::from_value::<CronJobDuePayload>(event.payload.clone()) {
                    Ok(payload) => {
                        execute(pool, http, config, &payload).await;
                        event.ack().await.ok();
                    }
                    Err(err) => {
                        tracing::warn!(error = %err, "malformed cron.job.due payload, routed to dead-letter queue");
                        event.nack(false).await.ok();
                    }
                }
            }
        }
    }
}

/// Runs one job to completion and records its result — shared by the outbox-consuming loop
/// above and the ticker's `Direct`-dispatch path (`ticker.rs`), so "how a job actually gets
/// executed" has exactly one implementation regardless of which `DispatchMode` got it here.
pub async fn execute(pool: &PgPool, http: &reqwest::Client, config: &ExecutorConfig, payload: &CronJobDuePayload) {
    if let Err(err) = metap_cron::start_run(pool, payload.run_id).await {
        tracing::error!(run_id = %payload.run_id, error = %err, "failed to mark cron job run started");
    }

    let (status, error, summary) = match dispatch(http, config, payload).await {
        Ok(summary) => (RunStatus::Success, None, Some(summary)),
        Err(err) => (RunStatus::Failed, Some(err.to_string()), None),
    };

    if status == RunStatus::Failed {
        tracing::warn!(job_id = %payload.job_id, run_id = %payload.run_id, error = ?error, "cron job execution failed");
    } else {
        tracing::info!(job_id = %payload.job_id, run_id = %payload.run_id, "cron job executed");
    }

    if let Err(err) = metap_cron::finish_run_with_retry(pool, payload, status, error.as_deref(), summary).await {
        tracing::error!(run_id = %payload.run_id, error = %err, "failed to record cron job run result");
    }
}

async fn dispatch(
    http: &reqwest::Client,
    config: &ExecutorConfig,
    payload: &CronJobDuePayload,
) -> anyhow::Result<Value> {
    let Some(target_type) = TargetType::parse(&payload.target_type) else {
        anyhow::bail!("unknown target_type {:?}", payload.target_type);
    };
    match target_type {
        TargetType::WorkflowTransition => run_workflow_transition(http, config, &payload.target_config).await,
        TargetType::BulkQueryAction => run_bulk_query_action(http, config, &payload.target_config).await,
        TargetType::Webhook => run_webhook(http, payload).await,
    }
}

#[derive(Deserialize)]
struct WorkflowTransitionConfig {
    entity: String,
    #[serde(rename = "recordId")]
    record_id: Uuid,
    action: String,
}

async fn run_workflow_transition(
    http: &reqwest::Client,
    config: &ExecutorConfig,
    target_config: &Value,
) -> anyhow::Result<Value> {
    let cfg: WorkflowTransitionConfig = serde_json::from_value(target_config.clone())?;
    transition_one(http, config, &cfg.entity, cfg.record_id, &cfg.action).await
}

#[derive(Deserialize)]
struct BulkQueryActionConfig {
    entity: String,
    #[serde(default)]
    filter: HashMap<String, String>,
    action: String,
}

async fn run_bulk_query_action(
    http: &reqwest::Client,
    config: &ExecutorConfig,
    target_config: &Value,
) -> anyhow::Result<Value> {
    let cfg: BulkQueryActionConfig = serde_json::from_value(target_config.clone())?;
    let base = config.target_base_url.trim_end_matches('/');

    let list: Value = http
        .get(format!("{base}/api/{}", cfg.entity))
        .bearer_auth(&config.service_jwt)
        .query(&[("limit", "200")])
        .query(&cfg.filter)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;

    let records = list.get("data").and_then(Value::as_array).cloned().unwrap_or_default();
    let mut succeeded = 0usize;
    let mut failed: Vec<Value> = Vec::new();
    for record in &records {
        let Some(id) = record
            .get("id")
            .and_then(Value::as_str)
            .and_then(|s| Uuid::parse_str(s).ok())
        else {
            continue;
        };
        match transition_one(http, config, &cfg.entity, id, &cfg.action).await {
            Ok(_) => succeeded += 1,
            Err(err) => failed.push(json!({ "id": id, "error": err.to_string() })),
        }
    }

    Ok(json!({ "matched": records.len(), "succeeded": succeeded, "failed": failed }))
}

/// GET-then-transition: the transition endpoint requires the record's current `version` for
/// optimistic locking (`crates/metap-http/src/routes/records.rs`), so this always reads
/// first — same two-step every other transition caller (the frontend included) has to do.
async fn transition_one(
    http: &reqwest::Client,
    config: &ExecutorConfig,
    entity: &str,
    record_id: Uuid,
    action: &str,
) -> anyhow::Result<Value> {
    let base = config.target_base_url.trim_end_matches('/');

    let record: Value = http
        .get(format!("{base}/api/{entity}/{record_id}"))
        .bearer_auth(&config.service_jwt)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;
    let version = record
        .get("data")
        .and_then(|d| d.get("version"))
        .and_then(Value::as_i64)
        .ok_or_else(|| anyhow::anyhow!("record {record_id} response had no version field"))?;

    let response = http
        .post(format!("{base}/api/{entity}/{record_id}/transitions/{action}"))
        .bearer_auth(&config.service_jwt)
        .json(&json!({ "version": version }))
        .send()
        .await?
        .error_for_status()?
        .json::<Value>()
        .await?;
    Ok(response)
}

#[derive(Deserialize)]
struct WebhookConfig {
    url: String,
    #[serde(default = "default_method")]
    method: String,
    #[serde(default)]
    headers: HashMap<String, String>,
    #[serde(default)]
    body: Option<Value>,
}

fn default_method() -> String {
    "POST".to_string()
}

async fn run_webhook(http: &reqwest::Client, payload: &CronJobDuePayload) -> anyhow::Result<Value> {
    let cfg: WebhookConfig = serde_json::from_value(payload.target_config.clone())?;
    let method = reqwest::Method::from_bytes(cfg.method.as_bytes())
        .map_err(|_| anyhow::anyhow!("invalid HTTP method {:?}", cfg.method))?;

    let mut body = cfg.body.unwrap_or_else(|| json!({}));
    if let Value::Object(map) = &mut body {
        map.insert("jobId".to_string(), json!(payload.job_id));
        map.insert("runId".to_string(), json!(payload.run_id));
    }

    let mut req = http.request(method, &cfg.url).json(&body);
    for (key, value) in &cfg.headers {
        req = req.header(key, value);
    }

    let response = req.send().await?;
    let status = response.status();
    let text = response.text().await.unwrap_or_default();
    if !status.is_success() {
        anyhow::bail!("webhook returned {status}: {}", truncate(&text, 500));
    }
    Ok(json!({ "status": status.as_u16(), "body": truncate(&text, 2000) }))
}

fn truncate(s: &str, max: usize) -> String {
    if s.len() <= max {
        s.to_string()
    } else {
        format!("{}...", &s[..max])
    }
}
