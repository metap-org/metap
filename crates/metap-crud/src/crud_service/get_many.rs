use std::collections::HashMap;

use metap_permission::{EntityAction, RequestContext};
use uuid::Uuid;

use crate::dto::{RecordCapabilities, RecordDto};
use crate::result::ServiceResult;

use super::helpers::{compute_capabilities, fetch_existing_batch, forbidden, mask_record_for_read, router_unavailable};
use super::CrudService;

impl CrudService {
    /// Batched counterpart to `get` — one query for every id instead of one `get` call per id.
    /// Exists specifically to give a `DataLoader` (`metap-graphql`'s resolver for `Reference`
    /// fields) a real batching primitive: without this, a DataLoader would still coalesce keys
    /// per tick but then have to issue N single `get` calls anyway, defeating the point. Runs
    /// the exact same permission/masking pipeline `get` does (one `load_snapshot` for the whole
    /// batch, then per-record enrichment/masking) — a caller can't see more or less through this
    /// than through N individual `get` calls, just faster.
    ///
    /// The returned `Vec` is reordered to match `ids` (`SELECT ... WHERE id = ANY($1)` doesn't
    /// preserve caller order) so a `DataLoader` can zip it back against its keys positionally.
    /// An id that doesn't exist, or that record-level permission denies, is simply absent from
    /// the result — not an error — matching `Reference` field resolution's existing "dangling or
    /// unresolvable reference resolves to null" semantics elsewhere in this platform, rather than
    /// `get`'s single-record 404 (there is no one id for a partial-batch failure to be "the" 404
    /// about).
    pub async fn get_many(
        &self,
        entity_name: &str,
        ids: &[Uuid],
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<(Uuid, RecordDto, RecordCapabilities)>>> {
        let Some(entity) = self.get_entity(entity_name) else {
            tracing::debug!(entity = entity_name, "get_many rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_read_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        if ids.is_empty() {
            return Ok(ServiceResult::ok(Vec::new()));
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
        let existing = fetch_existing_batch(&mut *tx, ids, tenant_id, &entity).await?;
        tx.commit().await?;

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;

        let mut by_id: HashMap<Uuid, (RecordDto, RecordCapabilities)> = HashMap::with_capacity(existing.len());
        for record in existing {
            // `Delete` listed for the same reason as in `get.rs` — `compute_capabilities` below
            // evaluates a record-level delete condition, which needs the same relation fields
            // resolved as the other three actions.
            let enriched = self
                .enrich_record_for_actions(
                    &entity,
                    &snapshot,
                    &[
                        EntityAction::Read,
                        EntityAction::Update,
                        EntityAction::Transition,
                        EntityAction::Delete,
                    ],
                    tenant_id,
                    &record.data,
                )
                .await?;
            let record_decision = snapshot.can_perform_record_condition(context, &enriched, EntityAction::Read);
            if !record_decision.allowed {
                continue; // denied records are simply absent, see doc comment above
            }
            let capabilities = compute_capabilities(&entity, context, &snapshot, &enriched);
            let id = record.id;
            let masked = mask_record_for_read(&entity, context, &snapshot, record);
            by_id.insert(id, (masked, capabilities));
        }

        let ordered = ids
            .iter()
            .filter_map(|id| by_id.remove(id).map(|(dto, caps)| (*id, dto, caps)))
            .collect();

        Ok(ServiceResult::ok(ordered))
    }
}
