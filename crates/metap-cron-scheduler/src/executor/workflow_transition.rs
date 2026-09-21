//! `workflow_transition`/`bulk_query_action` targets — both call back into the owning app's
//! entity CRUD via `metap-grpc::client::GrpcBackend` (`config.target_grpc_backend`), reusing its
//! permission/validation/audit rather than linking `metap-crud`/`metap-metadata` directly (see
//! `super`'s doc comment). **Moved off REST 2026-09-21** — `metap-http` dropped its generic
//! `/api/:entity*` CRUD surface in favor of GraphQL-only entity access (this crate's own
//! doc comment on that removal has the full reasoning); `RecordBackend` is the exact seam
//! `metap-graphql`/`metap-graphql-gateway` already call the same operations through, so this is
//! the same client already proven elsewhere in this workspace, not a new pattern.

use std::collections::HashMap;
use std::sync::Arc;

use metap_crud::{RecordBackend, RecordDto, ServiceResult};
use metap_permission::RequestContext;
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use super::config::ExecutorConfig;

/// A client-side `RequestContext` for a call this process makes on its own behalf, not on behalf
/// of a live inbound request — `forwarded_bearer_token: None` so `GrpcBackend` always signs with
/// `config.service_token`, and every other field is unused: `GrpcRecordService` (the gRPC server
/// side) derives its own `RequestContext` from the bearer token's claims and never reads what a
/// caller sends here (see that crate's `service.rs`, every RPC opens with
/// `authenticate(request.metadata(), ...)` before even decoding the request body).
fn service_context() -> RequestContext {
    RequestContext {
        tenant_id: String::new(),
        user_id: None,
        roles: None,
        function_id: None,
        context_attributes: None,
        forwarded_bearer_token: None,
    }
}

/// Unwraps a `ServiceResult` into a plain `anyhow::Result`, since every caller in this file
/// already treats a business-level `Err` (permission denied, validation failed, version
/// conflict, ...) the same way it treats a transport-level one: fail this job/step with a
/// message, nothing typed to recover from downstream.
fn unwrap_result<T>(result: ServiceResult<T>) -> anyhow::Result<T> {
    match result {
        ServiceResult::Ok { data, .. } => Ok(data),
        ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        } => anyhow::bail!(
            "{status} {error}{}{}",
            message.map(|m| format!(": {m}")).unwrap_or_default(),
            field_errors
                .map(|fe| format!(" (field_errors: {fe:?})"))
                .unwrap_or_default()
        ),
    }
}

fn target_grpc_backend(config: &ExecutorConfig) -> anyhow::Result<&Arc<dyn RecordBackend>> {
    config.target_grpc_backend.as_ref().ok_or_else(|| {
        anyhow::anyhow!(
            "CRON_TARGET_GRPC_ADDR is unset or the initial connection to it failed — \
             workflow_transition/bulk_query_action jobs cannot run"
        )
    })
}

#[derive(Deserialize)]
struct WorkflowTransitionConfig {
    entity: String,
    #[serde(rename = "recordId")]
    record_id: Uuid,
    action: String,
}

pub(crate) async fn run_workflow_transition(config: &ExecutorConfig, target_config: &Value) -> anyhow::Result<Value> {
    let cfg: WorkflowTransitionConfig = serde_json::from_value(target_config.clone())?;
    let record = transition_one(config, &cfg.entity, cfg.record_id, &cfg.action).await?;
    Ok(serde_json::to_value(record)?)
}

#[derive(Deserialize)]
struct BulkQueryActionConfig {
    entity: String,
    #[serde(default)]
    filter: HashMap<String, String>,
    action: String,
}

pub(crate) async fn run_bulk_query_action(config: &ExecutorConfig, target_config: &Value) -> anyhow::Result<Value> {
    let cfg: BulkQueryActionConfig = serde_json::from_value(target_config.clone())?;
    let backend = target_grpc_backend(config)?;

    let input = metap_query::ListInput {
        limit: 200,
        sort: None,
        filters: cfg.filter.into_iter().collect(),
        cursor: None,
        list_view: None,
        jql: None,
    };
    let records: Vec<RecordDto> = unwrap_result(backend.list(&cfg.entity, &input, &service_context()).await?)?;

    let mut succeeded = 0usize;
    let mut failed: Vec<Value> = Vec::new();
    for record in &records {
        match transition_one(config, &cfg.entity, record.id, &cfg.action).await {
            Ok(_) => succeeded += 1,
            Err(err) => failed.push(json!({ "id": record.id, "error": err.to_string() })),
        }
    }

    Ok(json!({ "matched": records.len(), "succeeded": succeeded, "failed": failed }))
}

/// Get-then-transition: the transition RPC requires the record's current `version` for
/// optimistic locking, so this always reads first — same two-step every other transition caller
/// (the frontend included) has to do.
async fn transition_one(
    config: &ExecutorConfig,
    entity: &str,
    record_id: Uuid,
    action: &str,
) -> anyhow::Result<RecordDto> {
    let backend = target_grpc_backend(config)?;
    let ctx = service_context();

    let (record, _capabilities) = unwrap_result(backend.get(entity, record_id, &ctx).await?)?;
    let result = backend
        .transition(entity, record_id, action, record.version, None, &ctx, None)
        .await?;
    unwrap_result(result)
}
