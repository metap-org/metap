//! `workflow_transition`/`bulk_query_action` targets — both call back into the owning
//! `crm-server`'s own `/api/:entity/...` HTTP surface via the shared `transition_one` (GET the
//! record for its `version`, then POST the transition), reusing its permission/validation/audit
//! rather than linking `metap-crud` directly (see `super`'s doc comment).

use std::collections::HashMap;

use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use super::config::ExecutorConfig;

#[derive(Deserialize)]
struct WorkflowTransitionConfig {
    entity: String,
    #[serde(rename = "recordId")]
    record_id: Uuid,
    action: String,
}

pub(crate) async fn run_workflow_transition(
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

pub(crate) async fn run_bulk_query_action(
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
