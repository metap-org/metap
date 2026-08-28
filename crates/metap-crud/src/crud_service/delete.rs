use metap_permission::{EntityAction, RequestContext};
use metap_workflow::emit_deleted;
use uuid::Uuid;

use crate::dto::RecordDto;
use crate::result::ServiceResult;

use super::helpers::{
    fetch_existing, find_referencing_record, forbidden, is_dedicated, mask_record_for_read, parse_user_id,
    referencing_fields, router_unavailable, row_to_dto, row_to_dto_dedicated, RECORD_COLUMNS, RECORD_COLUMNS_DEDICATED,
};
use super::CrudService;

impl CrudService {
    pub async fn delete(
        &self,
        entity_name: &str,
        id: Uuid,
        expected_version: i32,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
            tracing::debug!(entity = entity_name, "delete rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_delete_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let mut precheck_tx = match self.router.begin(tenant_id.into()).await {
            Ok(tx) => tx,
            Err(e) => {
                if let Some(result) = router_unavailable(&e) {
                    return Ok(result);
                }
                return Err(e);
            }
        };
        let Some(existing) = fetch_existing(&mut *precheck_tx, id, tenant_id, &entity).await? else {
            tracing::debug!(entity = entity.name, record_id = %id, "delete rejected: record not found");
            return Ok(ServiceResult::err(404, "record_not_found"));
        };
        precheck_tx.commit().await?;

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let enriched = self
            .enrich_record_for_actions(&entity, &snapshot, &[EntityAction::Delete], tenant_id, &existing.data)
            .await?;
        let record_decision = snapshot.can_perform_record_condition(context, &enriched, EntityAction::Delete);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        let user_id = parse_user_id(context)?;

        let mut tx = match self.router.begin(tenant_id.into()).await {
            Ok(tx) => tx,
            Err(e) => {
                if let Some(result) = router_unavailable(&e) {
                    return Ok(result);
                }
                return Err(e);
            }
        };

        // Reference-integrity guard (`docs/architectures/11-risks.md`): deleting a record that
        // another record still points to via a `Reference` field would otherwise leave a
        // silent orphan reference — no error, no cascade, nothing. Every `Reference` field
        // across the registry that targets this entity is Restrict-by-default (no per-field
        // override yet); checked inside the same transaction as the delete itself, right
        // before it, so this stays consistent with whatever the delete itself observes. One
        // combined query for every referencing `(entity, field)` pair, not one query per pair
        // (found in code review, 2026-08-22 — an entity referenced by K fields used to cost K
        // sequential round trips on every delete).
        let metadata = self.metadata.load();
        let refs = referencing_fields(&metadata, &entity.name);
        if let Some((ref_entity, ref_field)) = find_referencing_record(&mut tx, tenant_id, id, &refs).await? {
            tx.rollback().await.ok();
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                referencing_entity = ref_entity,
                referencing_field = ref_field,
                "delete rejected: record is still referenced by another record"
            );
            return Ok(ServiceResult::err_with_message(
                409,
                "record_referenced",
                format!("This record is referenced by \"{ref_field}\" on \"{ref_entity}\" and cannot be deleted."),
            ));
        }

        let dedicated = is_dedicated(&entity);
        let table = &entity.table_name;
        let delete_sql = if dedicated {
            format!(
                "UPDATE {table} SET deleted = true, version = version + 1, updated_at = now(), updated_by = $1 \
                 WHERE id = $2 AND tenant_id = $3 AND version = $4 AND deleted = false \
                 RETURNING {RECORD_COLUMNS_DEDICATED}"
            )
        } else {
            format!(
                "UPDATE {table} SET deleted = true, version = version + 1, updated_at = now(), updated_by = $1 \
                 WHERE id = $2 AND tenant_id = $3 AND entity = $4 AND version = $5 AND deleted = false \
                 RETURNING {RECORD_COLUMNS}"
            )
        };
        let mut query = sqlx::query(&delete_sql).bind(user_id).bind(id).bind(tenant_id);
        if !dedicated {
            query = query.bind(&entity.name);
        }
        let row = query.bind(expected_version).fetch_optional(&mut *tx).await?;

        let Some(row) = row else {
            tx.rollback().await.ok();
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                expected_version,
                "delete rejected: version conflict"
            );
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = if dedicated {
            row_to_dto_dedicated(row, &entity.name)?
        } else {
            row_to_dto(row)?
        };

        emit_deleted(&mut *tx, &entity, tenant_id, record.id).await?;
        tx.commit().await?;
        tracing::info!(entity = entity.name, record_id = %record.id, "record deleted");

        Ok(ServiceResult::ok(mask_record_for_read(
            &entity, context, &snapshot, record,
        )))
    }
}
