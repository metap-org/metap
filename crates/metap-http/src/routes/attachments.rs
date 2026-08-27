//! Generic `/api/{entity}/{id}/attachments*` — file attachments for any entity, any app,
//! parameterized by `:entity` the same way `records.rs`'s `/api/{entity}/{id}/transitions/{action}`
//! already is. Always registered in `build_router` regardless of whether the host binary
//! configures `AppState.object_store` — an unconfigured app just gets a 503 here, same shape any
//! other optional feature degrades to.
//!
//! Deliberately not routed through `CrudService`: `metap_attachments::AttachmentRecord` isn't a
//! metadata-driven entity (see that crate's doc comment for why — `record_id` can't carry a
//! typed FK to "whichever entity"), so permission is checked directly against the *target*
//! entity's policies (`can_update_entity`/`can_read_entity`/`can_delete_entity` — "can I edit
//! this issue" is exactly "can I attach a file to it"), and reads/writes go through
//! `Router::begin(tenant_id)` the same way every tenant-scoped route does, landing in whichever
//! physical database that tenant's strategy resolves to.

use axum::extract::{Multipart, Path, State};
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use metap_attachments::AttachmentRecord;
use serde_json::json;
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, router_unavailable_response, service_error_response};
use crate::state::AppState;

fn table_for<'a>(state: &'a AppState, entity: &str) -> &'a str {
    state
        .attachment_tables
        .get(entity)
        .map(String::as_str)
        .unwrap_or("attachments")
}

fn to_json(record: &AttachmentRecord) -> serde_json::Value {
    json!({
        "id": record.id,
        "entityName": record.entity_name,
        "recordId": record.record_id,
        "filename": record.filename,
        "key": record.key,
        "size": record.size,
        "contentType": record.content_type,
        "createdAt": record.created_at,
    })
}

async fn upload_attachment(
    State(state): State<AppState>,
    Path((entity, record_id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
    mut multipart: Multipart,
) -> Response {
    let Some(store) = state.object_store.clone() else {
        return service_error_response(
            503,
            "storage_not_configured",
            Some("Object storage is not configured."),
            None,
        );
    };
    let decision = match state.permissions.can_update_entity(&context, &entity).await {
        Ok(d) => d,
        Err(e) => return internal_error_response(e),
    };
    if !decision.allowed {
        return service_error_response(403, "forbidden", None, None);
    }
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };

    let field = match multipart.next_field().await {
        Ok(Some(field)) => field,
        Ok(None) => {
            return service_error_response(400, "missing_file", Some("No file field found in the upload."), None)
        }
        Err(e) => return service_error_response(400, "invalid_multipart", Some(&e.to_string()), None),
    };
    let filename = field.file_name().unwrap_or("upload").to_string();
    let content_type = field.content_type().map(str::to_string);
    let bytes = match field.bytes().await {
        Ok(b) => b,
        Err(e) => return service_error_response(400, "invalid_multipart", Some(&e.to_string()), None),
    };

    let key = format!("{entity}/{record_id}/{}/{filename}", Uuid::new_v4());
    if let Err(e) = store.put(tenant_id, &key, bytes.clone(), content_type.as_deref()).await {
        return internal_error_response(e);
    }

    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => {
            let _ = store.delete(tenant_id, &key).await;
            return router_unavailable_response(e);
        }
    };
    let table = table_for(&state, &entity);
    let created_by = context.user_id.as_deref().and_then(|s| Uuid::parse_str(s).ok());
    let record = match metap_attachments::create_attachment(
        &mut *tx,
        table,
        tenant_id,
        &entity,
        record_id,
        &filename,
        &key,
        bytes.len() as i64,
        content_type.as_deref(),
        created_by,
    )
    .await
    {
        Ok(r) => r,
        Err(e) => {
            let _ = store.delete(tenant_id, &key).await;
            return internal_error_response(e);
        }
    };
    if let Err(e) = tx.commit().await {
        let _ = store.delete(tenant_id, &key).await;
        return internal_error_response(e.into());
    }

    (StatusCode::CREATED, Json(json!({ "data": to_json(&record) }))).into_response()
}

