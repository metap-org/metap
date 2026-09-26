//! `POST /graphql/public` — unauthenticated GraphQL counterpart to `metap_http::routes::
//! tenant_config::public_config` (`GET /public/config`). A **separate, minimal `Schema`**, not a
//! field bolted onto the main authenticated schema (`crate::router`/`router_with_federation`) —
//! this schema has exactly one field, `publicConfig`, and nothing else reachable from it at all.
//! That is the safety property this design relies on: no entity data, no admin field, nothing
//! that needs an auth check anyone could forget to add — there is simply nothing else here to
//! query, by construction rather than by a runtime guard on every other field.

use std::sync::Arc;

use async_graphql::dynamic::{Field, FieldFuture, FieldValue, Object, Schema, TypeRef};
use async_graphql::Value as GqlValue;
use async_graphql_axum::{GraphQLRequest, GraphQLResponse};
use axum::http::HeaderMap;
use axum::routing::post;
use axum::Router;
use metap_http::AppState;
use serde_json::{json, Value};
use uuid::Uuid;

const JSON_SCALAR: &str = "Json";

fn json_value(v: Value) -> FieldValue<'static> {
    match GqlValue::from_json(v) {
        Ok(v) => FieldValue::value(v),
        Err(_) => FieldValue::NULL,
    }
}

fn json_list(values: Vec<Value>) -> FieldValue<'static> {
    FieldValue::list(values.into_iter().map(json_value))
}

/// Same hostname resolution `routes/tenant_config.rs`'s `request_hostname`/`resolve_hostname` use
/// — `Host` header only, never `X-Forwarded-Host` (attacker-controlled branding-selection would
/// otherwise be possible), an unrecognized/missing hostname degrading to fleet-wide defaults
/// rather than an error (this endpoint must never become a tenant-existence oracle).
async fn resolve_tenant(state: &AppState, headers: &HeaderMap) -> Option<Uuid> {
    let host = headers
        .get(axum::http::header::HOST)
        .and_then(|v| v.to_str().ok())
        .and_then(metap_control::normalize_hostname)?;
    let pool = state.pool.clone();
    let host_owned = host.clone();
    state
        .tenant_hostname_cache
        .get_with(&host, move || async move {
            metap_control::tenant_id_for_hostname(&pool, &host_owned)
                .await
                .map_err(anyhow::Error::from)
        })
        .await
        .unwrap_or(None)
}

fn build_schema() -> Schema {
    let query = Object::new("Query").field(Field::new(
        "publicConfig",
        TypeRef::named_nn_list_nn(JSON_SCALAR),
        |ctx| {
            FieldFuture::new(async move {
                let state = ctx.data_unchecked::<AppState>();
                let headers = ctx.data_unchecked::<HeaderMap>();
                let tenant_id = resolve_tenant(state, headers).await;
                let effective = match tenant_id {
                    Some(id) => state.effective_config(id).await,
                    None => state.config.effective(None),
                };
                let items: Vec<Value> = effective
                    .public_view()
                    .into_iter()
                    .map(|(key, value)| json!({ "key": key, "value": value }))
                    .collect();
                Ok(Some(json_list(items)))
            })
        },
    ));
    Schema::build("Query", None, None)
        .register(async_graphql::dynamic::Scalar::new(JSON_SCALAR))
        .register(query)
        .finish()
        .expect("public schema is static (no MetadataRegistry involved) and always valid")
}

/// Mounts `POST /graphql/public`. Unlike [`crate::router`], there is no `SchemaHolder`/hot-reload
/// here — this schema has no entity fields at all, so a low-code publish/rollback
/// (`MetadataRegistry` hot-swap) has nothing to invalidate; it is built once, at router
/// construction time. `state` is cloned once into the handler closure rather than extracted via
/// `State<AppState>` per request — every field this schema reads (`state.config`,
/// `state.tenant_hostname_cache`, `state.pool`) is set once at boot and never hot-swapped, unlike
/// `state.crud`/`state.metadata`.
pub fn public_router(state: &AppState) -> Router<AppState> {
    let schema = Arc::new(build_schema());
    let state = state.clone();
    Router::new().route(
        "/graphql/public",
        post(move |headers: HeaderMap, req: GraphQLRequest| {
            let schema = schema.clone();
            let state = state.clone();
            async move {
                let request = req.into_inner().data(state).data(headers);
                GraphQLResponse::from(schema.execute(request).await)
            }
        }),
    )
}
