//! `GrpcBackend` — implements `metap_crud::RecordBackend` by calling a remote `RecordService`
//! over gRPC, the client-side counterpart to `service.rs`'s server implementation. This is the
//! concrete "remote" arm of the local-vs-remote dispatch seam `RecordBackend` exists for (see
//! that trait's doc comment in `metap-crud`): a BFF gateway (`crates/graphql-gateway`) that has
//! no `CrudService`/Postgres of its own uses one `GrpcBackend` per upstream microservice instead.
//!
//! **Auth is a single static, pre-minted service JWT per upstream** — the same
//! `CRON_SERVICE_JWT` pattern `cron-scheduler` already uses for its own binary-to-binary HTTP
//! calls, not per-caller identity propagation. Every call this backend makes to a given upstream
//! authenticates as that one fixed service identity, regardless of who called the gateway.
//! Propagating the gateway caller's own identity downstream would need a shared JWKS trust root
//! between every upstream (`metap-jwks` exists, but `GrpcRecordService::auth` today only accepts
//! `TokenVerifier::Static` OR `TokenVerifier::Jwks` per-service, wired by that service's own
//! operator) — deliberately out of scope for this v1.
//!
//! **No batch RPC exists on the wire** (`RecordService`'s proto has no `GetMany`) — `get_many`
//! is implemented as one `get` call per id, sequentially. This still gets the DataLoader's actual
//! benefit (N `Reference` field resolutions in one GraphQL query coalesce into one `get_many`
//! *call*, not N separate resolver invocations racing each other), just not N-to-1 on the wire.
//! Worth revisiting if this becomes a real bottleneck (e.g. a `filter: id in [...]` `List` call).

use anyhow::{anyhow, Context as _};
use metap_crud::{JsonObject, PageInfo, RecordBackend, RecordCapabilities, RecordDto, ServiceResult};
use metap_permission::RequestContext;
use metap_query::ListInput;
use prost_types::Struct as PbStruct;
use serde_json::{json, Value as JsonValue};
use tonic::metadata::MetadataValue;
use tonic::transport::Channel;
use tonic::{Code, Request, Status};
use uuid::Uuid;

use crate::convert::{json_to_struct, struct_to_json};
use crate::pb::record_service_client::RecordServiceClient;
use crate::pb::{CreateRequest, DeleteRequest, GetRequest, ListRequest, TransitionRequest, UpdateRequest};

pub struct GrpcBackend {
    client: RecordServiceClient<Channel>,
    service_jwt: String,
}

impl GrpcBackend {
    /// `addr` is a full URI (`http://host:port` — plaintext, matching `serve()`'s own TLS-optional
    /// default; a deployment that terminates TLS between services passes an `https://` URI and
    /// `tonic`'s own TLS feature handles the rest, same as any other `tonic` client).
    pub async fn connect(addr: impl Into<String>, service_jwt: impl Into<String>) -> anyhow::Result<Self> {
        let addr = addr.into();
        let client = RecordServiceClient::connect(addr.clone())
            .await
            .with_context(|| format!("connecting to gRPC upstream at {addr}"))?;
        Ok(Self {
            client,
            service_jwt: service_jwt.into(),
        })
    }

    /// The one choke point every outbound RPC goes through — attaching the current request's
    /// W3C `traceparent` here (via `attach_traceparent`), rather than at each of the 7 call
    /// sites below, means any caller running inside an incoming request (a GraphQL resolver, a
    /// REST route) automatically propagates its trace context to whichever upstream
    /// microservice this backend calls — no per-call-site change needed for mesh (Istio/Envoy,
    /// Linkerd) interop.
    fn signed_request<T>(&self, message: T) -> anyhow::Result<Request<T>> {
        let mut request = Request::new(message);
        let value = MetadataValue::try_from(format!("Bearer {}", self.service_jwt))
            .context("service JWT is not valid gRPC metadata")?;
        request.metadata_mut().insert("authorization", value);
        attach_traceparent(&mut request)?;
        Ok(request)
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

/// The reverse of `crate::status::error_to_status` — necessarily lossy (a `tonic::Status` only
/// carries a `Code` + text, not `ServiceResult::Err`'s original numeric HTTP status/error
/// code/field_errors map), so this reconstructs the closest HTTP status for the `Code` rather
/// than recovering the original exactly. `Code::Internal`/`Unavailable`/anything else maps to
/// 502 (Bad Gateway) rather than 500 — this failure genuinely originates upstream, not in the
/// gateway itself.
fn status_to_service_err<T>(status: Status) -> ServiceResult<T> {
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
        _ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>> {
        let request = self.signed_request(ListRequest {
            entity_name: entity.to_string(),
            query: Some(json_to_struct(list_input_to_query(input))),
        })?;
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
        _ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>> {
        let request = self.signed_request(GetRequest {
            entity_name: entity.to_string(),
            id: id.to_string(),
        })?;
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
        _ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(CreateRequest {
            entity_name: entity.to_string(),
            data: Some(json_to_struct(JsonValue::Object(data.clone()))),
        })?;
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
        _ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(UpdateRequest {
            entity_name: entity.to_string(),
            id: id.to_string(),
            expected_version,
            data: Some(json_to_struct(JsonValue::Object(data.clone()))),
        })?;
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
        _ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(TransitionRequest {
            entity_name: entity.to_string(),
            id: id.to_string(),
            action: action.to_string(),
            expected_version,
            data: data.map(|d| json_to_struct(JsonValue::Object(d.clone()))),
        })?;
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
        _ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let request = self.signed_request(DeleteRequest {
            entity_name: entity.to_string(),
            id: id.to_string(),
            expected_version,
        })?;
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
}
