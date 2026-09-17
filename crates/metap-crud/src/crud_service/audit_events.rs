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

        let visibility = self.audit_diff_visibility(entity_name, record_id, context).await?;
        for event in &mut events {
            mask_diff(&mut event.diff, &visibility);
        }
        Ok(ServiceResult::ok(events))
    }

    /// How much of this record's audit diffs the caller may see. Two levels, both closing the
    /// same hazard at different heights: authorization that depends on **mutable record state**
    /// cannot be used to decide access to values describing states the record no longer has.
    ///
    /// The record level comes first and is all-or-nothing: if the caller's record-level read
    /// access itself carries a condition (`record_access_is_state_dependent`), no field values
    /// are served at all. Record-level access gates the whole trail, so a caller who steers the
    /// record into satisfying that condition gains its entire history — including values written
    /// while they had no access. This one bites without any malicious edit too: a record that
    /// legitimately changes owner or department over time carries the previous owner's values.
    /// The entries themselves (action, actor, timestamp, version, reason) are still served, so
    /// the trail keeps answering who changed this record and when.
    ///
    /// The field level then delegates to `PermissionSnapshot::filter_readable_fields` — the exact
    /// function the ordinary read path (`helpers::row_to_dto_masked`) already runs — rather than
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
    /// A field whose read policy is conditional on the record's own values is excluded outright
    /// (`record_state_dependent_read_fields`), not merely evaluated against the current state.
    /// Evaluating it was the original fix and it was **reversible**: the check's subject is a
    /// record the caller can edit, so flipping the field the policy conditions on — a field they
    /// may legitimately write — retroactively unmasked the whole history, handing back past
    /// values the ordinary read path never returns (it only ever returns the current one).
    /// Confirmed live, see this module's regression test. The precise alternative, evaluating
    /// each entry against its own historical state, is not available: the audit table stores
    /// only per-entry diffs, never full state snapshots.
    ///
    /// The cost is deliberate over-masking: a field carrying both an unconditional grant and a
    /// conditional one is excluded too, even though the unconditional grant alone would justify
    /// showing it. Over-masking history is the safe direction; under-masking is the bug above.
    /// Admins keep the unconditional bypass `filter_readable_fields` already gives them.
    async fn audit_diff_visibility(
        &self,
        entity_name: &str,
        record_id: Uuid,
        context: &RequestContext,
    ) -> anyhow::Result<DiffVisibility> {
        let Some(entity) = self.get_entity(entity_name) else {
            return Ok(DiffVisibility::Nothing);
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

        // Record-level access itself hinges on mutable record state — no field values at all.
        if snapshot.record_access_is_state_dependent(context, EntityAction::Read) {
            return Ok(DiffVisibility::Nothing);
        }

        let state_dependent = snapshot.record_state_dependent_read_fields(context);
        Ok(DiffVisibility::Fields(
            snapshot
                .filter_readable_fields(context, &probe)
                .into_iter()
                .map(|(field, _)| field)
                .filter(|field| !state_dependent.contains(field))
                .collect(),
        ))
    }
}

/// How much of an audit entry's `diff` this caller may see.
///
/// `Nothing` is not the same as "no rows": the entry itself (action, actor, timestamp, version,
/// transition, reason) is still served, only the before/after *values* are withheld. That split
/// is the point — the compliance question an audit trail exists to answer is who changed what
/// record and when, and that survives intact for a caller whose access is too state-dependent to
/// let them read historical values safely.
enum DiffVisibility {
    Nothing,
    Fields(std::collections::HashSet<String>),
}

/// Drops every key of an audit entry's `{field: {before, after}}` diff the caller may not see.
/// A non-object `diff` (nothing writes one today, but the column is plain `jsonb`) is replaced
/// wholesale rather than left alone — "there are no field keys to mask" is not a reason to serve
/// an unknown shape to a caller who may see no values at all.
fn mask_diff(diff: &mut serde_json::Value, visibility: &DiffVisibility) {
    let readable = match visibility {
        DiffVisibility::Nothing => {
            *diff = serde_json::Value::Object(serde_json::Map::new());
            return;
        }
        DiffVisibility::Fields(readable) => readable,
    };
    let Some(map) = diff.as_object_mut() else {
        *diff = serde_json::Value::Object(serde_json::Map::new());
        return;
    };
    map.retain(|field, _| readable.contains(field));
}
