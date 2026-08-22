//! Admin-gated CRUD over `metap_cron`'s job store (`docs/roadmap.md` Phase 13). Same shape as
//! `routes/admin.rs`: every handler uses `AdminContext`, tenant always comes from the token
//! via `state.permissions.scoped_tenant`, never from the request body/path.

use axum::extract::{Path, Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use metap_cron::{DispatchMode, JobUpdate, NewCronJob, OnTransitionTriggerConfig, TargetType, TriggerType};
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use crate::auth::AdminContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

fn not_found() -> Response {
    service_error_response(404, "cron_job_not_found", Some("Cron job not found."), None)
}

fn validation_error(message: &str) -> Response {
    service_error_response(400, "validation_failed", Some(message), None)
}

#[derive(Deserialize)]
struct CreateCronJobBody {
    name: String,
    #[serde(rename = "triggerType", default = "default_trigger_type")]
    trigger_type: String,
    #[serde(rename = "triggerConfig")]
    trigger_config: Option<Value>,
    #[serde(rename = "cronExpr")]
    cron_expr: Option<String>,
    #[serde(default = "default_timezone")]
    timezone: String,
    #[serde(rename = "targetType")]
    target_type: String,
    #[serde(rename = "targetConfig")]
    target_config: Value,
    #[serde(rename = "dispatchMode", default = "default_dispatch_mode")]
    dispatch_mode: String,
    #[serde(rename = "maxAttempts", default = "default_max_attempts")]
    max_attempts: i32,
    #[serde(rename = "retryBackoffSeconds", default = "default_retry_backoff_seconds")]
    retry_backoff_seconds: i32,
    #[serde(default = "default_true")]
    enabled: bool,
}

fn default_trigger_type() -> String {
    TriggerType::Schedule.as_str().to_string()
}

fn default_timezone() -> String {
    "UTC".to_string()
}

fn default_dispatch_mode() -> String {
    DispatchMode::Outbox.as_str().to_string()
}

fn default_max_attempts() -> i32 {
    1
}

fn default_retry_backoff_seconds() -> i32 {
    30
}

fn default_true() -> bool {
    true
}

/// Shared by create/update: `triggerType: "schedule"` needs a valid `cronExpr`/`timezone` (the
/// existing behavior, unchanged); `triggerType: "on_transition"` instead needs `triggerConfig`
/// to parse as `{entity, action}` with both non-empty — `cronExpr` is meaningless for it and
/// isn't validated. Returns `Err` with the response to send on the first validation failure.
fn validate_trigger(
    trigger_type: &str,
    trigger_config: Option<&Value>,
    cron_expr: Option<&str>,
    timezone: &str,
) -> Result<(), Box<Response>> {
    match TriggerType::parse(trigger_type) {
        Some(TriggerType::Schedule) => {
            let Some(cron_expr) = cron_expr else {
                return Err(Box::new(validation_error(
                    "`cronExpr` is required when `triggerType` is \"schedule\".",
                )));
            };
            metap_cron::validate_schedule(cron_expr, timezone).map_err(|e| Box::new(validation_error(&e.to_string())))
        }
        Some(TriggerType::OnTransition) => {
            let Some(trigger_config) = trigger_config else {
                return Err(Box::new(validation_error(
                    "`triggerConfig` ({entity, action}) is required when `triggerType` is \"on_transition\".",
                )));
            };
            let cfg: OnTransitionTriggerConfig = serde_json::from_value(trigger_config.clone()).map_err(|_| {
                Box::new(validation_error(
                    "`triggerConfig` must be `{ entity: string, action: string }`.",
                ))
            })?;
            if cfg.entity.trim().is_empty() || cfg.action.trim().is_empty() {
                return Err(Box::new(validation_error(
                    "`triggerConfig.entity`/`triggerConfig.action` must not be empty.",
                )));
            }
            Ok(())
        }
        None => Err(Box::new(validation_error(
            "`triggerType` must be one of: schedule, on_transition.",
        ))),
    }
}

async fn create_cron_job(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<CreateCronJobBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    if TargetType::parse(&body.target_type).is_none() {
        return validation_error("`targetType` must be one of: workflow_transition, bulk_query_action, webhook.");
    }
    if DispatchMode::parse(&body.dispatch_mode).is_none() {
        return validation_error("`dispatchMode` must be one of: outbox, direct.");
    }
    if body.max_attempts < 1 {
        return validation_error("`maxAttempts` must be at least 1.");
    }
    if body.retry_backoff_seconds < 0 {
        return validation_error("`retryBackoffSeconds` must be non-negative.");
    }
    if let Err(response) = validate_trigger(
        &body.trigger_type,
        body.trigger_config.as_ref(),
        body.cron_expr.as_deref(),
        &body.timezone,
    ) {
        return *response;
    }

    let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    let input = NewCronJob {
        name: body.name,
        trigger_type: body.trigger_type,
        trigger_config: body.trigger_config,
        cron_expr: body.cron_expr,
        timezone: body.timezone,
        target_type: body.target_type,
        target_config: body.target_config,
        dispatch_mode: body.dispatch_mode,
        max_attempts: body.max_attempts,
        retry_backoff_seconds: body.retry_backoff_seconds,
        enabled: body.enabled,
    };
    match metap_cron::create_job(&state.pool, tenant_id, input, created_by).await {
        Ok(job) => (StatusCode::CREATED, Json(json!({ "data": job }))).into_response(),
        Err(e) => internal_error_response(e),
    }
}

async fn list_cron_jobs(State(state): State<AppState>, AdminContext(context): AdminContext) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    match metap_cron::list_jobs(&state.pool, tenant_id).await {
        Ok(jobs) => Json(json!({ "data": jobs })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

async fn get_cron_job(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    match metap_cron::get_job(&state.pool, tenant_id, id).await {
        Ok(Some(job)) => Json(json!({ "data": job })).into_response(),
        Ok(None) => not_found(),
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize, Default)]
struct UpdateCronJobBody {
    name: Option<String>,
    #[serde(rename = "triggerType")]
    trigger_type: Option<String>,
    #[serde(rename = "triggerConfig")]
    trigger_config: Option<Value>,
    #[serde(rename = "cronExpr")]
    cron_expr: Option<String>,
    timezone: Option<String>,
    #[serde(rename = "targetType")]
    target_type: Option<String>,
    #[serde(rename = "targetConfig")]
    target_config: Option<Value>,
    #[serde(rename = "dispatchMode")]
    dispatch_mode: Option<String>,
    #[serde(rename = "maxAttempts")]
    max_attempts: Option<i32>,
    #[serde(rename = "retryBackoffSeconds")]
    retry_backoff_seconds: Option<i32>,
    enabled: Option<bool>,
}

async fn update_cron_job(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    AdminContext(context): AdminContext,
    Json(body): Json<UpdateCronJobBody>,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    if let Some(target_type) = &body.target_type {
        if TargetType::parse(target_type).is_none() {
            return validation_error("`targetType` must be one of: workflow_transition, bulk_query_action, webhook.");
        }
    }
    if let Some(dispatch_mode) = &body.dispatch_mode {
        if DispatchMode::parse(dispatch_mode).is_none() {
            return validation_error("`dispatchMode` must be one of: outbox, direct.");
        }
    }
    if let Some(max_attempts) = body.max_attempts {
        if max_attempts < 1 {
            return validation_error("`maxAttempts` must be at least 1.");
        }
    }
    if let Some(retry_backoff_seconds) = body.retry_backoff_seconds {
        if retry_backoff_seconds < 0 {
            return validation_error("`retryBackoffSeconds` must be non-negative.");
        }
    }
    // Validate the *effective* trigger (existing value if the request didn't change it) so an
    // update can't leave a job with a schedule `metap_cron::next_run_at` can't compute, or an
    // on_transition job with a malformed `triggerConfig`.
    if body.trigger_type.is_some()
        || body.trigger_config.is_some()
        || body.cron_expr.is_some()
        || body.timezone.is_some()
    {
        let existing = match metap_cron::get_job(&state.pool, tenant_id, id).await {
            Ok(Some(job)) => job,
            Ok(None) => return not_found(),
            Err(e) => return internal_error_response(e),
        };
        let trigger_type = body.trigger_type.as_deref().unwrap_or(&existing.trigger_type);
        let trigger_config = body.trigger_config.as_ref().or(existing.trigger_config.as_ref());
        let cron_expr = body.cron_expr.as_deref().or(existing.cron_expr.as_deref());
        let timezone = body.timezone.as_deref().unwrap_or(&existing.timezone);
        if let Err(response) = validate_trigger(trigger_type, trigger_config, cron_expr, timezone) {
            return *response;
        }
    }

    let update = JobUpdate {
        name: body.name,
        trigger_type: body.trigger_type,
        trigger_config: body.trigger_config,
        cron_expr: body.cron_expr,
        timezone: body.timezone,
        target_type: body.target_type,
        target_config: body.target_config,
        dispatch_mode: body.dispatch_mode,
        max_attempts: body.max_attempts,
        retry_backoff_seconds: body.retry_backoff_seconds,
        enabled: body.enabled,
    };
    match metap_cron::update_job(&state.pool, tenant_id, id, update).await {
        Ok(Some(job)) => Json(json!({ "data": job })).into_response(),
        Ok(None) => not_found(),
        Err(e) => internal_error_response(e),
    }
}

async fn delete_cron_job(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    match metap_cron::delete_job(&state.pool, tenant_id, id).await {
        Ok(()) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => internal_error_response(e),
    }
}

async fn list_cron_job_runs(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    Query(params): Query<std::collections::HashMap<String, String>>,
    AdminContext(context): AdminContext,
) -> Response {
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let limit = params
        .get("limit")
        .and_then(|v| v.parse::<i64>().ok())
        .filter(|n| *n > 0 && *n <= 200)
        .unwrap_or(50);
    match metap_cron::list_job_runs(&state.pool, tenant_id, id, limit).await {
        Ok(runs) => Json(json!({ "data": runs })).into_response(),
        Err(e) => internal_error_response(e),
    }
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/admin/cron-jobs", get(list_cron_jobs).post(create_cron_job))
        .route(
            "/admin/cron-jobs/{id}",
            get(get_cron_job).patch(update_cron_job).delete(delete_cron_job),
        )
        .route("/admin/cron-jobs/{id}/runs", get(list_cron_job_runs))
}
