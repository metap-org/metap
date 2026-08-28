use metap_permission::{EntityAction, RequestContext};
use uuid::Uuid;

use crate::dto::{RecordCapabilities, RecordDto};
use crate::result::ServiceResult;

use super::helpers::{compute_capabilities, fetch_existing, forbidden, mask_record_for_read, router_unavailable};
use super::CrudService;

impl CrudService {
    pub async fn get(
        &self,
        entity_name: &str,
        id: Uuid,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>> {
        let Some(entity) = self.get_entity(entity_name) else {
            tracing::debug!(entity = entity_name, "get rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_read_entity(context, &entity.name).await?;
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
            tracing::debug!(entity = entity.name, record_id = %id, "get rejected: record not found");
            return Ok(ServiceResult::err(404, "record_not_found"));
        };
        tx.commit().await?;

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        // Enriched once for every record-level action `get` cares about (the Read check below,
        // plus Update/Transition inside `compute_capabilities`) so a cross-record fetch never
        // runs twice for the same relation within one call.
        let enriched = self
            .enrich_record_for_actions(
                &entity,
                &snapshot,
                &[EntityAction::Read, EntityAction::Update, EntityAction::Transition],
                tenant_id,
                &existing.data,
            )
            .await?;
        let record_decision = snapshot.can_perform_record_condition(context, &enriched, EntityAction::Read);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        let capabilities = compute_capabilities(&entity, context, &snapshot, &enriched);
        let masked = mask_record_for_read(&entity, context, &snapshot, existing);
        Ok(ServiceResult::ok((masked, capabilities)))
    }
}
