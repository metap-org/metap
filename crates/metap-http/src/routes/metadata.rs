//! Mirrors `packages/core/src/server/routes/metadata.ts`. `/metadata/openapi.json` is
//! deliberately public (no `AuthContext` extraction) — same reasoning as the TS route: it
//! only describes API shape (entity/field names/kinds), never tenant data, so
//! `openapi-typescript` codegen can point at a running server without a minted token.

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde_json::{json, Value};

use metap_permission::EntityAction;

use crate::auth::AuthContext;
use crate::error::service_error_response;
use crate::state::AppState;

async fn openapi_json(State(state): State<AppState>) -> Response {
    let entities = state.metadata.load().list_entities();
    let mut doc = metap_metadata::generate_openapi_document(&entities);
    // Merge in this crate's own static-route paths plus whatever optional platform capability
    // the composition root wired in (`state.extra_openapi_paths` — see that field's doc
    // comment). `generate_openapi_document` only knows about `/metadata/*` and the per-entity
    // `/api/{entity}*` paths it derives from `MetadataRegistry`; every other route this binary
    // serves is documented by `crate::openapi_paths` instead (`utoipa`-derived since
    // 2026-09-06, see that module's doc comment), since none of it is metadata-driven.
    if let Some(paths) = doc.get_mut("paths").and_then(Value::as_object_mut) {
        paths.extend(crate::openapi_paths::static_paths());
        paths.extend((*state.extra_openapi_paths).clone());
    }
    // `state.extra_openapi_schemas`'s own doc comment: a `$ref` merged into `paths` above
    // without its target merged here resolves to nothing in the served document. This crate's
    // own `static_paths()` is `utoipa`-generated now, so it contributes real `$ref`s too — not
    // just the optional platform capabilities that used to be the only source.
    if let Some(schemas) = doc
        .get_mut("components")
        .and_then(Value::as_object_mut)
        .and_then(|components| components.get_mut("schemas"))
        .and_then(Value::as_object_mut)
    {
        schemas.extend(crate::openapi_paths::static_schemas());
        schemas.extend((*state.extra_openapi_schemas).clone());
    }
    Json(doc).into_response()
}

/// Requires *a* valid session (`AuthContext`) but not `can_read_entity` per entity — every
/// registered entity's full field/list-view/workflow shape is visible to any authenticated user,
/// regardless of which entities they can actually read. Only schema shape, never tenant data —
/// but in a multi-entity app this discloses the existence and field names of entities the caller
/// has no access to (architecture audit
/// `../metap-docs/docs/audits/03-metap-core-architecture-audit.md` finding #14, 2026-09-02).
async fn list_entities(State(state): State<AppState>, AuthContext(_context): AuthContext) -> Response {
    Json(json!({ "data": state.metadata.load().list_entities() })).into_response()
}

async fn get_entity(
    State(state): State<AppState>,
    Path(entity): Path<String>,
    AuthContext(_context): AuthContext,
) -> Response {
    match state.metadata.load().get_entity_metadata(&entity) {
        Some(summary) => Json(json!({ "data": summary })).into_response(),
        None => service_error_response(404, "entity_not_found", None, None),
    }
}

/// The fixed action set a policy can grant (`EntityAction::ALL`) — static, non-sensitive shape
/// information (same category as entity/field names), not admin-only. Exists so the frontend's
/// permission-matrix UI has a single source of truth for its action columns instead of a second
/// hand-typed mirror of this list (the exact drift `metap-http::routes::admin::KNOWN_ACTIONS`
/// already had before it was pointed at `EntityAction::ALL` too).
async fn list_actions(AuthContext(_context): AuthContext) -> Response {
    let actions: Vec<&'static str> = EntityAction::ALL.iter().map(EntityAction::as_str).collect();
    Json(json!({ "data": actions })).into_response()
}

/// Public — no auth required, matches `registerOpenApiRoute`.
pub fn public_router() -> Router<AppState> {
    Router::new().route("/metadata/openapi.json", get(openapi_json))
}

/// Protected — mounted behind the auth extractor, matches `registerMetadataRoutes`.
pub fn protected_router() -> Router<AppState> {
    Router::new()
        .route("/metadata/entities", get(list_entities))
        .route("/metadata/entities/{entity}", get(get_entity))
        .route("/metadata/actions", get(list_actions))
}
