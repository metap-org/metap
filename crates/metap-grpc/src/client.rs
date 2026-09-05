//! `GrpcBackend` — implements `metap_crud::RecordBackend` by calling a remote `RecordService`
//! over gRPC, the client-side counterpart to `service.rs`'s server implementation. This is the
//! concrete "remote" arm of the local-vs-remote dispatch seam `RecordBackend` exists for (see
//! that trait's doc comment in `metap-crud`): a BFF gateway (`crates/metap-graphql-gateway`) that has
//! no `CrudService`/Postgres of its own uses one `GrpcBackend` per upstream microservice instead.
//!
//! **Auth prefers the caller's own forwarded identity, falling back to a self-refreshing service
//! account token per upstream.** `RequestContext::forwarded_bearer_token` (set by
//! `graphql-gateway/src/server.rs::authenticate` from the inbound request's own bearer token) is
//! used verbatim when present — this only works when the caller and every upstream verify
//! against the same signing keypair (true of every `metap-demo-waf` service today; a deployment
//! where they don't share one would need a JWKS-based re-mint instead, not this). When absent
//! (e.g. the gateway's own boot-time schema-discovery call, which has no inbound request to
//! forward from), falls back to `ServiceTokenSource::current()` — a token this process logged
//! into the upstream's own `POST /auth/login` with (email+password, a credential that doesn't
//! expire, unlike a hand-minted JWT) and refreshes in the background well before it expires. This
//! replaced a static, hand-minted-once `service_jwt` (2026-09-02) after that JWT's 1h TTL expired
//! in a running deployment and took the whole gateway down at boot (schema discovery got a 401).
//! `ServiceTokenSource` itself lives in `metap_runtime::service_token` (this module re-exports
//! it) — `cron-scheduler` is the second real caller, using it directly over REST (no gRPC), which
//! is why it moved out of this crate rather than staying `metap-grpc`-specific.
//!
//! **No batch RPC exists on the wire** (`RecordService`'s proto has no `GetMany`) — `get_many`
//! is implemented as one `get` call per id, sequentially. This still gets the DataLoader's actual
//! benefit (N `Reference` field resolutions in one GraphQL query coalesce into one `get_many`
//! *call*, not N separate resolver invocations racing each other), just not N-to-1 on the wire.
//! Worth revisiting if this becomes a real bottleneck (e.g. a `filter: id in [...]` `List` call).

use std::borrow::Cow;

use anyhow::{anyhow, Context as _};
use metap_crud::{JsonObject, PageInfo, RecordBackend, RecordCapabilities, RecordDto, ServiceResult};
use metap_permission::RequestContext;
use metap_query::{AggregateSpec, ListInput};
pub use metap_runtime::service_token::ServiceTokenSource;
use prost_types::Struct as PbStruct;
use serde_json::{json, Value as JsonValue};
use tonic::metadata::MetadataValue;
use tonic::transport::Channel;
use tonic::{Code, Request, Status};
use uuid::Uuid;

use crate::convert::{json_to_struct, struct_to_json};
use crate::pb::record_service_client::RecordServiceClient;
use crate::pb::{
    AggregateRequest, CreateRequest, DeleteRequest, GetRequest, ListRequest, TransitionRequest, UpdateRequest,
};

pub struct GrpcBackend {
    client: RecordServiceClient<Channel>,
    service_token: ServiceTokenSource,
}

impl GrpcBackend {
    /// `addr` is a full URI (`http://host:port` — plaintext, matching `serve()`'s own TLS-optional
    /// default; a deployment that terminates TLS between services passes an `https://` URI and
    /// `tonic`'s own TLS feature handles the rest, same as any other `tonic` client).
    pub async fn connect(addr: impl Into<String>, service_token: ServiceTokenSource) -> anyhow::Result<Self> {
        let addr = addr.into();
        let client = RecordServiceClient::connect(addr.clone())
            .await
            .with_context(|| format!("connecting to gRPC upstream at {addr}"))?;
        Ok(Self { client, service_token })
    }