async fn list_attachments(
    State(state): State<AppState>,
    Path((entity, record_id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    let decision = match state.permissions.can_read_entity(&context, &entity).await {
        Ok(d) => d,
        Err(e) => return internal_error_response(e),
    };
    if !decision.allowed {
        return service_error_response(403, "forbidden", None, None);
    }
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    let table = table_for(&state, &entity);
    let records = match metap_attachments::list_attachments(&mut *tx, table, tenant_id, &entity, record_id).await {
        Ok(r) => r,
        Err(e) => return internal_error_response(e),
    };
    let _ = tx.commit().await;

    Json(json!({ "data": records.iter().map(to_json).collect::<Vec<_>>() })).into_response()
}

/// Looks up the attachment and checks it actually belongs to `(entity, record_id)` — the
/// `attachmentId` path segment alone would already be tenant-scoped (`get_attachment` filters by
/// `tenant_id`), but cross-checking the parent keeps a mismatched URL (an attachment id from a
/// different record, guessed or copy-pasted) a clean 404 rather than silently serving it.
async fn load_owned_attachment(
    state: &AppState,
    entity: &str,
    record_id: Uuid,
    attachment_id: Uuid,
    tenant_id: Uuid,
    table: &str,
) -> Result<AttachmentRecord, Box<Response>> {
    let mut tx = state
        .router
        .begin(tenant_id.into())
        .await
        .map_err(|e| Box::new(router_unavailable_response(e)))?;
    let record = metap_attachments::get_attachment(&mut *tx, table, tenant_id, attachment_id)
        .await
        .map_err(|e| Box::new(internal_error_response(e)))?
        .ok_or_else(|| Box::new(service_error_response(404, "record_not_found", None, None)))?;
    let _ = tx.commit().await;
    if record.entity_name != entity || record.record_id != record_id {
        return Err(Box::new(service_error_response(404, "record_not_found", None, None)));
    }
    Ok(record)
}

async fn download_attachment(
    State(state): State<AppState>,
    Path((entity, record_id, attachment_id)): Path<(String, Uuid, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    let Some(store) = state.object_store.clone() else {
        return service_error_response(
            503,
            "storage_not_configured",
            Some("Object storage is not configured."),
            None,
        );
    };
    let decision = match state.permissions.can_read_entity(&context, &entity).await {
        Ok(d) => d,
        Err(e) => return internal_error_response(e),
    };
    if !decision.allowed {
        return service_error_response(403, "forbidden", None, None);
    }
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let table = table_for(&state, &entity).to_string();
    let record = match load_owned_attachment(&state, &entity, record_id, attachment_id, tenant_id, &table).await {
        Ok(r) => r,
        Err(resp) => return *resp,
    };

    let bytes = match store.get(tenant_id, &record.key).await {
        Ok(Some(b)) => b,
        Ok(None) => {
            return service_error_response(
                404,
                "record_not_found",
                Some("The file is missing from object storage."),
                None,
            )
        }
        Err(e) => return internal_error_response(e),
    };

    // `Content-Disposition: attachment` — never `inline` — so a browser downloads rather than
    // renders whatever `contentType` the uploader claimed (a caller-supplied, unverified value —
    // see `metap-storage`'s top doc comment on why that matters for a user-uploaded
    // `text/html`/`image/svg+xml`).
    (
        StatusCode::OK,
        [
            (
                header::CONTENT_TYPE,
                record
                    .content_type
                    .clone()
                    .unwrap_or_else(|| "application/octet-stream".to_string()),
            ),
            (
                header::CONTENT_DISPOSITION,
                format!("attachment; filename=\"{}\"", record.filename),
            ),
        ],
        bytes,
    )
        .into_response()
}

async fn delete_attachment(
    State(state): State<AppState>,
    Path((entity, record_id, attachment_id)): Path<(String, Uuid, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    let Some(store) = state.object_store.clone() else {
        return service_error_response(
            503,
            "storage_not_configured",
            Some("Object storage is not configured."),
            None,
        );
    };
    let decision = match state.permissions.can_delete_entity(&context, &entity).await {
        Ok(d) => d,
        Err(e) => return internal_error_response(e),
    };
    if !decision.allowed {
        return service_error_response(403, "forbidden", None, None);
    }
    let tenant_id = match state.permissions.scoped_tenant(&context) {
        Ok(id) => id,
        Err(e) => return internal_error_response(e),
    };
    let table = table_for(&state, &entity).to_string();
    let record = match load_owned_attachment(&state, &entity, record_id, attachment_id, tenant_id, &table).await {
        Ok(r) => r,
        Err(resp) => return *resp,
    };

    let mut tx = match state.router.begin(tenant_id.into()).await {
        Ok(tx) => tx,
        Err(e) => return router_unavailable_response(e),
    };
    if let Err(e) = metap_attachments::delete_attachment(&mut *tx, &table, tenant_id, attachment_id).await {
        return internal_error_response(e);
    }
    if let Err(e) = tx.commit().await {
        return internal_error_response(e.into());
    }

    // Best-effort — the metadata row (the thing permission/listing/download actually rely on)
    // is already gone at this point regardless of whether the blob cleanup itself succeeds; a
    // failed `delete` here leaves an orphaned object in storage, not a dangling metadata
    // reference, so it's logged rather than turned into a failed request.
    if let Err(e) = store.delete(tenant_id, &record.key).await {
        tracing::warn!(key = %record.key, error = %e, "failed to delete attachment blob from object storage");
    }

    StatusCode::NO_CONTENT.into_response()
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route(
            "/api/{entity}/{record_id}/attachments",
            post(upload_attachment).get(list_attachments),
        )
        .route(
            "/api/{entity}/{record_id}/attachments/{attachment_id}/download",
            get(download_attachment),
        )
        .route(
            "/api/{entity}/{record_id}/attachments/{attachment_id}",
            axum::routing::delete(delete_attachment),
        )
}
