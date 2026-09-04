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

const RESERVED_QUERY_KEYS: [&str; 5] = ["limit", "sort", "cursor", "listView", "jql"];

fn parse_list_input(params: &HashMap<String, String>) -> Result<ListInput, Box<Response>> {
    let limit = match params.get("limit") {
        None => 30,
        Some(raw) => match raw.parse::<i64>() {
            Ok(n) if n > 0 && n <= 200 => n,
            _ => {
                return Err(Box::new(service_error_response(
                    400,
                    "validation_failed",
                    Some("`limit` must be a positive integer no greater than 200."),
                    None,
                )))
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
        list_view: params.get("listView").cloned(),
        jql: params.get("jql").cloned(),
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
        Err(resp) => return *resp,
    };

    match state.crud.list(&entity, &input, &context).await {
        Ok(ServiceResult::Ok { data, page }) => {
            let page = page.map(|p| json!({ "limit": p.limit, "nextCursor": p.next_cursor }));
            Json(json!({ "data": data, "page": page })).into_response()
        }
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => service_error_response(status, &error, message.as_deref(), field_errors),
        Err(e) => internal_error_response(e),
    }
}

async fn get_record(
    State(state): State<AppState>,
    Path((entity, id)): Path<(String, Uuid)>,
    AuthContext(context): AuthContext,
) -> Response {
    match state.crud.get(&entity, id, &context).await {
        Ok(ServiceResult::Ok {
            data: (record, capabilities),
            ..
        }) => {
            let mut data = serde_json::to_value(record).unwrap_or(Value::Null);
            if let Value::Object(map) = &mut data {
                map.insert(
                    "capabilities".to_string(),
                    serde_json::to_value(capabilities).unwrap_or(Value::Null),
                );
            }
            Json(json!({ "data": data })).into_response()
        }
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => service_error_response(status, &error, message.as_deref(), field_errors),
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
        Ok(ServiceResult::Ok { data, .. }) => (StatusCode::CREATED, Json(json!({ "data": data }))).into_response(),
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => service_error_response(status, &error, message.as_deref(), field_errors),
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
        Ok(ServiceResult::Ok { data, .. }) => {
            invalidate_context_cache_if_auth_context_entity(&state, &context, &entity, &data.data).await;
            Json(json!({ "data": data })).into_response()
        }
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => service_error_response(status, &error, message.as_deref(), field_errors),
        Err(e) => internal_error_response(e),
    }
}

/// Closes a real staleness gap found in code review (2026-08-22): editing a user's
/// `AUTH_CONTEXT_ENTITY` record (e.g. reassigning `departmentId`) through the ordinary
/// `PATCH /api/:entity/:id` path never invalidated `context_attributes_cache` — only the
/// explicit `POST /admin/users/{userId}/context/invalidate` endpoint did, an easy step to forget
/// since it's a separate call an admin has to remember to make. The record being edited isn't
/// necessarily the *caller's own* — an admin editing another user's employee record is the
/// common case — so this reads `userId` off the just-updated record's data, not off `context`,
/// to invalidate the right cache key. Best-effort: a malformed/missing `userId` just means
/// nothing to invalidate, not an error; this must never fail the request that triggered it.
async fn invalidate_context_cache_if_auth_context_entity(
    state: &AppState,
    context: &metap_permission::RequestContext,
    entity: &str,
    data: &metap_crud::JsonObject,
) {
    let Some(auth_context_entity) = &state.auth_context_entity else {
        return;
    };
    if entity != auth_context_entity.as_ref() {
        return;
    }
    let Some(user_id) = data
        .get("userId")
        .and_then(Value::as_str)
        .and_then(|s| Uuid::parse_str(s).ok())
    else {
        return;
    };
    let Ok(tenant_id) = state.permissions.scoped_tenant(context) else {
        return;
    };
    state.context_attributes_cache.invalidate(tenant_id, user_id).await;
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
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => service_error_response(status, &error, message.as_deref(), field_errors),
        Err(e) => internal_error_response(e),
    }
}

#[derive(Deserialize)]
struct TransitionBody {
    version: i32,
    #[serde(default)]
    data: Option<HashMap<String, Value>>,
}

async fn transition_record(
    State(state): State<AppState>,
    Path((entity, id, action)): Path<(String, Uuid, String)>,
    AuthContext(context): AuthContext,
    Json(body): Json<TransitionBody>,
) -> Response {
    let payload: Option<metap_crud::JsonObject> = body.data.map(|d| d.into_iter().collect());
    match state
        .crud
        .transition(&entity, id, &action, body.version, payload.as_ref(), &context)
        .await
    {
        Ok(ServiceResult::Ok { data, .. }) => Json(json!({ "data": data })).into_response(),
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => service_error_response(status, &error, message.as_deref(), field_errors),
        Err(e) => internal_error_response(e),
    }
}

/// `POST /api/{entity}/aggregate`'s body is `metap_query::AggregateSpec` directly — the same raw
/// wire shape gRPC's `Aggregate` RPC and GraphQL's `aggregate` field parse too (`AggregateSpec`'s
/// own doc comment). A `POST` rather than query params on a `GET` because the metric/filter set
/// is a structured object, not a flat string map — and because a dashboard sends several of these
/// per screen, where a long, hand-encoded query string is the part most likely to be built wrong.
/// It reads no state and writes none, so it is a `POST` that is semantically a read; `AuthContext`
/// gates it exactly like `list_records`.
async fn aggregate_records(
    State(state): State<AppState>,
    Path(entity): Path<String>,
    AuthContext(context): AuthContext,
    Json(spec): Json<metap_query::AggregateSpec>,
) -> Response {
    let input = match spec.into_input() {
        Ok(input) => input,
        Err(e) => return service_error_response(400, "invalid_aggregate", Some(&e.to_string()), None),
    };

    match state.crud.aggregate(&entity, &input, &context).await {
        Ok(ServiceResult::Ok { data, .. }) => Json(json!({ "data": data })).into_response(),
        Ok(ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        }) => service_error_response(status, &error, message.as_deref(), field_errors),
        Err(e) => internal_error_response(e),
    }
}

pub fn router() -> Router<AppState> {
    Router::new()
        .route("/api/{entity}", get(list_records).post(create_record))
        // Before `/api/{entity}/{id}` in source order for readability only — axum's router
        // matches a literal segment ahead of a `{param}` one regardless of registration order,
        // so `aggregate` can never be swallowed as an `{id}` (it would fail `Uuid` parsing
        // anyway, which is what made this collision safe to add at all).
        .route("/api/{entity}/aggregate", post(aggregate_records))
        .route(
            "/api/{entity}/{id}",
            get(get_record).patch(update_record).delete(delete_record),
        )
        .route("/api/{entity}/{id}/transitions/{action}", post(transition_record))
}
