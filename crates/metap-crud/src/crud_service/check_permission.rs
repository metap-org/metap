use metap_permission::{EntityAction, RequestContext};

use crate::result::ServiceResult;

use super::helpers::{fetch_existing, forbidden, router_unavailable};
use super::CrudService;

impl CrudService {
    /// Checks whether `context` may perform `action` (`Read`/`Update`/`Delete` only) against an
    /// *existing* record — entity-level then record-level (ABAC), the same two-stage check
    /// `get`/`update`/`delete` each already run internally — without reading, writing, or
    /// returning the record itself.
    ///
    /// Exists for a resource that isn't itself a metadata-driven entity but is attached to one —
    /// `metap-attachments`' file attachments and `metap-workflow`'s transition-history log are
    /// the two real callers (`crates/metap-http/src/routes/attachments.rs`/`workflow_events.rs`).
    /// Both routes used to check only entity-level permission (`can_read/update/delete_entity`)
    /// before doing their own thing, which is exactly the gap a record-level (ABAC) policy is
    /// meant to close — a caller denied `GET /api/jira.issues/{id}` by a record-level condition
    /// could still list/download/delete that same record's attachments, or read its full
    /// transition history, since neither route re-checked the condition. Found in an
    /// architecture audit (`../metap-docs/docs/audits/03-metap-core-architecture-audit.md`,
    /// finding #1) — the same class of bug `crud_service.rs`'s `enrich_record_for_actions`'s doc
    /// comment on `hydrate_related_display` was fixed for on 2026-08-22: "a display convenience
    /// must not leak a value the caller would get a 403 for reading directly".
    pub async fn check_record_permission(
        &self,
        entity_name: &str,
        id: uuid::Uuid,
        action: EntityAction,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<()>> {
        let Some(entity) = self.get_entity(entity_name) else {
            tracing::debug!(
                entity = entity_name,
                "check_record_permission rejected: entity not found"
            );
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = match action {
            EntityAction::Read => self.permissions.can_read_entity(context, &entity.name).await?,
            EntityAction::Update => self.permissions.can_update_entity(context, &entity.name).await?,
            EntityAction::Delete => self.permissions.can_delete_entity(context, &entity.name).await?,
            EntityAction::Create | EntityAction::Transition => {
                anyhow::bail!("check_record_permission only supports Read/Update/Delete, got {action:?}")
            }
        };
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let mut tx = match self.router.begin(tenant_id.into()).await {
            Ok(tx) => tx,
            Err(e) => {
                if let Some(result) = router_unavailable(&e) {
                    return Ok(result);
                }
                return Err(e);
            }
        };
        let Some(existing) = fetch_existing(&mut *tx, id, tenant_id, &entity).await? else {
            tracing::debug!(entity = entity.name, record_id = %id, "check_record_permission rejected: record not found");
            return Ok(ServiceResult::err(404, "record_not_found"));
        };
        tx.commit().await?;

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let enriched = self
            .enrich_record_for_actions(&entity, &snapshot, &[action], tenant_id, &existing.data)
            .await?;
        let record_decision = snapshot.can_perform_record_condition(context, &enriched, action);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        Ok(ServiceResult::ok(()))
    }
}
