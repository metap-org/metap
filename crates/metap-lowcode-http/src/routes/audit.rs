//! `GET .../entities/{name}/audit` and `GET /admin/lowcode/audit` — per-entity and cross-entity
//! audit feed (`docs/roadmap.md` Phase 11 Phase C).

use axum::extract::{Path, Query, State};
use axum::response::{IntoResponse, Response};
use axum::Json;
use metap_http::auth::AdminContext;
use metap_http::error::internal_error_response;
use metap_http::AppState;
use metap_lowcode::audit;
use serde_json::{json, Value};

use crate::resolve_pool;

/// `docs/roadmap.md` Phase 11 Phase C's "audit log cho metadata" — who/when
/// draft-saved/published/rolled-back/enabled/disabled this entity, newest first.
pub(crate) async fn list_audit_events(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match audit::list_for_entity(&pool, &name, &context.tenant_id).await {
        Ok(events) => {
            let data: Vec<_> = events.into_iter().map(audit_event_to_json).collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

fn audit_event_to_json(e: audit::AuditEvent) -> Value {
    json!({
        "entityName": e.entity_name,
        "action": e.action,
        "actorUserId": e.actor_user_id,
        "actorTenantId": e.actor_tenant_id,
        "versionNumber": e.version_number,
        "restoredFromVersion": e.restored_from_version,
        "occurredAt": e.occurred_at,
    })
}

const DEFAULT_RECENT_AUDIT_LIMIT: i64 = 50;
const MAX_RECENT_AUDIT_LIMIT: i64 = 200;

/// Cross-entity counterpart to `list_audit_events` — "operational visibility" (Phase 11C,
/// `docs/roadmap.md`): the last deliverable of Phase C without its own admin API surface yet.
/// `?limit=N` (default 50, clamped to 200) — same "every list has a max limit" convention
/// `QueryPlanner` follows, applied here since this bypasses `QueryPlanner`/`records` entirely
/// (a fixed, non-metadata-driven table).
pub(crate) async fn list_recent_audit_events(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Query(params): Query<std::collections::HashMap<String, String>>,
) -> Response {
    let limit = params
        .get("limit")
        .and_then(|s| s.parse::<i64>().ok())
        .filter(|&n| n > 0)
        .unwrap_or(DEFAULT_RECENT_AUDIT_LIMIT)
        .min(MAX_RECENT_AUDIT_LIMIT);
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match audit::list_recent(&pool, &context.tenant_id, limit).await {
        Ok(events) => {
            let data: Vec<_> = events.into_iter().map(audit_event_to_json).collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}
