//! `GET`/`PUT /admin/lowcode/entities/{name}/draft` — reading and saving an entity's unpublished
//! draft definition.

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::Json;
use metap_http::auth::AdminContext;
use metap_http::error::{internal_error_response, service_error_response};
use metap_http::AppState;
use metap_lowcode::audit::{self, AuditAction, AuditActor, AuditVersionInfo};
use metap_lowcode::LowCodeEntityDefinition;
use metap_metadata::{EntityField, EntityListView, EntityWorkflow};
use serde::Deserialize;
use serde_json::json;

use crate::resolve_pool;

use super::publish::publish_error_response;

#[derive(Deserialize)]
pub(crate) struct DraftBody {
    label: String,
    #[serde(default)]
    fields: Vec<EntityField>,
    #[serde(rename = "listViews", default)]
    list_views: Vec<EntityListView>,
    #[serde(default)]
    workflow: Option<EntityWorkflow>,
}

pub(crate) async fn save_draft(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
    Json(body): Json<DraftBody>,
) -> Response {
    let definition = LowCodeEntityDefinition {
        name: name.clone(),
        label: body.label,
        fields: body.fields,
        list_views: body.list_views,
        workflow: body.workflow,
    };
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match metap_lowcode::save_draft(&pool, &name, &definition).await {
        Ok(()) => {
            audit::record(
                &pool,
                &name,
                AuditAction::DraftSaved,
                &AuditActor {
                    user_id: context.user_id.clone(),
                    tenant_id: context.tenant_id.clone(),
                },
                AuditVersionInfo::default(),
            )
            .await;
            Json(json!({ "data": definition })).into_response()
        }
        Err(e) => publish_error_response(e),
    }
}

pub(crate) async fn get_draft(
    State(state): State<AppState>,
    Path(name): Path<String>,
    AdminContext(context): AdminContext,
) -> Response {
    let pool = match resolve_pool(&state, &context).await {
        Ok(p) => p,
        Err(resp) => return *resp,
    };
    match metap_lowcode::get_draft(&pool, &name).await {
        Ok(Some(def)) => Json(json!({ "data": def })).into_response(),
        Ok(None) => service_error_response(404, "lowcode_draft_not_found", None, None),
        Err(e) => internal_error_response(e),
    }
}
