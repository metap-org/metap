//! `RecordServiceServer`'s handler implementation — the gRPC counterpart to
//! `crates/metap-http/src/routes/records.rs`. Every RPC follows the same three steps: authenticate
//! (`crate::auth::authenticate`), convert the request's `Struct` payload to `serde_json`, call the
//! matching `metap_crud::CrudService` method, convert the result back. No entity-specific code
//! anywhere — same generic-over-metadata shape REST/OpenAPI already have.

use std::sync::Arc;

use metap_crud::CrudService;
use tonic::{Request, Response, Status};
use uuid::Uuid;

use crate::auth::{authenticate, AuthConfig};
use crate::convert::{json_to_struct, struct_to_json};
use crate::list_input::list_input_from_query;
use crate::pb::record_service_server::RecordService;
use crate::pb::{
    AggregateRequest, AggregateResponse, CreateRequest, DeleteRequest, GetRequest, GetResponse, ListRequest,
    ListResponse, RecordResponse, TransitionRequest, UpdateRequest,
};
use crate::status::{error_to_status, internal, service_result_to_status};

pub struct GrpcRecordService {
    crud: Arc<CrudService>,
    auth: AuthConfig,
}

impl GrpcRecordService {
    pub fn new(crud: Arc<CrudService>, auth: AuthConfig) -> Self {
        Self { crud, auth }
    }
}

fn parse_id(id: &str) -> Result<Uuid, Status> {
    Uuid::parse_str(id).map_err(|_| Status::invalid_argument("`id` must be a UUID"))
}

/// Fills in the `entity`/`record_id` fields `serve.rs`'s access-log span reserves as `Empty` —
/// every RPC here shares one generic name (`RecordService.List`, etc.) regardless of entity, so
/// the access log can't distinguish a `waf.zones` call from a `waf.scan_jobs` one without this.
/// Called from inside each handler below, which runs within that span (tonic/tower_http
/// `.instrument()` the whole request future), so `Span::current()` here *is* that span —
/// recording a field name that span's `info_span!` never declared is a silent no-op, so this
/// stays harmless in a context with no such span (e.g. a unit test constructing
/// `GrpcRecordService` directly, no `serve()` layer wrapping it).
fn record_span_fields(entity: &str, record_id: Option<&str>) {
    let span = tracing::Span::current();
    span.record("entity", entity);
    if let Some(id) = record_id {
        span.record("record_id", id);
    }
}

#[tonic::async_trait]
impl RecordService for GrpcRecordService {
    async fn list(&self, request: Request<ListRequest>) -> Result<Response<ListResponse>, Status> {
        let context = authenticate(request.metadata(), &self.auth).await?;
        let req = request.into_inner();
        record_span_fields(&req.entity_name, None);
        let query = req.query.map(struct_to_json).and_then(|v| v.as_object().cloned());
        let input = list_input_from_query(query)?;

        let result = self
            .crud
            .list(&req.entity_name, &input, &context)
            .await
            .map_err(internal)?;
        let (records, next_cursor, has_more) = match result {
            metap_crud::ServiceResult::Ok { data, page } => {
                let next_cursor = page.as_ref().and_then(|p| p.next_cursor.clone()).unwrap_or_default();
                let has_more = page.as_ref().map(|p| p.next_cursor.is_some()).unwrap_or(false);
                (data, next_cursor, has_more)
            }
            metap_crud::ServiceResult::Err {
                status,
                error,
                message,
                field_errors,
            } => return Err(error_to_status(status, error, message, field_errors)),
        };

        Ok(Response::new(ListResponse {
            records: records
                .into_iter()
                .map(|r| json_to_struct(serde_json::to_value(r).unwrap_or_default()))
                .collect(),
            next_cursor,
            has_more,
        }))
    }

    async fn get(&self, request: Request<GetRequest>) -> Result<Response<GetResponse>, Status> {
        let context = authenticate(request.metadata(), &self.auth).await?;
        let req = request.into_inner();
        record_span_fields(&req.entity_name, Some(&req.id));
        let id = parse_id(&req.id)?;

        let result = self.crud.get(&req.entity_name, id, &context).await.map_err(internal)?;
        let (record, capabilities) = service_result_to_status(result)?;
        Ok(Response::new(GetResponse {
            record: Some(json_to_struct(serde_json::to_value(record).unwrap_or_default())),
            capabilities: Some(json_to_struct(serde_json::to_value(capabilities).unwrap_or_default())),
        }))
    }

