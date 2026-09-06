//! Mirrors `packages/core/src/server/routes/health.ts`.

use axum::extract::State;
use axum::response::{IntoResponse, Response};
use axum::{Json, Router};
use serde::Serialize;
use serde_json::json;
use utoipa::ToSchema;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;

use crate::state::AppState;

// Never actually constructed — see `metap-lowcode`'s `docs/features/02-utoipa-migration.md` for
// why response DTOs exist purely for `ToSchema`'s shape derivation on a handler returning a
// type-erased `Response`, not `Json<T>` directly.
#[derive(Serialize, ToSchema)]
struct HealthChecks {
    database: bool,
}

#[derive(Serialize, ToSchema)]
struct HealthResponse {
    #[schema(example = "ok")]
    status: String,
    checks: HealthChecks,
}

#[utoipa::path(
    get,
    path = "/health",
    responses((status = 200, description = "OK", body = HealthResponse)),
)]
async fn health(State(state): State<AppState>) -> Response {
    let db_ok = metap_infra::health_check(&state.pool).await;
    Json(json!({
        "status": if db_ok { "ok" } else { "degraded" },
        "checks": { "database": db_ok },
    }))
    .into_response()
}

fn build_router() -> OpenApiRouter<AppState> {
    OpenApiRouter::new().routes(routes!(health))
}

pub fn router() -> Router<AppState> {
    build_router().split_for_parts().0
}

pub(crate) fn openapi() -> utoipa::openapi::OpenApi {
    build_router().split_for_parts().1
}
