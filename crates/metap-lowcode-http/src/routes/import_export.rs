//! `GET /admin/lowcode/export` / `POST /admin/lowcode/import` — a portable snapshot of published
//! entity definitions for moving a low-code app between deployments (`docs/roadmap.md` Phase 11
//! Phase C).

use std::collections::HashSet;

use axum::extract::{Query, State};
use axum::response::{IntoResponse, Response};
use axum::Json;
use metap_http::auth::AdminContext;
use metap_http::error::internal_error_response;
use metap_http::AppState;
use metap_lowcode::audit::{self, AuditAction, AuditActor, AuditVersionInfo};
use metap_lowcode::LowCodeEntityDefinition;
use serde::Deserialize;
use serde_json::json;

use crate::resolve_pool;

/// Merge this into `metap_http::build_router`'s `extra_routes` argument to expose the
/// low-code admin API on a running server — never merged automatically by `metap-http` itself.
/// `docs/roadmap.md` Phase 11 Phase C's "import/export định nghĩa app" — portable snapshot of
/// published entity definitions, for moving a low-code app between deployments (definitions
/// are global to a deployment, not tenant-scoped — see this file's top doc comment — so this
/// is not a cross-tenant copy). `?entities=a,b,c` filters to those names, omitted exports
/// everything published; a requested name with no published version is reported under
/// `notFound` instead of erroring the whole request.
pub(crate) async fn export_entities(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Query(params): Query<std::collections::HashMap<String, String>>,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    let requested: Option<Vec<String>> = params.get("entities").map(|s| {
        s.split(',')
            .map(str::trim)
            .filter(|n| !n.is_empty())
            .map(str::to_string)
            .collect()
    });

    let found = match metap_lowcode::export_entities(&pool, requested.as_deref()).await {
        Ok(entities) => entities,
        Err(e) => return internal_error_response(e),
    };
    let found_names: HashSet<&str> = found.iter().map(|(name, _)| name.as_str()).collect();
    let not_found: Vec<&str> = requested
        .as_deref()
        .unwrap_or(&[])
        .iter()
        .map(String::as_str)
        .filter(|name| !found_names.contains(name))
        .collect();
    let entities: Vec<_> = found
        .into_iter()
        .map(|(name, definition)| json!({ "name": name, "definition": definition }))
        .collect();

    Json(json!({ "data": { "entities": entities, "notFound": not_found } })).into_response()
}

#[derive(Deserialize)]
struct ImportEntity {
    name: String,
    definition: LowCodeEntityDefinition,
}

#[derive(Deserialize)]
pub(crate) struct ImportBody {
    entities: Vec<ImportEntity>,
}

/// The write side of import/export — writes each entity in the bundle as a *draft*
/// (`metap_lowcode::save_draft`, same shape validation an operator authoring through the admin
/// UI gets), never auto-publishes. Publishing stays a deliberate, per-entity next step through
/// the existing `POST .../publish` (with its full name-reservation/cross-reference/
/// migration-impact checks) — import intentionally doesn't bypass any of that. Best-effort like
/// `bulk_query_action` cron targets: one bad entity in the batch doesn't fail the rest, the
/// response reports each name's outcome individually.
pub(crate) async fn import_entities(
    State(state): State<AppState>,
    AdminContext(context): AdminContext,
    Json(body): Json<ImportBody>,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    let mut imported = Vec::new();
    let mut failed = Vec::new();
    for item in body.entities {
        match metap_lowcode::save_draft(&pool, &item.name, &item.definition).await {
            Ok(()) => {
                audit::record(
                    &pool,
                    &item.name,
                    AuditAction::DraftSaved,
                    &AuditActor {
                        user_id: context.user_id.clone(),
                        tenant_id: context.tenant_id.clone(),
                    },
                    AuditVersionInfo::default(),
                )
                .await;
                imported.push(item.name);
            }
            Err(e) => failed.push(json!({ "name": item.name, "error": e.to_string() })),
        }
    }
    Json(json!({ "data": { "imported": imported, "failed": failed } })).into_response()
}
