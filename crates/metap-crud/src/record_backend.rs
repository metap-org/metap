//! `RecordBackend` — the seam that lets a GraphQL resolver (`metap-graphql`) or any other
//! generic caller be written once against "however this entity's records actually get read and
//! written," without hardcoding whether that means an in-process `CrudService` call or a remote
//! call to another, separately-deployed microservice. This is exactly the "local-vs-remote
//! dispatch" seam `docs/architectures/04-strategy.md` names as the one piece intentionally left
//! undesigned until a real split-deploy trigger existed — a BFF gateway aggregating across
//! multiple already-separately-deployed microservices (`crates/graphql-gateway`) is that trigger.
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
use metap_query::ListInput;

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
}
