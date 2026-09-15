use metap_audit::AuditTrailEntryRow;
use metap_permission::{EntityAction, RequestContext};
use uuid::Uuid;

use crate::result::ServiceResult;

use super::CrudService;

impl CrudService {
    /// Read side of `record_audit` — full create/update/delete/transition history for one
    /// record. Record-level (ABAC) read permission via `check_record_permission`, the same check
    /// `workflow-events`/attachments already run before serving anything attached to a record
    /// (see that method's own doc comment: a caller denied `GET /api/{entity}/{id}` by a
    /// record-level condition must not still read this record's full history through a side
    /// door). Returns whatever rows exist regardless of the entity's *current*
    /// `EntityAuditConfig.enabled` flag — toggling audit off must not make already-recorded
    /// history disappear, and `record_audit` already guarantees no rows were ever written for an
    /// entity that was never opted in, so there is nothing else to gate on here.
    pub async fn list_audit_events(
        &self,
        entity_name: &str,
        record_id: Uuid,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<AuditTrailEntryRow>>> {
        if let ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        } = self
            .check_record_permission(entity_name, record_id, EntityAction::Read, context)
            .await?
        {
            return Ok(ServiceResult::Err {
                status,
                error,
                message,
                field_errors,
            });
        }

        // No `AuditTrailStore` configured for this deployment at all (`CrudService::new`, not
        // `with_audit`) — same "pays nothing beyond the field read" no-op `record_audit` already
        // applies on the write side.
        let Some(store) = &self.audit else {
            return Ok(ServiceResult::ok(Vec::new()));
        };
        let tenant_id = self.permissions.scoped_tenant(context)?;
        let events = store.list_for_record(tenant_id, entity_name, record_id).await?;
        Ok(ServiceResult::ok(events))
    }
}
