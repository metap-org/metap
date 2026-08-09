//! Mirrors `packages/core/src/server/routes/metadata.ts`. `/metadata/openapi.json` is
//! deliberately public (no `AuthContext` extraction) — same reasoning as the TS route: it
//! only describes API shape (entity/field names/kinds), never tenant data, so
//! `openapi-typescript` codegen can point at a running server without a minted token.

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::{Json, Router};
use serde_json::json;

use crate::auth::AuthContext;
use crate::error::service_error_response;
use crate::state::AppState;

async fn openapi_json(State(state): State<AppState>) -> Response {
    let entities = state.metadata.list_entities();
    Json(metap_metadata::generate_openapi_document(&entities)).into_response()
}

async fn list_entities(State(state): State<AppState>, AuthContext(_context): AuthContext) -> Response {
    Json(json!({ "data": state.metadata.list_entities() })).into_response()
}

async fn get_entity(
    State(state): State<AppState>,
    Path(entity): Path<String>,
    AuthContext(_context): AuthContext,
) -> Response {
    match state.metadata.get_entity_metadata(&entity) {
        Some(summary) => Json(json!({ "data": summary })).into_response(),
        None => service_error_response(404, "entity_not_found", None, None),
    }
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
}
