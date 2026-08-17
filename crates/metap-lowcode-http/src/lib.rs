//! Admin-gated HTTP surface for `metap-lowcode`'s draft/publish/rollback storage
//! (`docs/roadmap.md` Phase 11 / Phase A sub-project 4, retargeted from
//! `docs/low-code-metadata-storage-design.md`). Same shape as `metap-http`'s
//! `routes/admin.rs`/`routes/cron.rs`: every handler uses `AdminContext`. Unlike
//! `routes/cron.rs`, nothing here is tenant-scoped — DB-authored entity metadata is global by
//! design for Phase A (see the spec's "Các quyết định đã chốt"), so there's no `tenant_id` in
//! any query.
//!
//! **Deliberately its own crate, not a module inside `metap-http`** — the low-code control
//! plane is an optional platform capability, not core (`docs/roadmap.md` Phase 11 is a
//! trigger-based, in-progress phase; the base execution engine predates it and must keep
//! working without it). `metap-http` has zero dependency on this crate or on `metap-lowcode`;
//! a binary that wants this surface merges [`router`] into `metap_http::build_router`'s
//! `extra_routes` argument itself (see `apps/crm-server/src/main.rs`) — it is never wired in
//! automatically. A downstream project that doesn't want a low-code control plane at all can
//! depend on `metap-http`/`metap-crud`/`metap-metadata` and skip this crate entirely.
//!
//! `publish`/`rollback` are the only handlers that mutate `state.metadata` — both call
//! [`apply_registry`] after `metap_lowcode` writes a new version, swapping the already-
//! validated registry `metap_lowcode::publish`/`rollback` returns straight into
//! `state.metadata`'s `ArcSwap` before the handler responds. No restart required (Phase A
//! sub-project 2) — any request after the response comes back is guaranteed to see the new
//! registry.

use std::sync::Arc;

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use metap_http::auth::AdminContext;
use metap_http::error::{internal_error_response, service_error_response};
use metap_http::AppState;
use metap_lowcode::{LowCodeEntityDefinition, PublishError};
use metap_metadata::{EntityField, EntityListView, EntityWorkflow, MetadataRegistry};
use serde::Deserialize;
use serde_json::json;

#[derive(Deserialize)]
struct DraftBody {
    label: String,
    #[serde(default)]
    fields: Vec<EntityField>,
    #[serde(rename = "listViews", default)]
    list_views: Vec<EntityListView>,
    #[serde(default)]
    workflow: Option<EntityWorkflow>,
}

fn publish_error_response(err: PublishError) -> Response {
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
pub async fn apply_registry(state: &AppState, registry: MetadataRegistry) {
    let entities = registry.list_entities();
    state.metadata.store(Arc::new(registry));
    metap_peripherals::reconcile_indexes(&state.pool, &entities).await;
}

async fn list_entities(State(state): State<AppState>, AdminContext(_context): AdminContext) -> Response {
    // `list_all_published` here is deliberately the *unfiltered* one (includes disabled
    // entities) — this listing is what tells an operator an entity has been published at
    // all, regardless of its current enabled state; `list_enabled_published` would make a
    // disabled-but-published entity look indistinguishable from one that was never published.
    let published = match metap_lowcode::list_all_published(&state.pool).await {
        Ok(p) => p,
        Err(e) => return internal_error_response(e),
    };
    let statuses = match metap_lowcode::list_draft_statuses(&state.pool).await {
        Ok(s) => s,
        Err(e) => return internal_error_response(e),
    };
    let published_names: std::collections::HashSet<&str> = published.iter().map(|(name, _)| name.as_str()).collect();
    let entities: Vec<_> = statuses
        .into_iter()
        .map(|(name, enabled)| {
            let is_published = published_names.contains(name.as_str());
            json!({ "name": name, "published": is_published, "enabled": enabled })
        })
        .collect();
    Json(json!({ "data": { "entities": entities } })).into_response()
}

#[derive(Deserialize)]
struct SetEnabledBody {
    enabled: bool,
}

/// Toggles an entity's enabled flag and immediately rebuilds + swaps the live registry (same
/// as `publish`/`rollback`) so a disable takes effect without a restart — a disabled entity
/// disappears from `GET /metadata/entities` and `/api/:entity` starts 404ing on it right
/// away, and re-enabling brings it straight back with no republish needed.
async fn set_enabled(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
    Json(body): Json<SetEnabledBody>,
) -> Response {
    if let Err(e) = metap_lowcode::set_enabled(&state.pool, &name, body.enabled).await {
        return internal_error_response(e);
    }
    let db_entities: Vec<_> = match metap_lowcode::list_enabled_published(&state.pool).await {
        Ok(entities) => entities
            .into_iter()
            .map(|(_, def)| def.to_entity_definition())
            .collect(),
        Err(e) => return internal_error_response(e),
    };
    let registry = match state.metadata_base.merge_with(db_entities) {
        Ok(r) => r,
        Err(e) => return internal_error_response(e.into()),
    };
    apply_registry(&state, registry).await;
    Json(json!({ "data": { "name": name, "enabled": body.enabled } })).into_response()
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
        workflow: body.workflow,
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
        Ok(outcome) => {
            let version_number = outcome.version_number;
            apply_registry(&state, outcome.registry).await;
            Json(json!({ "data": { "versionNumber": version_number } })).into_response()
        }
        Err(e) => publish_error_response(e),
    }
}

#[derive(Deserialize)]
struct RollbackBody {
    #[serde(rename = "toVersionNumber")]
    to_version_number: i32,
}

/// `docs/roadmap.md` Phase 11 Phase B's publish preview/validation report — runs the exact
/// checks `publish` would (shape, name-reservation, cross-reference) with no side effect, so
/// an operator can validate a draft before committing to a new published version.
async fn preview_publish(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
) -> Response {
    match metap_lowcode::preview_publish(&state.pool, &name, &state.metadata_base).await {
        Ok(preview) => {
            Json(json!({ "data": { "wouldBeVersion": preview.would_be_version, "valid": true } })).into_response()
        }
        Err(e) => publish_error_response(e),
    }
}

async fn rollback(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(_context): AdminContext,
    Json(body): Json<RollbackBody>,
) -> Response {
    match metap_lowcode::rollback(&state.pool, &name, body.to_version_number, &state.metadata_base).await {
        Ok(outcome) => {
            let version_number = outcome.version_number;
            apply_registry(&state, outcome.registry).await;
            Json(json!({ "data": { "versionNumber": version_number } })).into_response()
        }
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

/// Merge this into `metap_http::build_router`'s `extra_routes` argument to expose the
/// low-code admin API on a running server — never merged automatically by `metap-http` itself.
pub fn router() -> Router<AppState> {
    Router::new()
        .route("/admin/lowcode/entities", get(list_entities))
        .route("/admin/lowcode/entities/{name}", axum::routing::patch(set_enabled))
        .route("/admin/lowcode/entities/{name}/draft", get(get_draft).put(save_draft))
        .route("/admin/lowcode/entities/{name}/publish", axum::routing::post(publish))
        .route(
            "/admin/lowcode/entities/{name}/publish/preview",
            axum::routing::post(preview_publish),
        )
        .route("/admin/lowcode/entities/{name}/rollback", axum::routing::post(rollback))
        .route("/admin/lowcode/entities/{name}/published", get(get_published))
        .route("/admin/lowcode/entities/{name}/versions", get(list_versions))
}