    /// The one choke point every outbound RPC goes through — attaching the current request's
    /// W3C `traceparent` here (via `attach_traceparent`), rather than at each of the 7 call
    /// sites below, means any caller running inside an incoming request (a GraphQL resolver, a
    /// REST route) automatically propagates its trace context to whichever upstream
    /// microservice this backend calls — no per-call-site change needed for mesh (Istio/Envoy,
    /// Linkerd) interop. Bearer token is chosen by `pick_token` — see that function's doc
    /// comment for the forwarded-vs-service-account-token precedence.
    fn signed_request<T>(&self, message: T, ctx: &RequestContext) -> anyhow::Result<Request<T>> {
        let mut request = Request::new(message);
        let token = pick_token(ctx, &self.service_token);
        let value =
            MetadataValue::try_from(format!("Bearer {token}")).context("bearer token is not valid gRPC metadata")?;
        request.metadata_mut().insert("authorization", value);
        attach_traceparent(&mut request)?;
        Ok(request)
    }
}

/// Picks which bearer token an outbound RPC authenticates with: the caller's own forwarded token
/// when `ctx` carries one, else `service_token`'s current value. A free function, not inlined
/// into `signed_request`, so this precedence is unit-testable without a real `GrpcBackend`
/// connection.
fn pick_token<'a>(ctx: &'a RequestContext, service_token: &ServiceTokenSource) -> Cow<'a, str> {
    match ctx.forwarded_bearer_token.as_deref() {
        Some(forwarded) => Cow::Borrowed(forwarded),
        None => Cow::Owned((*service_token.current()).clone()),
    }
}

/// Attaches `metap_runtime::trace_context::current()`'s W3C `traceparent` to `request`'s gRPC
/// metadata, if this call is running inside one (`trace_context::scope`) — a no-op otherwise
/// (e.g. a boot-time or background call with no incoming request to propagate from), not an
/// error. A free function, not a `GrpcBackend` method, so it's testable without a real
/// connection (`GrpcBackend::connect` needs one).
fn attach_traceparent<T>(request: &mut Request<T>) -> anyhow::Result<()> {
    let Some(ctx) = metap_runtime::trace_context::current() else {
        return Ok(());
    };
    let header = ctx.to_traceparent_header();
    let value = MetadataValue::try_from(header.to_str().unwrap_or_default())
        .context("traceparent is not valid gRPC metadata")?;
    request.metadata_mut().insert("traceparent", value);
    Ok(())
}

/// The reverse of `crate::status::error_to_status`. **Lossless when the upstream speaks this
/// platform's error envelope** (audit 04 finding B#2, fixed 2026-09-03): the original numeric HTTP
/// status, the stable `error` code, and the whole `field_errors` map ride in the `Status`'s details
/// and are restored here exactly as `CrudService` produced them, so a validation failure arriving
/// through `graphql-gateway` still says *which fields* failed instead of only how many.
///
/// The `Code`-based reconstruction below stays as the fallback for a status with no details — an
/// older `metap` build, or an error raised by something that isn't a `metap` service at all (a mesh
/// sidecar, a proxy). There, `Code::Internal`/`Unavailable`/anything else maps to 502 (Bad Gateway)
/// rather than 500, since that failure genuinely originates upstream, not in the caller.
fn status_to_service_err<T>(status: Status) -> ServiceResult<T> {
    if let Some(details) = crate::status::error_details_from_status(&status) {
        return ServiceResult::Err {
            status: details.status,
            error: details.error,
            message: details.message,
            field_errors: details.field_errors,
        };
    }
    let http_status = match status.code() {
        Code::InvalidArgument => 400,
        Code::Unauthenticated => 401,
        Code::PermissionDenied => 403,
        Code::NotFound => 404,
        Code::Aborted => 409,
        _ => 502,
    };
    ServiceResult::err_with_message(http_status, "upstream_error", status.message().to_string())
}

fn deserialize_record(s: PbStruct) -> anyhow::Result<RecordDto> {
    serde_json::from_value(struct_to_json(s)).context("upstream record did not match RecordDto shape")
}

