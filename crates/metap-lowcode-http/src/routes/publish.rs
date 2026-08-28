//! Publish/preview/rollback and their read counterparts (`GET .../published`, `GET
//! .../versions`) — the handlers that actually change (or preview changing) which version of an
//! entity is live. `apply_registry` is the one function outside this crate ever calls directly
//! (`pub` at the crate root too, re-exported from `lib.rs`).

use std::sync::Arc;

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::Json;
use metap_http::auth::AdminContext;
use metap_http::error::{internal_error_response, service_error_response};
use metap_http::AppState;
use metap_lowcode::audit::{self, AuditAction, AuditActor, AuditVersionInfo};
use metap_lowcode::PublishError;
use metap_metadata::MetadataRegistry;
use serde::Deserialize;
use serde_json::json;
use sqlx::PgPool;

use crate::resolve_pool;

pub(crate) fn publish_error_response(err: PublishError) -> Response {
    match err {
        PublishError::NoDraft => service_error_response(
            404,
            "lowcode_draft_not_found",
            Some("No draft exists for this entity."),
            None,
        ),
        PublishError::NameReservedByCodeEntity => service_error_response(
            409,
            "lowcode_name_reserved",
            Some("This entity name is already used by a code-authored entity."),
            None,
        ),
        PublishError::VersionNotFound(v) => service_error_response(
            404,
            "lowcode_version_not_found",
            Some(&format!("Version {v} does not exist for this entity.")),
            None,
        ),
        PublishError::Invalid(e) => {
            service_error_response(422, "lowcode_validation_failed", Some(&e.to_string()), None)
        }
        PublishError::Db(e) => internal_error_response(e),
    }
}

/// Swaps `registry` into `state.metadata`'s live `ArcSwap`, then reconciles indexes for the
/// new entity list — reused from `apps/crm-server`'s own boot sequence
/// (`metap_peripherals::reconcile_indexes`), not reimplemented here. Does *not* re-run
/// `check_metadata_drift`: that check only concerns code-authored entities, which never
/// change at runtime.
///
/// Takes the registry as a parameter — the caller passes `PublishOutcome::registry`, the
/// exact same already-validated merge `metap_lowcode::publish`/`rollback` just built to check
/// the write it made, rather than this function re-querying `list_all_published` and
/// re-running `merge_with` from scratch. That used to mean every publish/rollback paid for
/// the same DB query and registry build twice, and (worse) meant this function could itself
/// fail *after* the version row was already durably committed, leaving Postgres and the live
/// registry disagreeing with no way to retry just the reload half. Reusing the registry here
/// makes this function infallible (`store`/`reconcile_indexes` can't fail outwardly), so that
/// failure mode no longer exists.
///
/// Takes `pool` explicitly (the caller's `resolve_pool`-resolved pool) rather than reaching for
/// `state.pool` itself, same reasoning as every other handler in this file.
pub async fn apply_registry(state: &AppState, pool: &PgPool, registry: MetadataRegistry) {
    let entities = registry.list_entities();
    state.metadata.store(Arc::new(registry));
    metap_peripherals::reconcile_indexes(pool, &entities).await;
}

pub(crate) async fn publish(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match metap_lowcode::publish(&pool, &name, &state.metadata_base).await {
        Ok(outcome) => {
            let version_number = outcome.version_number;
            apply_registry(&state, &pool, outcome.registry).await;
            audit::record(
                &pool,
                &name,
                AuditAction::Published,
                &AuditActor {
                    user_id: context.user_id.clone(),
                    tenant_id: context.tenant_id.clone(),
                },
                AuditVersionInfo {
                    version_number: Some(version_number),
                    restored_from_version: None,
                },
            )
            .await;
            Json(json!({ "data": { "versionNumber": version_number } })).into_response()
        }
        Err(e) => publish_error_response(e),
    }
}

#[derive(Deserialize)]
pub(crate) struct RollbackBody {
    #[serde(rename = "toVersionNumber")]
    to_version_number: i32,
}

/// `docs/roadmap.md` Phase 11 Phase B's publish preview/validation report — runs the exact
/// checks `publish` would (shape, name-reservation, cross-reference) with no side effect, so
/// an operator can validate a draft before committing to a new published version.
pub(crate) async fn preview_publish(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match metap_lowcode::preview_publish(&pool, &name, &state.metadata_base).await {
        Ok(preview) => Json(json!({
            "data": { "wouldBeVersion": preview.would_be_version, "valid": true, "impact": preview.impact }
        }))
        .into_response(),
        Err(e) => publish_error_response(e),
    }
}

pub(crate) async fn rollback(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
    Json(body): Json<RollbackBody>,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match metap_lowcode::rollback(&pool, &name, body.to_version_number, &state.metadata_base).await {
        Ok(outcome) => {
            let version_number = outcome.version_number;
            apply_registry(&state, &pool, outcome.registry).await;
            audit::record(
                &pool,
                &name,
                AuditAction::RolledBack,
                &AuditActor {
                    user_id: context.user_id.clone(),
                    tenant_id: context.tenant_id.clone(),
                },
                AuditVersionInfo {
                    version_number: Some(version_number),
                    restored_from_version: Some(body.to_version_number),
                },
            )
            .await;
            Json(json!({ "data": { "versionNumber": version_number } })).into_response()
        }
        Err(e) => publish_error_response(e),
    }
}

pub(crate) async fn get_published(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match metap_lowcode::get_published(&pool, &name).await {
        Ok(Some(v)) => Json(json!({
            "data": {
                "versionNumber": v.version_number,
                "definition": v.definition,
                "publishedAt": v.published_at,
                "restoredFromVersion": v.restored_from_version,
            }
        }))
        .into_response(),
        Ok(None) => service_error_response(404, "lowcode_published_not_found", None, None),
        Err(e) => internal_error_response(e),
    }
}

pub(crate) async fn list_versions(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match metap_lowcode::list_versions(&pool, &name).await {
        Ok(versions) => {
            let data: Vec<_> = versions
                .into_iter()
                .map(|v| {
                    json!({
                        "versionNumber": v.version_number,
                        "publishedAt": v.published_at,
                        "restoredFromVersion": v.restored_from_version,
                    })
                })
                .collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}
