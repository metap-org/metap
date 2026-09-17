use metap_audit::AuditTrailEntryRow;
use metap_permission::{EntityAction, RequestContext};
use uuid::Uuid;

use crate::result::ServiceResult;

use super::helpers::fetch_existing;
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
        let mut events = store.list_for_record(tenant_id, entity_name, record_id).await?;

        let readable = self.readable_field_names(entity_name, record_id, context).await?;
        for event in &mut events {
            mask_diff(&mut event.diff, &readable);
        }
        Ok(ServiceResult::ok(events))
    }

    /// Which of `entity_name`'s fields this caller may read, for masking audit diffs. Delegates
    /// the actual decision to `PermissionSnapshot::filter_readable_fields` — the exact function
    /// the ordinary read path (`helpers::row_to_dto_masked`) already runs — rather than
    /// reimplementing field-policy evaluation here, since a second implementation is precisely
    /// how the two drift apart.
    ///
    /// Two details that a naive call would get wrong:
    /// - `filter_readable_fields` only ever returns keys the record it was handed actually has,
    ///   so a field that is readable but currently null/absent would be dropped and its history
    ///   masked for no reason. The probe therefore starts from the live record's `data` and adds
    ///   an explicit null for every declared field missing from it.
    /// - Field policies may be conditional on the record's own values, so the probe has to carry
    ///   the real current values, not an empty object.
    ///
    /// Readability is decided against the record's **current** state, deliberately, not against
    /// each historical entry's own before/after: what a caller may see in the history is exactly
    /// what they may see on the record today. Deciding per-entry would let a field readable only
    /// in some past state leak through the history after it stopped being readable.
    async fn readable_field_names(
        &self,
        entity_name: &str,
        record_id: Uuid,
        context: &RequestContext,
    ) -> anyhow::Result<std::collections::HashSet<String>> {
        let Some(entity) = self.get_entity(entity_name) else {
            return Ok(std::collections::HashSet::new());
        };
        let tenant_id = self.permissions.scoped_tenant(context)?;

        let mut tx = self.router.begin(tenant_id.into()).await?;
        let existing = fetch_existing(&mut *tx, record_id, tenant_id, &entity).await?;
        tx.commit().await?;

        let mut probe = existing.map(|record| record.data).unwrap_or_default();
        for field in &entity.fields {
            probe.entry(field.name.clone()).or_insert(serde_json::Value::Null);
        }

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        Ok(snapshot
            .filter_readable_fields(context, &probe)
            .into_iter()
            .map(|(field, _)| field)
            .collect())
    }
}

/// Drops every key of an audit entry's `{field: {before, after}}` diff that `readable` doesn't
/// name. A non-object `diff` (nothing writes one today, but the column is plain `jsonb`) is left
/// alone — there are no field keys in it to mask.
fn mask_diff(diff: &mut serde_json::Value, readable: &std::collections::HashSet<String>) {
    let Some(map) = diff.as_object_mut() else { return };
    map.retain(|field, _| readable.contains(field));
}