fn deserialize_capabilities(s: PbStruct) -> anyhow::Result<RecordCapabilities> {
    serde_json::from_value(struct_to_json(s)).context("upstream capabilities did not match RecordCapabilities shape")
}

/// Mirrors `crate::list_input::list_input_from_query`'s expected shape exactly (`filter`/`sort`/
/// `cursor`/`limit`/`listView`/`jql`) — the two functions are inverses of each other.
fn list_input_to_query(input: &ListInput) -> JsonValue {
    let mut filter = serde_json::Map::new();
    for (key, value) in &input.filters {
        filter.insert(key.clone(), JsonValue::String(value.clone()));
    }
    json!({
        "limit": input.limit,
        "sort": input.sort,
        "filter": filter,
        "cursor": input.cursor,
        "listView": input.list_view,
        "jql": input.jql,
    })
}

#[async_trait::async_trait]
impl RecordBackend for GrpcBackend {
    async fn list(
        &self,
        entity: &str,
        input: &ListInput,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>> {
        let request = self.signed_request(
            ListRequest {
                entity_name: entity.to_string(),
                query: Some(json_to_struct(list_input_to_query(input))),
            },
            ctx,
        )?;
        let mut client = self.client.clone();
        match client.list(request).await {
            Ok(response) => {
                let response = response.into_inner();
                let records = response
                    .records
                    .into_iter()
                    .map(deserialize_record)
                    .collect::<anyhow::Result<Vec<_>>>()?;
                let next_cursor = (!response.next_cursor.is_empty()).then_some(response.next_cursor);
                Ok(ServiceResult::ok_with_page(
                    records,
                    PageInfo {
                        limit: input.limit,
                        next_cursor,
                    },
                ))
            }
            Err(status) => Ok(status_to_service_err(status)),
        }
    }

    async fn get(
        &self,
        entity: &str,
        id: Uuid,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>> {
        let request = self.signed_request(
            GetRequest {
                entity_name: entity.to_string(),
                id: id.to_string(),
            },
            ctx,
        )?;
        let mut client = self.client.clone();
        match client.get(request).await {
            Ok(response) => {
                let response = response.into_inner();
                let record = response
                    .record
                    .ok_or_else(|| anyhow!("upstream get response missing record"))?;
                let capabilities = response
                    .capabilities
                    .ok_or_else(|| anyhow!("upstream get response missing capabilities"))?;
                Ok(ServiceResult::ok((
                    deserialize_record(record)?,
                    deserialize_capabilities(capabilities)?,
                )))
            }
            Err(status) => Ok(status_to_service_err(status)),
        }
    }

    async fn get_many(
        &self,
        entity: &str,
        ids: &[Uuid],
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<(Uuid, RecordDto, RecordCapabilities)>>> {
        let mut results = Vec::with_capacity(ids.len());
        for &id in ids {
            match self.get(entity, id, ctx).await? {
                ServiceResult::Ok {
                    data: (dto, capabilities),
                    ..
                } => results.push((id, dto, capabilities)),
                // Same convention `RecordLoader` already expects: a denied/missing id is simply
                // absent from the batch result rather than failing every other id in it.
                ServiceResult::Err { .. } => {}
            }
        }
        Ok(ServiceResult::ok(results))
    }

    async fn create(
        &self,
        entity: &str,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(
            CreateRequest {
                entity_name: entity.to_string(),
                data: Some(json_to_struct(JsonValue::Object(data.clone()))),
            },
            ctx,
        )?;
        let mut client = self.client.clone();
        match client.create(request).await {
            Ok(response) => {
                let record = response
                    .into_inner()
                    .record
                    .ok_or_else(|| anyhow!("upstream create response missing record"))?;
                Ok(ServiceResult::ok(deserialize_record(record)?))
            }
            Err(status) => Ok(status_to_service_err(status)),
        }
    }

    async fn update(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(
            UpdateRequest {
                entity_name: entity.to_string(),
                id: id.to_string(),
                expected_version,
                data: Some(json_to_struct(JsonValue::Object(data.clone()))),
            },
            ctx,
        )?;
        let mut client = self.client.clone();
        match client.update(request).await {
            Ok(response) => {
                let record = response
                    .into_inner()
                    .record
                    .ok_or_else(|| anyhow!("upstream update response missing record"))?;
                Ok(ServiceResult::ok(deserialize_record(record)?))
            }
            Err(status) => Ok(status_to_service_err(status)),
        }
    }

    async fn transition(
        &self,
        entity: &str,
        id: Uuid,
        action: &str,
        expected_version: i32,
        data: Option<&JsonObject>,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(
            TransitionRequest {
                entity_name: entity.to_string(),
                id: id.to_string(),
                action: action.to_string(),
                expected_version,
                data: data.map(|d| json_to_struct(JsonValue::Object(d.clone()))),
            },
            ctx,
        )?;
        let mut client = self.client.clone();
        match client.transition(request).await {
            Ok(response) => {
                let record = response
                    .into_inner()
                    .record
                    .ok_or_else(|| anyhow!("upstream transition response missing record"))?;
                Ok(ServiceResult::ok(deserialize_record(record)?))
            }
            Err(status) => Ok(status_to_service_err(status)),
        }
    }

    async fn delete(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(
            DeleteRequest {
                entity_name: entity.to_string(),
                id: id.to_string(),
                expected_version,
            },
            ctx,
        )?;
        let mut client = self.client.clone();
        match client.delete(request).await {
            Ok(response) => {
                let record = response
                    .into_inner()
                    .record
                    .ok_or_else(|| anyhow!("upstream delete response missing record"))?;
                Ok(ServiceResult::ok(deserialize_record(record)?))
            }
            Err(status) => Ok(status_to_service_err(status)),
        }
    }

    async fn aggregate(
        &self,
        entity: &str,
        spec: &AggregateSpec,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<JsonValue>>> {
        let request = self.signed_request(
            AggregateRequest {
                entity_name: entity.to_string(),
                spec: Some(json_to_struct(serde_json::to_value(spec)?)),
            },
            ctx,
        )?;
        let mut client = self.client.clone();
        match client.aggregate(request).await {
            Ok(response) => {
                let rows = response.into_inner().rows.into_iter().map(struct_to_json).collect();
                Ok(ServiceResult::ok(rows))
            }
            Err(status) => Ok(status_to_service_err(status)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn attach_traceparent_is_a_no_op_outside_a_scope() {
        let mut request = Request::new(());
        attach_traceparent(&mut request).unwrap();
        assert!(request.metadata().get("traceparent").is_none());
    }

    #[tokio::test]
    async fn attach_traceparent_sets_metadata_inside_a_scope() {
        let ctx = metap_runtime::trace_context::from_headers(&Default::default());
        let trace_id = ctx.trace_id.clone();
        metap_runtime::trace_context::scope(ctx, async {
            let mut request = Request::new(());
            attach_traceparent(&mut request).unwrap();
            let value = request.metadata().get("traceparent").unwrap().to_str().unwrap();
            assert!(value.contains(&trace_id));
        })
        .await;
    }

    fn base_ctx(forwarded_bearer_token: Option<String>) -> RequestContext {
        RequestContext {
            tenant_id: "t1".to_string(),
            user_id: Some("u1".to_string()),
            roles: None,
            function_id: None,
            context_attributes: None,
            forwarded_bearer_token,
        }
    }

    #[test]
    fn pick_token_prefers_the_forwarded_token_when_present() {
        let ctx = base_ctx(Some("user-token".to_string()));
        let service_token = ServiceTokenSource::from_static("service-jwt");
        assert_eq!(pick_token(&ctx, &service_token), "user-token");
    }

    #[test]
    fn pick_token_falls_back_to_the_service_jwt_when_absent() {
        let ctx = base_ctx(None);
        let service_token = ServiceTokenSource::from_static("service-jwt");
        assert_eq!(pick_token(&ctx, &service_token), "service-jwt");
    }
}
