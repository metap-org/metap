//! `RecordBackend` — the seam that lets a GraphQL resolver (`metap-graphql`) or any other
//! generic caller be written once against "however this entity's records actually get read and
//! written," without hardcoding whether that means an in-process `CrudService` call or a remote
//! call to another, separately-deployed microservice. This is exactly the "local-vs-remote
//! dispatch" seam `docs/architectures/04-strategy.md` names as the one piece intentionally left
//! undesigned until a real split-deploy trigger existed — a BFF gateway aggregating across
//! multiple already-separately-deployed microservices (`crates/metap-graphql-gateway`) is that trigger.
//!
//! Lives in `metap-crud`, not `metap-graphql` or a new crate: every type in the trait's
//! signature (`ServiceResult`, `RecordDto`, `RecordCapabilities`, `JsonObject`, and
//! `metap_query::ListInput`, which this crate already depends on for `CrudService::list`) already
//! lives here. Both `metap-graphql` (the trait's primary consumer) and `metap-grpc` (whose
//! `client` module implements it for a remote gRPC-backed service) already depend on
//! `metap-crud`, so defining the trait here adds no new dependency to either and creates no
//! dependency-direction question between them.
//!
//! `CrudService` implements this trait directly (see below) — a thin, logic-free delegation,
//! since its six methods already match the trait's shape exactly.

use uuid::Uuid;

use metap_permission::RequestContext;
use metap_query::{AggregateSpec, ListInput};

use crate::crud_service::CrudService;
use crate::dto::{JsonObject, RecordCapabilities, RecordDto};
use crate::result::ServiceResult;

#[async_trait::async_trait]
pub trait RecordBackend: Send + Sync {
    async fn list(
        &self,
        entity: &str,
        input: &ListInput,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>>;

    async fn get(
        &self,
        entity: &str,
        id: Uuid,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>>;

    async fn get_many(
        &self,
        entity: &str,
        ids: &[Uuid],
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<(Uuid, RecordDto, RecordCapabilities)>>>;

    async fn create(
        &self,
        entity: &str,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>>;

    async fn update(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>>;

    #[allow(clippy::too_many_arguments)]
    async fn transition(
        &self,
        entity: &str,
        id: Uuid,
        action: &str,
        expected_version: i32,
        data: Option<&JsonObject>,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>>;

    async fn delete(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>>;

    /// The `GROUP BY`/`COUNT`/`SUM` counterpart to `list` — see `CrudService::aggregate`'s own
    /// doc comment for the permission/masking rules this must preserve regardless of which
    /// backend actually runs it (in-process `CrudService`, or a remote `GrpcBackend` call).
    /// Added 2026-09-04 alongside every transport this trait already serves (REST already had
    /// its own direct `CrudService::aggregate` call, unaffected by this) so gRPC/GraphQL callers
    /// (`crates/metap-graphql-gateway`, `metap-graphql`) get the same capability.
    async fn aggregate(
        &self,
        entity: &str,
        spec: &AggregateSpec,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<serde_json::Value>>>;
}

#[async_trait::async_trait]
impl RecordBackend for CrudService {
    async fn list(
        &self,
        entity: &str,
        input: &ListInput,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>> {
        self.list(entity, input, ctx).await
    }

    async fn get(
        &self,
        entity: &str,
        id: Uuid,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>> {
        self.get(entity, id, ctx).await
    }

    async fn get_many(
        &self,
        entity: &str,
        ids: &[Uuid],
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<(Uuid, RecordDto, RecordCapabilities)>>> {
        self.get_many(entity, ids, ctx).await
    }

    async fn create(
        &self,
        entity: &str,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        self.create(entity, data, ctx).await
    }

    async fn update(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        self.update(entity, id, expected_version, data, ctx).await
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
        self.transition(entity, id, action, expected_version, data, ctx).await
    }

    async fn delete(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        self.delete(entity, id, expected_version, ctx).await
    }

    /// A malformed `spec` (bad metric/bucket string) surfaces as a `400`-shaped `ServiceResult`,
    /// not an `Err` — `Err` here means an unexpected failure (e.g. a DB error), the same
    /// convention every other method in this impl already follows for its own input parsing.
    async fn aggregate(
        &self,
        entity: &str,
        spec: &AggregateSpec,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<serde_json::Value>>> {
        let input = match spec.clone().into_input() {
            Ok(input) => input,
            Err(e) => return Ok(ServiceResult::err_with_message(400, "invalid_aggregate", e.to_string())),
        };
        self.aggregate(entity, &input, ctx).await
    }
}
