use metap_permission::{EntityAction, RequestContext};
use metap_workflow::emit_updated;
use serde_json::Value;
use uuid::Uuid;

use crate::dto::{JsonObject, RecordDto};
use crate::result::ServiceResult;
use crate::validation::validate_payload;

use super::helpers::{
    fetch_existing, forbidden, forbidden_with_field, is_dedicated, mask_record_for_read, parse_user_id,
    router_unavailable, row_to_dto, row_to_dto_dedicated, unique_violation, RECORD_COLUMNS, RECORD_COLUMNS_DEDICATED,
};
use super::CrudService;

impl CrudService {
    pub async fn update(
        &self,
        entity_name: &str,
        id: Uuid,
        expected_version: i32,
        raw_data: &JsonObject,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
            tracing::debug!(entity = entity_name, "update rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_update_entity(context, &entity.name).await?;
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
            tracing::debug!(entity = entity.name, record_id = %id, "update rejected: record not found");
            return Ok(ServiceResult::err(404, "record_not_found"));
        };
        precheck_tx.commit().await?;

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let enriched = self
            .enrich_record_for_actions(&entity, &snapshot, &[EntityAction::Update], tenant_id, &existing.data)
            .await?;
        let record_decision = snapshot.can_perform_record_condition(context, &enriched, EntityAction::Update);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        let keys: Vec<String> = raw_data.keys().cloned().collect();
        let write_decision = snapshot.assert_writable_fields(context, &keys, Some(&existing.data));
        if !write_decision.allowed {
            return Ok(forbidden_with_field(write_decision));
        }

        // The state field can never change through this path — only `create` and
        // `transition` are allowed to move it — so it's always reset to its existing value.
        let mut merged = existing.data.clone();
        for (k, v) in raw_data {
            merged.insert(k.clone(), v.clone());
        }
        if let Some(workflow) = &entity.workflow {
            if let Some(existing_state) = existing.data.get(&workflow.state_field) {
                merged.insert(workflow.state_field.clone(), existing_state.clone());
            }
        }

        let data = match validate_payload(&entity, &merged) {
            Ok(d) => d,
            Err(field_errors) => {
                tracing::warn!(
                    entity = entity.name,
                    record_id = %id,
                    fields = ?field_errors.keys().collect::<Vec<_>>(),
                    "update rejected: validation failed"
                );
                return Ok(ServiceResult::err_with_field_errors(
                    400,
                    "validation_failed",
                    field_errors,
                ));
            }
        };

        let code = data.get("code").and_then(Value::as_str).map(String::from);
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
        let dedicated = is_dedicated(&entity);
        let table = &entity.table_name;
        let update_sql = if dedicated {
            format!(
                "UPDATE {table} SET data = $1, code = $2, version = version + 1, updated_at = now(), updated_by = $3 \
                 WHERE id = $4 AND tenant_id = $5 AND version = $6 AND deleted = false \
                 RETURNING {RECORD_COLUMNS_DEDICATED}"
            )
        } else {
            format!(
                "UPDATE {table} SET data = $1, code = $2, version = version + 1, updated_at = now(), updated_by = $3 \
                 WHERE id = $4 AND tenant_id = $5 AND entity = $6 AND version = $7 AND deleted = false \
                 RETURNING {RECORD_COLUMNS}"
            )
        };
        let mut query = sqlx::query(&update_sql)
            .bind(Value::Object(data.clone()))
            .bind(&code)
            .bind(user_id)
            .bind(id)
            .bind(tenant_id);
        if !dedicated {
            query = query.bind(&entity.name);
        }
        let row = match query.bind(expected_version).fetch_optional(&mut *tx).await {
            Ok(row) => row,
            Err(e) => {
                if let Some(result) = unique_violation(&entity.name, &e) {
                    tx.rollback().await.ok();
                    tracing::warn!(entity = entity.name, record_id = %id, "update rejected: unique constraint violated");
                    return Ok(result);
                }
                return Err(e.into());
            }
        };

        let Some(row) = row else {
            tx.rollback().await.ok();
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                expected_version,
                "update rejected: version conflict"
            );
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = if dedicated {
            row_to_dto_dedicated(row, &entity.name)?
        } else {
            row_to_dto(row)?
        };

        emit_updated(&mut *tx, &entity, tenant_id, record.id, &data, record.version).await?;
        tx.commit().await?;
        tracing::info!(entity = entity.name, record_id = %record.id, version = record.version, "record updated");

        Ok(ServiceResult::ok(mask_record_for_read(
            &entity, context, &snapshot, record,
        )))
    }
}
