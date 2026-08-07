//! Mirrors `packages/core/src/core/permission/permission-service.ts`.

use uuid::Uuid;

use crate::context::{EntityAction, PermissionDecision, RequestContext};
use crate::permission_snapshot::PermissionSnapshot;
use crate::policy_condition::{evaluate_policy_row, PolicyCondition};
use crate::policy_explainer::{explain_policies, PolicyExplanation};
use crate::policy_store::{ExplainOptions, PolicyRow, PolicySubject, PolicyStore};

pub struct PermissionService {
    store: Box<dyn PolicyStore>,
}

impl PermissionService {
    pub fn new(store: Box<dyn PolicyStore>) -> Self {
        Self { store }
    }

    /// `RequestContext.tenantId` is required and always derived from a verified JWT by the
    /// auth hook before this is ever called — an empty/unparseable value here means a real
    /// bug upstream, not a legitimate "no tenant" case. Fail loudly (matching the TS
    /// `scopedTenant`'s throw) rather than silently defaulting to a hardcoded tenant, which
    /// would mask that bug as wrong-but-quiet query results instead of a clear error.
    pub fn scoped_tenant(&self, context: &RequestContext) -> anyhow::Result<Uuid> {
        if context.tenant_id.is_empty() {
            anyhow::bail!("RequestContext.tenantId is required but was missing or empty");
        }
        Ok(Uuid::parse_str(&context.tenant_id)?)
    }

    async fn check_action(
        &self,
        context: &RequestContext,
        entity_name: &str,
        action: EntityAction,
    ) -> anyhow::Result<PermissionDecision> {
        if context.is_admin() {
            return Ok(PermissionDecision::allowed());
        }

        let tenant_id = self.scoped_tenant(context)?;
        let rows = self
            .store
            .find_context_policies(tenant_id, entity_name, action.as_str())
            .await?;

        if rows.is_empty() {
            return Ok(PermissionDecision::allowed());
        }

        let passed = rows.iter().any(|policy| evaluate_policy_row(policy, context, None));
        Ok(if passed { PermissionDecision::allowed() } else { PermissionDecision::forbidden() })
    }

    pub async fn can_read_entity(
        &self,
        context: &RequestContext,
        entity: &str,
    ) -> anyhow::Result<PermissionDecision> {
        self.check_action(context, entity, EntityAction::Read).await
    }

    pub async fn can_create_entity(
        &self,
        context: &RequestContext,
        entity: &str,
    ) -> anyhow::Result<PermissionDecision> {
        self.check_action(context, entity, EntityAction::Create).await
    }

    pub async fn can_update_entity(
        &self,
        context: &RequestContext,
        entity: &str,
    ) -> anyhow::Result<PermissionDecision> {
        self.check_action(context, entity, EntityAction::Update).await
    }

    pub async fn can_delete_entity(
        &self,
        context: &RequestContext,
        entity: &str,
    ) -> anyhow::Result<PermissionDecision> {
        self.check_action(context, entity, EntityAction::Delete).await
    }

    pub async fn load_snapshot(
        &self,
        tenant_id: Uuid,
        entity: &str,
    ) -> anyhow::Result<PermissionSnapshot> {
        PermissionSnapshot::load(self.store.as_ref(), tenant_id, entity).await
    }

    pub async fn list_policies(
        &self,
        tenant_id: Uuid,
        entity: Option<&str>,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        self.store.list_policies(tenant_id, entity).await
    }

    #[allow(clippy::too_many_arguments)]
    pub async fn create_policy(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        roles: Option<Vec<String>>,
        condition: Option<PolicyCondition>,
        created_by: Option<Uuid>,
        field: Option<&str>,
        subject: Option<PolicySubject>,
    ) -> anyhow::Result<PolicyRow> {
        self.store
            .create_policy(tenant_id, entity, action, roles, condition, created_by, field, subject)
            .await
    }

    pub async fn delete_policy(&self, tenant_id: Uuid, id: Uuid) -> anyhow::Result<()> {
        self.store.delete_policy(tenant_id, id).await
    }

    pub async fn explain(
        &self,
        context: &RequestContext,
        entity: &str,
        action: &str,
        field: Option<&str>,
        record: Option<&crate::permission_snapshot::JsonObject>,
    ) -> anyhow::Result<PolicyExplanation> {
        let tenant_id = self.scoped_tenant(context)?;

        let options = if let Some(field) = field {
            ExplainOptions { field: Some(field.to_string()), subject: None }
        } else if record.is_some() {
            ExplainOptions { field: None, subject: Some(PolicySubject::Record) }
        } else {
            ExplainOptions::default()
        };

        let rows = self.store.find_explain_policies(tenant_id, entity, action, &options).await?;
        Ok(explain_policies(&rows, context, record))
    }
}
