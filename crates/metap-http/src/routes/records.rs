//! Mirrors `packages/core/src/server/routes/records.ts`.

use std::collections::HashMap;

use axum::extract::{Path, Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use metap_crud::ServiceResult;
use metap_query::ListInput;
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

use crate::auth::AuthContext;
use crate::error::{internal_error_response, service_error_response};
use crate::state::AppState;

const RESERVED_QUERY_KEYS: [&str; 3] = ["limit", "sort", "cursor"];

fn parse_list_input(params: &HashMap<String, String>) -> Result<ListInput, Response> {
    let limit = match params.get("limit") {
        None => 30,
        Some(raw) => match raw.parse::<i64>() {
            Ok(n) if n > 0 && n <= 200 => n,
            _ => {
                return Err(service_error_response(
                    400,
                    "validation_failed",
                    Some("`limit` must be a positive integer no greater than 200."),
                    None,
                ))
            }
        },
    };

    let filters: Vec<(String, String)> = params
        .iter()
        .filter(|(k, _)| !RESERVED_QUERY_KEYS.contains(&k.as_str()))
        .map(|(k, v)| (k.clone(), v.clone()))
        .collect();

    Ok(ListInput {
        limit,
        sort: params.get("sort").cloned(),
        filters,
        cursor: params.get("cursor").cloned(),
    })
}

async fn list_records(
    State(state): State<AppState>,
    Path(entity): Path<String>,
    Query(params): Query<HashMap<String, String>>,
    AuthContext(context): AuthContext,
) -> Response {
    let input = match parse_list_input(&params) {
        Ok(i) => i,
        Err(resp) => return resp,
    };

    match state.crud.list(&entity, &input, &context).await {
        Ok(ServiceResult::Ok { data, page }) => {
            let page = page.map(|p| json!({ "limit": p.limit, "nextCursor": p.next_cursor }));
            Json(json!({ "data": data, "page": page })).into_response()
        }
        Ok(ServiceResult::Err { status, error, message, field_errors }) => {
            service_error_response(status, &error, message.as_deref(), field_errors)
        }
        Err(e) => internal_error_response(e),
    }
}

async fn get_record(
    State(state): State<AppState>,
    Path((entity, id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    match state.crud.get(&entity, id, &context).await {
        Ok(ServiceResult::Ok { data: (record, capabilities), .. }) => {
            let mut data = serde_json::to_value(record).unwrap_or(Value::Null);
            if let Value::Object(map) = &mut data {
                map.insert("capabilities".to_string(), serde_json::to_value(capabilities).unwrap_or(Value::Null));
            }
            Json(json!({ "data": data })).into_response()
        }
        Ok(ServiceResult::Err { status, error, message, field_errors }) => {
            service_error_response(status, &error, message.as_deref(), field_errors)
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct RecordBody {
    data: HashMap<String, Value>,
}

async fn create_record(
    State(state): State<AppState>,
    Path(entity): Path<String>,
    AuthContext(context): AuthContext,
    Json(body): Json<RecordBody>,
) -> Response {
    let data: metap_crud::JsonObject = body.data.into_iter().collect();
    match state.crud.create(&entity, &data, &context).await {
        Ok(ServiceResult::Ok { data, .. }) => {
            (StatusCode::CREATED, Json(json!({ "data": data }))).into_response()
        }
        Ok(ServiceResult::Err { status, error, message, field_errors }) => {
            service_error_response(status, &error, message.as_deref(), field_errors)
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct UpdateBody {
    version: i32,
    data: HashMap<String, Value>,
}

async fn update_record(
    State(state): State<AppState>,
    Path((entity, id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
    Json(body): Json<UpdateBody>,
) -> Response {
    let data: metap_crud::JsonObject = body.data.into_iter().collect();
    match state.crud.update(&entity, id, body.version, &data, &context).await {
        Ok(ServiceResult::Ok { data, .. }) => Json(json!({ "data": data })).into_response(),
        Ok(ServiceResult::Err { status, error, message, field_errors }) => {
            service_error_response(status, &error, message.as_deref(), field_errors)
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct DeleteBody {
    version: i32,
}

async fn delete_record(
    State(state): State<AppState>,
    Path((entity, id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
    Json(body): Json<DeleteBody>,
) -> Response {
    match state.crud.delete(&entity, id, body.version, &context).await {
        Ok(ServiceResult::Ok { data, .. }) => Json(json!({ "data": data })).into_response(),
        Ok(ServiceResult::Err { status, error, message, field_errors }) => {
            service_error_response(status, &error, message.as_deref(), field_errors)
        }
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct TransitionBody {
    version: i32,
}

async fn transition_record(
    State(state): State<AppState>,
    Path((entity, id, action)): Path<(String, Uuid, String)>,
    AuthContext(context): AuthContext,
    Json(body): Json<TransitionBody>,
) -> Response {
    match state.crud.transition(&entity, id, &action, body.version, &context).await {
        Ok(ServiceResult::Ok { data, .. }) => Json(json!({ "data": data })).into_response(),
        Ok(ServiceResult::Err { status, error, message, field_errors }) => {
            service_error_response(status, &error, message.as_deref(), field_errors)
        }
        Err(e) => internal_error_response(e),
    }
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/api/{entity}", get(list_records).post(create_record))
        .route("/api/{entity}/{id}", get(get_record).patch(update_record).delete(delete_record))
        .route("/api/{entity}/{id}/transitions/{action}", post(transition_record))
}
