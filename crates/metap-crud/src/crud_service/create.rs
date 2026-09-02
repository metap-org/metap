use metap_permission::RequestContext;
use metap_workflow::{emit_created, get_initial_status};
use serde_json::Value;

use crate::dto::{JsonObject, RecordDto};
use crate::result::ServiceResult;
use crate::validation::validate_payload;

use super::helpers::{
    forbidden, forbidden_with_field, is_dedicated, mask_record_for_read, parse_user_id, recompute_fields,
    router_unavailable, row_to_dto, row_to_dto_dedicated, unique_violation, RECORD_COLUMNS, RECORD_COLUMNS_DEDICATED,
};
use super::CrudService;

impl CrudService {
    pub async fn create(
        &self,
        entity_name: &str,
        raw_data: &JsonObject,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
            tracing::debug!(entity = entity_name, "create rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_create_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;

        let keys: Vec<String> = raw_data.keys().cloned().collect();
        let write_decision = snapshot.assert_writable_fields(context, &keys, None);
        if !write_decision.allowed {
            return Ok(forbidden_with_field(write_decision));
        }

        let mut data = match validate_payload(&entity, raw_data) {
            Ok(d) => d,
            Err(field_errors) => {
                tracing::warn!(
                    entity = entity.name,
                    fields = ?field_errors.keys().collect::<Vec<_>>(),
                    "create rejected: validation failed"
                );
                return Ok(ServiceResult::err_with_field_errors(
                    400,
                    "validation_failed",
                    field_errors,
                ));
            }
        };

        recompute_fields(&entity, &mut data);

        let status = get_initial_status(&entity, &data);
        // TS's per-entity Zod schema commonly defaults the state field (e.g.
        // `status: z.enum([...]).default("draft")`), so `data` already contains it by the
        // time `getInitialStatus` runs there. This validator has no `.default()` equivalent
        // (see `validation.rs`'s doc comment), so the state field has to be written into
        // `data` explicitly here — otherwise the top-level `status` column and the `data`
        // blob disagree the moment a caller omits it, which `mask_record_for_read`'s
        // masking check (`filtered_data.contains_key(stateField)`) then reads as "absent".
        if let (Some(workflow), Some(status)) = (&entity.workflow, &status) {
            data.entry(workflow.state_field.clone())
                .or_insert_with(|| Value::String(status.clone()));
        }
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
        let insert_sql = if dedicated {
            format!(
                "INSERT INTO {table} (tenant_id, code, status, data, created_by, updated_by) \
                 VALUES ($1, $2, $3, $4, $5, $6) RETURNING {RECORD_COLUMNS_DEDICATED}"
            )
        } else {
            format!(
                "INSERT INTO {table} (tenant_id, entity, code, status, data, created_by, updated_by) \
                 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING {RECORD_COLUMNS}"
            )
        };
        let mut query = sqlx::query(&insert_sql).bind(tenant_id);
        if !dedicated {
            query = query.bind(&entity.name);
        }
        let row = match query
            .bind(&code)
            .bind(&status)
            .bind(Value::Object(data.clone()))
            .bind(user_id)
            .bind(user_id)
            .fetch_one(&mut *tx)
            .await
        {
            Ok(row) => row,
            Err(e) => {
                if let Some(result) = unique_violation(&entity.name, &e) {
                    tx.rollback().await.ok();
                    tracing::warn!(entity = entity.name, "create rejected: unique constraint violated");
                    return Ok(result);
                }
                return Err(e.into());
            }
        };
        let record = if dedicated {
            row_to_dto_dedicated(row, &entity.name)?
        } else {
            row_to_dto(row)?
        };

        emit_created(&mut *tx, &entity, tenant_id, record.id, &data).await?;
        tx.commit().await?;
        tracing::info!(entity = entity.name, record_id = %record.id, "record created");

        Ok(ServiceResult::ok(mask_record_for_read(
            &entity, context, &snapshot, record,
        )))
    }
}
