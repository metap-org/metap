use metap_permission::{EntityAction, RequestContext};
use metap_workflow::{apply_set_fields, emit_transitioned, find_transition, record_event, run_guard, run_validator};
use serde_json::Value;
use uuid::Uuid;

use crate::dto::{JsonObject, RecordDto};
use crate::result::ServiceResult;
use crate::validation::validate_payload;

use super::helpers::{
    fetch_existing, forbidden, forbidden_with_field, is_dedicated, mask_record_for_read, parse_user_id,
    router_unavailable, row_to_dto, row_to_dto_dedicated, RECORD_COLUMNS, RECORD_COLUMNS_DEDICATED,
};
use super::CrudService;

impl CrudService {
    pub async fn transition(
        &self,
        entity_name: &str,
        id: Uuid,
        action: &str,
        expected_version: i32,
        payload: Option<&JsonObject>,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
            tracing::debug!(entity = entity_name, "transition rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_transition_entity(context, &entity.name).await?;
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
            tracing::debug!(entity = entity.name, record_id = %id, "transition rejected: record not found");
            return Ok(ServiceResult::err(404, "record_not_found"));
        };
        precheck_tx.commit().await?;

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let enriched = self
            .enrich_record_for_actions(
                &entity,
                &snapshot,
                &[EntityAction::Transition],
                tenant_id,
                &existing.data,
            )
            .await?;
        let record_decision = snapshot.can_perform_record_condition(context, &enriched, EntityAction::Transition);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        let Some(workflow) = &entity.workflow else {
            tracing::warn!(entity = entity.name, record_id = %id, "transition rejected: entity has no workflow");
            return Ok(ServiceResult::err(400, "no_workflow"));
        };

        let Some(from_state) = existing.data.get(&workflow.state_field).and_then(Value::as_str) else {
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                state_field = workflow.state_field,
                "transition rejected: record has no value for the workflow state field"
            );
            return Ok(ServiceResult::err(409, "invalid_transition"));
        };
        let from_state = from_state.to_string();

        let Some(transition) = find_transition(&entity, action, &from_state) else {
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                action,
                from_state,
                "transition rejected: no transition defined for this action/from-state pair"
            );
            return Ok(ServiceResult::err(409, "invalid_transition"));
        };

        if let Err(reason) = run_guard(transition, &existing.data, context) {
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                action,
                from_state,
                reason,
                "transition rejected: guard failed"
            );
            return Ok(ServiceResult::err_with_message(422, "guard_failed", reason));
        }
        let to_state = transition.to.clone();

        // A transition's payload goes through the same writable-fields check and
        // field-metadata validation as `update()` — the state field itself is always driven
        // by `to`, never by the caller, even if the payload tries to include it.
        let mut merged = existing.data.clone();
        if let Some(payload) = payload {
            let keys: Vec<String> = payload.keys().cloned().collect();
            let write_decision = snapshot.assert_writable_fields(context, &keys, Some(&existing.data));
            if !write_decision.allowed {
                return Ok(forbidden_with_field(write_decision));
            }
            for (k, v) in payload {
                merged.insert(k.clone(), v.clone());
            }
        }
        merged.insert(workflow.state_field.clone(), Value::String(to_state.clone()));

        let mut next_data = match validate_payload(&entity, &merged) {
            Ok(d) => d,
            Err(field_errors) => {
                tracing::warn!(
                    entity = entity.name,
                    record_id = %id,
                    fields = ?field_errors.keys().collect::<Vec<_>>(),
                    "transition rejected: validation failed"
                );
                return Ok(ServiceResult::err_with_field_errors(
                    400,
                    "validation_failed",
                    field_errors,
                ));
            }
        };

        if let Err(reason) = run_validator(transition, &next_data, context) {
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                action,
                from_state,
                reason,
                "transition rejected: validator failed"
            );
            return Ok(ServiceResult::err_with_message(422, "validator_failed", reason));
        }

        apply_set_fields(transition, &mut next_data, context);

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
        let transition_sql = if dedicated {
            format!(
                "UPDATE {table} SET data = $1, status = $2, version = version + 1, updated_at = now(), updated_by = $3 \
                 WHERE id = $4 AND tenant_id = $5 AND version = $6 AND deleted = false \
                 RETURNING {RECORD_COLUMNS_DEDICATED}"
            )
        } else {
            format!(
                "UPDATE {table} SET data = $1, status = $2, version = version + 1, updated_at = now(), updated_by = $3 \
                 WHERE id = $4 AND tenant_id = $5 AND entity = $6 AND version = $7 AND deleted = false \
                 RETURNING {RECORD_COLUMNS}"
            )
        };
        let mut query = sqlx::query(&transition_sql)
            .bind(Value::Object(next_data))
            .bind(&to_state)
            .bind(user_id)
            .bind(id)
            .bind(tenant_id);
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
                "transition rejected: version conflict"
            );
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = if dedicated {
            row_to_dto_dedicated(row, &entity.name)?
        } else {
            row_to_dto(row)?
        };

        record_event(&mut *tx, &entity, record.id, action, &from_state, &to_state, context).await?;
        emit_transitioned(
            &mut *tx,
            &entity,
            tenant_id,
            record.id,
            action,
            &from_state,
            &to_state,
            user_id,
        )
        .await?;
        tx.commit().await?;
        tracing::info!(
            entity = entity.name,
            record_id = %record.id,
            action,
            from = from_state,
            to = to_state,
            "record transitioned"
        );

        Ok(ServiceResult::ok(mask_record_for_read(
            &entity, context, &snapshot, record,
        )))
    }
}
