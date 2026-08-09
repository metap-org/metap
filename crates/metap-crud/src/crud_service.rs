//! Mirrors `packages/core/src/core/crud/crud-service.ts`. No injected `QueryPlanner`/
//! `WorkflowEngine`/`OutboxService` — those are the free-function modules built in
//! Migration Order steps 5/6 (`metap_query::plan_list`, `metap_workflow::*`), called
//! directly instead of held as constructor dependencies that wrap nothing.
//!
//! `entity` is fetched as an owned, cloned `EntityDefinition` at the top of every method
//! rather than borrowed from `self.metadata` — a deliberate simplicity choice (entities are
//! small; this sidesteps any borrow-across-`.await` friction) that can be revisited if
//! profiling ever shows it matters, not a performance decision made ahead of evidence.

use std::collections::HashMap;
use std::sync::Arc;

use metap_metadata::{EntityDefinition, MetadataRegistry};
use metap_permission::{EntityAction, PermissionDecision, PermissionService, PermissionSnapshot, RequestContext};
use metap_query::{apply_params, encode_cursor, plan_list, Cursor, InvalidCursorError, ListInput, SortDir};
use metap_workflow::{emit_created, emit_deleted, emit_transitioned, emit_updated, find_transition, get_initial_status, record_event, run_guard};
use serde_json::Value;
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::dto::{JsonObject, RecordCapabilities, RecordDto, TransitionAvailability};
use crate::result::{PageInfo, ServiceResult};
use crate::validation::validate_payload;

const RECORD_COLUMNS: &str = "id, entity, code, status, data, version, created_at, updated_at";

/// `metadata`/`permissions` are `Arc`, not owned — `crates/metap-http` (Migration Order step
/// 8) needs to share the same registry/permission service across route handlers (direct
/// `/metadata/*` routes, the auth extractor's role lookups) without cloning them, and
/// `Arc<T>: Clone + Send + Sync` is exactly what a multi-handler async server needs.
pub struct CrudService {
    pool: PgPool,
    metadata: Arc<MetadataRegistry>,
    permissions: Arc<PermissionService>,
}

impl CrudService {
    pub fn new(pool: PgPool, metadata: Arc<MetadataRegistry>, permissions: Arc<PermissionService>) -> Self {
        Self { pool, metadata, permissions }
    }

    fn get_entity(&self, entity_name: &str) -> Option<EntityDefinition> {
        self.metadata.get_entity(entity_name).cloned()
    }

