//! Admin-gated HTTP surface for `metap-lowcode`'s draft/publish/rollback storage
//! (`docs/roadmap.md` Phase 11 / Phase A sub-project 4, retargeted from
//! `docs/low-code-metadata-storage-design.md`). Same shape as `routes/admin.rs`/
//! `routes/cron.rs`: every handler uses `AdminContext`. Unlike `routes/cron.rs`, nothing here
//! is tenant-scoped — DB-authored entity metadata is global by design for Phase A (see the
//! spec's "Các quyết định đã chốt"), so there's no `tenant_id` in any query.
//!
//! `publish`/`rollback` are the only handlers that mutate `state.metadata` — both call
//! `reload_metadata` after `metap_lowcode` writes a new version, which rebuilds the merged
//! registry from `state.metadata_base` (code-authored) plus every currently-published
//! DB-authored entity and swaps it into `state.metadata`'s `ArcSwap` before the handler
//! responds. No restart required (Phase A sub-project 2) — any request after the response
//! comes back is guaranteed to see the new registry.

use std::sync::Arc;

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use metap_lowcode::{LowCodeEntityDefinition, PublishError};
use metap_metadata::{EntityField, EntityListView};
use serde::Deserialize;
use serde_json::json;

use crate::auth::AdminContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

#[derive(Deserialize)]
struct DraftBody {
    label: String,
    #[serde(default)]
    fields: Vec<EntityField>,
    #[serde(rename = "listViews", default)]
    list_views: Vec<EntityListView>,
}

fn publish_error_response(err: PublishError) -> Response {
    match err {
        PublishError::NoDraft => {
            service_error_response(404, "lowcode_draft_not_found", Some("No draft exists for this entity."), None)
        }
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

/// Rebuilds the merged runtime registry (`state.metadata_base` + every currently-published
/// DB-authored entity, read fresh from the DB) and swaps it into `state.metadata`, then
/// reconciles indexes for the new entity list — reused from `apps/crm-server`'s own boot
/// sequence (`metap_peripherals::reconcile_indexes`), not reimplemented here. Does *not*
/// re-run `check_metadata_drift`: that check only concerns code-authored entities, which
/// never change at runtime.
async fn reload_metadata(state: &AppState) -> anyhow::Result<()> {
    let db_entities: Vec<_> = metap_lowcode::list_all_published(&state.pool)
        .await?
        .into_iter()
        .map(|(_, def)| def.to_entity_definition())
        .collect();
    let merged = state.metadata_base.merge_with(db_entities)?;
    let entities = merged.list_entities();
    state.metadata.store(Arc::new(merged));
    metap_peripherals::reconcile_indexes(&state.pool, &entities).await;
    Ok(())
}

async fn list_entities(State(state): State<AppState>, AdminContext(_context): AdminContext) -> Response {
    let published = match metap_lowcode::list_all_published(&state.pool).await {
        Ok(p) => p,
        Err(e) => return internal_error_response(e),
    };
    let drafts = match metap_lowcode::list_draft_names(&state.pool).await {
        Ok(d) => d,
        Err(e) => return internal_error_response(e),
    };
    let published_names: Vec<&str> = published.iter().map(|(name, _)| name.as_str()).collect();
    Json(json!({ "data": { "published": published_names, "drafts": drafts } })).into_response()
}

async fn save_draft(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
    Json(body): Json<DraftBody>,
) -> Response {
    let definition = LowCodeEntityDefinition {
        name: name.clone(),
        label: body.label,
        fields: body.fields,
        list_views: body.list_views,
    };
    match metap_lowcode::save_draft(&state.pool, &name, &definition).await {
        Ok(()) => Json(json!({ "data": definition })).into_response(),
        Err(e) => publish_error_response(e),
    }
}

async fn get_draft(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
) -> Response {
    match metap_lowcode::get_draft(&state.pool, &name).await {
        Ok(Some(def)) => Json(json!({ "data": def })).into_response(),
        Ok(None) => service_error_response(404, "lowcode_draft_not_found", None, None),
        Err(e) => internal_error_response(e),
    }
}

async fn publish(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
) -> Response {
    match metap_lowcode::publish(&state.pool, &name, &state.metadata_base).await {
        Ok(outcome) => match reload_metadata(&state).await {
            Ok(()) => Json(json!({ "data": { "versionNumber": outcome.version_number } })).into_response(),
            Err(e) => internal_error_response(e),
        },
        Err(e) => publish_error_response(e),
    }
}

#[derive(Deserialize)]
struct RollbackBody {
    #[serde(rename = "toVersionNumber")]
    to_version_number: i32,
}

async fn rollback(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
    Json(body): Json<RollbackBody>,
) -> Response {
    match metap_lowcode::rollback(&state.pool, &name, body.to_version_number, &state.metadata_base).await {
        Ok(outcome) => match reload_metadata(&state).await {
            Ok(()) => Json(json!({ "data": { "versionNumber": outcome.version_number } })).into_response(),
            Err(e) => internal_error_response(e),
        },
        Err(e) => publish_error_response(e),
    }
}

async fn get_published(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
) -> Response {
    match metap_lowcode::get_published(&state.pool, &name).await {
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

async fn list_versions(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
) -> Response {
    match metap_lowcode::list_versions(&state.pool, &name).await {
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

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/admin/lowcode/entities", get(list_entities))
        .route("/admin/lowcode/entities/{name}/draft", get(get_draft).put(save_draft))
        .route("/admin/lowcode/entities/{name}/publish", axum::routing::post(publish))
        .route("/admin/lowcode/entities/{name}/rollback", axum::routing::post(rollback))
        .route("/admin/lowcode/entities/{name}/published", get(get_published))
        .route("/admin/lowcode/entities/{name}/versions", get(list_versions))
}
