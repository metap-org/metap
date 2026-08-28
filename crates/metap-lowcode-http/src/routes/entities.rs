//! `GET /admin/lowcode/entities` (list) and `PATCH /admin/lowcode/entities/{name}` (enable/
//! disable) — the two handlers that operate on an entity's registration itself, not its draft/
//! published content.

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::Json;
use metap_http::auth::AdminContext;
use metap_http::error::internal_error_response;
use metap_http::AppState;
use metap_lowcode::audit::{self, AuditAction, AuditActor, AuditVersionInfo};
use serde::Deserialize;
use serde_json::json;

use crate::resolve_pool;

use super::publish::apply_registry;

pub(crate) async fn list_entities(State(state): State<AppState>, AdminContext(context): AdminContext) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    // `list_all_published` here is deliberately the *unfiltered* one (includes disabled
    // entities) — this listing is what tells an operator an entity has been published at
    // all, regardless of its current enabled state; `list_enabled_published` would make a
    // disabled-but-published entity look indistinguishable from one that was never published.
    let published = match metap_lowcode::list_all_published(&pool).await {
        Ok(p) => p,
        Err(e) => return internal_error_response(e),
    };
    let statuses = match metap_lowcode::list_draft_statuses(&pool).await {
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
pub(crate) struct SetEnabledBody {
    enabled: bool,
}

/// Toggles an entity's enabled flag and immediately rebuilds + swaps the live registry (same
/// as `publish`/`rollback`) so a disable takes effect without a restart — a disabled entity
/// disappears from `GET /metadata/entities` and `/api/:entity` starts 404ing on it right
/// away, and re-enabling brings it straight back with no republish needed.
pub(crate) async fn set_enabled(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
    Json(body): Json<SetEnabledBody>,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    if let Err(e) = metap_lowcode::set_enabled(&pool, &name, body.enabled).await {
        return internal_error_response(e);
    }
    let db_entities: Vec<_> = match metap_lowcode::list_enabled_published(&pool).await {
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
    apply_registry(&state, &pool, registry).await;
    let action = if body.enabled {
        AuditAction::Enabled
    } else {
        AuditAction::Disabled
    };
    audit::record(
        &pool,
        &name,
        action,
        &AuditActor {
            user_id: context.user_id.clone(),
            tenant_id: context.tenant_id.clone(),
        },
        AuditVersionInfo::default(),
    )
    .await;
    Json(json!({ "data": { "name": name, "enabled": body.enabled } })).into_response()
}