    pub async fn list(
        &self,
        entity_name: &str,
        input: &ListInput,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>> {
        let Some(entity) = self.get_entity(entity_name) else {
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_read_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_policies = snapshot.get_record_policies(EntityAction::Read);

        let planned = match plan_list(&self.metadata, &self.permissions, &entity.name, input, context, record_policies) {
            Ok(p) => p,
            Err(e) => {
                if e.downcast_ref::<InvalidCursorError>().is_some() {
                    return Ok(ServiceResult::err_with_message(400, "invalid_cursor", e.to_string()));
                }
                return Err(e);
            }
        };

        let sql = format!(
            "SELECT {RECORD_COLUMNS} FROM records WHERE {} ORDER BY {} LIMIT {}",
            planned.where_sql,
            planned.order_by_sql,
            planned.limit + 1
        );
        let query = apply_params(sqlx::query(&sql), &planned.params);
        let rows = query.fetch_all(&self.pool).await?;

        let has_more = rows.len() as i64 > planned.limit;
        let page_rows: Vec<_> =
            if has_more { rows.into_iter().take(planned.limit as usize).collect() } else { rows };
        let page_dtos: Vec<RecordDto> =
            page_rows.into_iter().map(row_to_dto).collect::<anyhow::Result<_>>()?;

        let next_cursor = if has_more {
            page_dtos.last().map(|last| {
                encode_cursor(&Cursor {
                    field: planned.resolved_sort.field.clone(),
                    value: sort_field_value(last, &planned.resolved_sort.field),
                    id: last.id.to_string(),
                    dir: if planned.resolved_sort.descending { SortDir::Desc } else { SortDir::Asc },
                })
            })
        } else {
            None
        };

        let data: Vec<RecordDto> = page_dtos
            .into_iter()
            .map(|dto| mask_record_for_read(&entity, context, &snapshot, dto))
            .collect();

        Ok(ServiceResult::ok_with_page(data, PageInfo { limit: planned.limit, next_cursor }))
    }

    pub async fn get(
        &self,
        entity_name: &str,
        id: Uuid,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>> {
        let Some(entity) = self.get_entity(entity_name) else {
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_read_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let Some(existing) = fetch_existing(&self.pool, id, tenant_id, &entity.name).await? else {
            return Ok(ServiceResult::err(404, "record_not_found"));
        };

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_decision =
            snapshot.can_update_record_condition(context, &existing.data, EntityAction::Read);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        let capabilities = compute_capabilities(&entity, context, &snapshot, &existing.data);
        let masked = mask_record_for_read(&entity, context, &snapshot, existing);
        Ok(ServiceResult::ok((masked, capabilities)))
    }

    pub async fn create(
        &self,
        entity_name: &str,
        raw_data: &JsonObject,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
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
                return Ok(ServiceResult::err_with_field_errors(400, "validation_failed", field_errors))
            }
        };

        let status = get_initial_status(&entity, &data);
        // TS's per-entity Zod schema commonly defaults the state field (e.g.
        // `status: z.enum([...]).default("draft")`), so `data` already contains it by the
        // time `getInitialStatus` runs there. This validator has no `.default()` equivalent
        // (see `validation.rs`'s doc comment), so the state field has to be written into
        // `data` explicitly here — otherwise the top-level `status` column and the `data`
        // blob disagree the moment a caller omits it, which `mask_record_for_read`'s
        // masking check (`filtered_data.contains_key(stateField)`) then reads as "absent".
        if let (Some(workflow), Some(status)) = (&entity.workflow, &status) {
            data.entry(workflow.state_field.clone()).or_insert_with(|| Value::String(status.clone()));
        }
        let code = data.get("code").and_then(Value::as_str).map(String::from);
        let user_id = parse_user_id(context)?;

        let mut tx = self.pool.begin().await?;
        let row = sqlx::query(&format!(
            "INSERT INTO records (tenant_id, entity, code, status, data, created_by, updated_by) \
             VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING {RECORD_COLUMNS}"
        ))
        .bind(tenant_id)
        .bind(&entity.name)
        .bind(&code)
        .bind(&status)
        .bind(Value::Object(data.clone()))
        .bind(user_id)
        .bind(user_id)
        .fetch_one(&mut *tx)
        .await?;
        let record = row_to_dto(row)?;

        emit_created(&mut *tx, &entity, record.id, &data).await?;
        tx.commit().await?;

        Ok(ServiceResult::ok(mask_record_for_read(&entity, context, &snapshot, record)))
    }

    pub async fn update(
        &self,
        entity_name: &str,
        id: Uuid,
        expected_version: i32,
        raw_data: &JsonObject,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_update_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let Some(existing) = fetch_existing(&self.pool, id, tenant_id, &entity.name).await? else {
            return Ok(ServiceResult::err(404, "record_not_found"));
        };

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_decision =
            snapshot.can_update_record_condition(context, &existing.data, EntityAction::Update);
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
                return Ok(ServiceResult::err_with_field_errors(400, "validation_failed", field_errors))
            }
        };

        let code = data.get("code").and_then(Value::as_str).map(String::from);
        let user_id = parse_user_id(context)?;

        let mut tx = self.pool.begin().await?;
        let row = sqlx::query(&format!(
            "UPDATE records SET data = $1, code = $2, version = version + 1, updated_at = now(), updated_by = $3 \
             WHERE id = $4 AND tenant_id = $5 AND entity = $6 AND version = $7 AND deleted = false \
             RETURNING {RECORD_COLUMNS}"
        ))
        .bind(Value::Object(data.clone()))
        .bind(&code)
        .bind(user_id)
        .bind(id)
        .bind(tenant_id)
        .bind(&entity.name)
        .bind(expected_version)
        .fetch_optional(&mut *tx)
        .await?;

        let Some(row) = row else {
            tx.rollback().await.ok();
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = row_to_dto(row)?;

        emit_updated(&mut *tx, &entity, record.id, &data, record.version).await?;
        tx.commit().await?;

        Ok(ServiceResult::ok(mask_record_for_read(&entity, context, &snapshot, record)))
    }

    pub async fn transition(
        &self,
        entity_name: &str,
        id: Uuid,
        action: &str,
        expected_version: i32,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_update_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let Some(existing) = fetch_existing(&self.pool, id, tenant_id, &entity.name).await? else {
            return Ok(ServiceResult::err(404, "record_not_found"));
        };

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_decision =
            snapshot.can_update_record_condition(context, &existing.data, EntityAction::Update);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        let Some(workflow) = &entity.workflow else {
            return Ok(ServiceResult::err(400, "no_workflow"));
        };

        let Some(from_state) = existing.data.get(&workflow.state_field).and_then(Value::as_str) else {
            return Ok(ServiceResult::err(409, "invalid_transition"));
        };
        let from_state = from_state.to_string();

        let Some(transition) = find_transition(&entity, action, &from_state) else {
            return Ok(ServiceResult::err(409, "invalid_transition"));
        };

        if let Err(reason) = run_guard(transition, &existing.data, context) {
            return Ok(ServiceResult::err_with_message(422, "guard_failed", reason));
        }
        let to_state = transition.to.clone();

        let mut next_data = existing.data.clone();
        next_data.insert(workflow.state_field.clone(), Value::String(to_state.clone()));

        let user_id = parse_user_id(context)?;

        let mut tx = self.pool.begin().await?;
        let row = sqlx::query(&format!(
            "UPDATE records SET data = $1, status = $2, version = version + 1, updated_at = now(), updated_by = $3 \
             WHERE id = $4 AND tenant_id = $5 AND entity = $6 AND version = $7 AND deleted = false \
             RETURNING {RECORD_COLUMNS}"
        ))
        .bind(Value::Object(next_data))
        .bind(&to_state)
        .bind(user_id)
        .bind(id)
        .bind(tenant_id)
        .bind(&entity.name)
        .bind(expected_version)
        .fetch_optional(&mut *tx)
        .await?;

        let Some(row) = row else {
            tx.rollback().await.ok();
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = row_to_dto(row)?;

        record_event(&mut *tx, &entity, record.id, action, &from_state, &to_state, context).await?;
        emit_transitioned(&mut *tx, &entity, record.id, action, &from_state, &to_state, user_id).await?;
        tx.commit().await?;

        Ok(ServiceResult::ok(mask_record_for_read(&entity, context, &snapshot, record)))
    }

    pub async fn delete(
        &self,
        entity_name: &str,
        id: Uuid,
        expected_version: i32,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        let Some(entity) = self.get_entity(entity_name) else {
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_delete_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let Some(existing) = fetch_existing(&self.pool, id, tenant_id, &entity.name).await? else {
            return Ok(ServiceResult::err(404, "record_not_found"));
        };

        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_decision =
            snapshot.can_update_record_condition(context, &existing.data, EntityAction::Delete);
        if !record_decision.allowed {
            return Ok(forbidden(record_decision));
        }

        let user_id = parse_user_id(context)?;

        let mut tx = self.pool.begin().await?;
        let row = sqlx::query(&format!(
            "UPDATE records SET deleted = true, version = version + 1, updated_at = now(), updated_by = $1 \
             WHERE id = $2 AND tenant_id = $3 AND entity = $4 AND version = $5 AND deleted = false \
             RETURNING {RECORD_COLUMNS}"
        ))
        .bind(user_id)
        .bind(id)
        .bind(tenant_id)
        .bind(&entity.name)
        .bind(expected_version)
        .fetch_optional(&mut *tx)
        .await?;

        let Some(row) = row else {
            tx.rollback().await.ok();
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = row_to_dto(row)?;

        emit_deleted(&mut *tx, &entity, record.id).await?;
        tx.commit().await?;

        Ok(ServiceResult::ok(mask_record_for_read(&entity, context, &snapshot, record)))
    }
}

fn parse_user_id(context: &RequestContext) -> anyhow::Result<Option<Uuid>> {
    Ok(context.user_id.as_deref().map(Uuid::parse_str).transpose()?)
}

fn forbidden<T>(decision: PermissionDecision) -> ServiceResult<T> {
    ServiceResult::err(403, decision.reason.unwrap_or_else(|| "forbidden".to_string()))
}

fn forbidden_with_field<T>(decision: PermissionDecision) -> ServiceResult<T> {
    let reason = decision.reason.clone().unwrap_or_else(|| "forbidden".to_string());
    match decision.field {
        Some(field) => ServiceResult::err_with_field_errors(
            403,
            reason,
            HashMap::from([(field, vec!["forbidden".to_string()])]),
        ),
        None => ServiceResult::err(403, reason),
    }
}

async fn fetch_existing(
    pool: &PgPool,
    id: Uuid,
    tenant_id: Uuid,
    entity_name: &str,
) -> anyhow::Result<Option<RecordDto>> {
    let row = sqlx::query(&format!(
        "SELECT {RECORD_COLUMNS} FROM records \
         WHERE id = $1 AND tenant_id = $2 AND entity = $3 AND deleted = false"
    ))
    .bind(id)
    .bind(tenant_id)
    .bind(entity_name)
    .fetch_optional(pool)
    .await?;
    row.map(row_to_dto).transpose()
}

fn row_to_dto(row: sqlx::postgres::PgRow) -> anyhow::Result<RecordDto> {
    let data_value: Value = row.try_get("data")?;
    let data = data_value
        .as_object()
        .cloned()
        .ok_or_else(|| anyhow::anyhow!("records.data was not a JSON object"))?;
    Ok(RecordDto {
        id: row.try_get("id")?,
        entity: row.try_get("entity")?,
        code: row.try_get("code")?,
        status: row.try_get("status")?,
        data,
        version: row.try_get("version")?,
        created_at: row.try_get("created_at")?,
        updated_at: row.try_get("updated_at")?,
    })
}

/// `records.code`/`records.status` are physical columns that mirror
/// `data.code`/`data[entity.workflow.stateField]` purely for indexing —
/// `filter_readable_fields` only masks the `data` blob, so this masks the mirrored
/// top-level columns the same way or a denied field's value still leaks through them.
fn mask_record_for_read(
    entity: &EntityDefinition,
    context: &RequestContext,
    snapshot: &PermissionSnapshot,
    row: RecordDto,
) -> RecordDto {
    let filtered_data = snapshot.filter_readable_fields(context, &row.data);
    let state_field = entity.workflow.as_ref().map(|w| w.state_field.as_str());
    let code = if filtered_data.contains_key("code") { row.code } else { None };
    let status = match state_field {
        Some(sf) if !filtered_data.contains_key(sf) => None,
        _ => row.status,
    };
    RecordDto { code, status, data: filtered_data, ..row }
}

fn compute_capabilities(
    entity: &EntityDefinition,
    context: &RequestContext,
    snapshot: &PermissionSnapshot,
    existing_data: &JsonObject,
) -> RecordCapabilities {
    let all_field_names: Vec<String> = entity.fields.iter().map(|f| f.name.clone()).collect();
    let writable_fields = snapshot.writable_fields(context, &all_field_names, Some(existing_data));

    let record_decision =
        snapshot.can_update_record_condition(context, existing_data, EntityAction::Update);
    let can_update = record_decision.allowed;

    let mut transitions = Vec::new();
    let current_state =
        entity.workflow.as_ref().and_then(|w| existing_data.get(&w.state_field)).and_then(Value::as_str);

    if let (Some(workflow), Some(current_state)) = (&entity.workflow, current_state) {
        for transition in &workflow.transitions {
            if transition.from != current_state {
                continue;
            }

            if !can_update {
                transitions.push(TransitionAvailability {
                    action: transition.action.clone(),
                    available: false,
                    reason: record_decision.reason.clone(),
                });
                continue;
            }

            let guard_result = run_guard(transition, existing_data, context);
            transitions.push(TransitionAvailability {
                action: transition.action.clone(),
                available: guard_result.is_ok(),
                reason: guard_result.err(),
            });
        }
    }

    RecordCapabilities { writable_fields, can_update, transitions }
}

fn sort_field_value(row: &RecordDto, field: &str) -> String {
    match field {
        "createdAt" => row.created_at.to_rfc3339(),
        "updatedAt" => row.updated_at.to_rfc3339(),
        _ => match row.data.get(field) {
            Some(Value::String(s)) => s.clone(),
            Some(Value::Number(n)) => n.to_string(),
            Some(Value::Bool(b)) => b.to_string(),
            Some(v) if !v.is_null() => v.to_string(),
            _ => String::new(),
        },
    }
}