    async fn create(&self, request: Request<CreateRequest>) -> Result<Response<RecordResponse>, Status> {
        let context = authenticate(request.metadata(), &self.auth).await?;
        let req = request.into_inner();
        record_span_fields(&req.entity_name, None);
        let data = req.data.map(struct_to_json).unwrap_or_default();
        let Some(data) = data.as_object().cloned() else {
            return Err(Status::invalid_argument("`data` must be an object"));
        };

        let result = self
            .crud
            .create(&req.entity_name, &data, &context)
            .await
            .map_err(internal)?;
        let record = service_result_to_status(result)?;
        Ok(Response::new(RecordResponse {
            record: Some(json_to_struct(serde_json::to_value(record).unwrap_or_default())),
        }))
    }

    async fn update(&self, request: Request<UpdateRequest>) -> Result<Response<RecordResponse>, Status> {
        let context = authenticate(request.metadata(), &self.auth).await?;
        let req = request.into_inner();
        record_span_fields(&req.entity_name, Some(&req.id));
        let id = parse_id(&req.id)?;
        let data = req.data.map(struct_to_json).unwrap_or_default();
        let Some(data) = data.as_object().cloned() else {
            return Err(Status::invalid_argument("`data` must be an object"));
        };

        let result = self
            .crud
            .update(&req.entity_name, id, req.expected_version, &data, &context)
            .await
            .map_err(internal)?;
        let record = service_result_to_status(result)?;
        Ok(Response::new(RecordResponse {
            record: Some(json_to_struct(serde_json::to_value(record).unwrap_or_default())),
        }))
    }

    async fn transition(&self, request: Request<TransitionRequest>) -> Result<Response<RecordResponse>, Status> {
        let context = authenticate(request.metadata(), &self.auth).await?;
        let req = request.into_inner();
        record_span_fields(&req.entity_name, Some(&req.id));
        let id = parse_id(&req.id)?;
        let data = req.data.map(struct_to_json).and_then(|v| v.as_object().cloned());

        let result = self
            .crud
            .transition(
                &req.entity_name,
                id,
                &req.action,
                req.expected_version,
                data.as_ref(),
                &context,
            )
            .await
            .map_err(internal)?;
        let record = service_result_to_status(result)?;
        Ok(Response::new(RecordResponse {
            record: Some(json_to_struct(serde_json::to_value(record).unwrap_or_default())),
        }))
    }

    async fn delete(&self, request: Request<DeleteRequest>) -> Result<Response<RecordResponse>, Status> {
        let context = authenticate(request.metadata(), &self.auth).await?;
        let req = request.into_inner();
        record_span_fields(&req.entity_name, Some(&req.id));
        let id = parse_id(&req.id)?;

        let result = self
            .crud
            .delete(&req.entity_name, id, req.expected_version, &context)
            .await
            .map_err(internal)?;
        let record = service_result_to_status(result)?;
        Ok(Response::new(RecordResponse {
            record: Some(json_to_struct(serde_json::to_value(record).unwrap_or_default())),
        }))
    }

    async fn aggregate(&self, request: Request<AggregateRequest>) -> Result<Response<AggregateResponse>, Status> {
        let context = authenticate(request.metadata(), &self.auth).await?;
        let req = request.into_inner();
        record_span_fields(&req.entity_name, None);
        let spec: metap_query::AggregateSpec = req
            .spec
            .map(struct_to_json)
            .map(serde_json::from_value)
            .transpose()
            .map_err(|e| Status::invalid_argument(format!("invalid aggregate spec: {e}")))?
            .unwrap_or_default();
        let input = spec.into_input().map_err(|e| Status::invalid_argument(e.to_string()))?;

        let result = self
            .crud
            .aggregate(&req.entity_name, &input, &context)
            .await
            .map_err(internal)?;
        let rows = service_result_to_status(result)?;
        Ok(Response::new(AggregateResponse {
            rows: rows.into_iter().map(json_to_struct).collect(),
        }))
    }
}
