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

use arc_swap::ArcSwap;
use metap_control::{Router, RouterError};
use metap_metadata::{EntityDefinition, FieldKind, MetadataRegistry};
use metap_permission::{EntityAction, PermissionDecision, PermissionService, PermissionSnapshot, RequestContext};
use metap_query::{apply_params, encode_cursor, plan_list, Cursor, InvalidCursorError, ListInput, SortDir};
use metap_workflow::{
    emit_created, emit_deleted, emit_transitioned, emit_updated, find_transition, get_initial_status, record_event,
    run_guard,
};
use serde_json::Value;
use sqlx::{PgExecutor, Row};
use uuid::Uuid;

use crate::dto::{JsonObject, RecordCapabilities, RecordDto, TransitionAvailability};
use crate::result::{PageInfo, ServiceResult};
use crate::validation::validate_payload;

const RECORD_COLUMNS: &str = "id, entity, code, status, data, version, created_at, updated_at";

/// `metadata`/`permissions` are `Arc`, not owned — `crates/metap-http` (Migration Order step
/// 8) needs to share the same registry/permission service across route handlers (direct
/// `/metadata/*` routes, the auth extractor's role lookups) without cloning them, and
/// `Arc<T>: Clone + Send + Sync` is exactly what a multi-handler async server needs.
///
/// `metadata` is an `ArcSwap`, not a plain `Arc<MetadataRegistry>` (`docs/roadmap.md` Phase
/// 11 / Phase A sub-project 2) — a DB-authored entity publish/rollback swaps in a new merged
/// registry while the server keeps running, no restart. Every method here loads a snapshot
/// once, synchronously, near the top (via `get_entity`, or directly in `list`) and uses that
/// same snapshot for the rest of the call — a request is never torn between two registry
/// versions even if a publish happens mid-request.
pub struct CrudService {
    router: Router,
    metadata: Arc<ArcSwap<MetadataRegistry>>,
    permissions: Arc<PermissionService>,
}

impl CrudService {
    pub fn new(router: Router, metadata: Arc<ArcSwap<MetadataRegistry>>, permissions: Arc<PermissionService>) -> Self {
        Self {
            router,
            metadata,
            permissions,
        }
    }

    fn get_entity(&self, entity_name: &str) -> Option<EntityDefinition> {
        self.metadata.load().get_entity(entity_name).cloned()
    }

    /// Cross-record permission conditions (`docs/roadmap.md`'s permission-review findings,
    /// 2026-08-21, item #3): a record-level policy's condition may reference a related record
    /// via a dotted attribute path (e.g. `"project.ownerId"`), resolved one hop through a
    /// `FieldKind::Reference` field. Builds a *copy* of `record` with each such relation field's
    /// value replaced by the related record's own data (never mutates the caller's copy — other
    /// call-site logic, e.g. workflow guards or `writable_fields`, still needs the original
    /// reference-id value, not the expanded object) — used only as the subject passed into
    /// `PermissionSnapshot::can_perform_record_condition`.
    ///
    /// Only runs when `snapshot.required_relation_fields(action)` (for the union of `actions`)
    /// is non-empty, so an entity with no cross-record conditions pays zero extra query cost —
    /// this only ever runs for single-record operations (get/update/delete/transition), never
    /// `list()`, which has no way to resolve a relation inside a SQL `WHERE` clause (see
    /// `metap_query::condition_to_sql`'s doc comment on why that's rejected there instead of
    /// silently mismatching).
    async fn enrich_record_for_actions(
        &self,
        entity: &EntityDefinition,
        snapshot: &PermissionSnapshot,
        actions: &[EntityAction],
        tenant_id: Uuid,
        record: &JsonObject,
    ) -> anyhow::Result<JsonObject> {
        let mut relation_fields: Vec<String> = Vec::new();
        for &action in actions {
            for field in snapshot.required_relation_fields(action) {
                if !relation_fields.contains(&field) {
                    relation_fields.push(field);
                }
            }
        }
        if relation_fields.is_empty() {
            return Ok(record.clone());
        }

        let mut enriched = record.clone();
        let mut tx = self.router.begin(tenant_id.into()).await?;
        for field_name in relation_fields {
            let Some(field) = entity.fields.iter().find(|f| f.name == field_name) else {
                continue;
            };
            if field.kind != FieldKind::Reference {
                continue;
            }
            let Some(ref_entity) = &field.ref_entity else {
                continue;
            };
            let Some(ref_id) = record
                .get(&field_name)
                .and_then(Value::as_str)
                .and_then(|s| Uuid::parse_str(s).ok())
            else {
                continue;
            };
            if let Some(related_data) = fetch_related_data(&mut *tx, ref_id, tenant_id, ref_entity).await? {
                enriched.insert(field_name, Value::Object(related_data));
            }
        }
        tx.commit().await?;
        Ok(enriched)
    }

    pub async fn list(
        &self,
        entity_name: &str,
        input: &ListInput,
        context: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>> {
        // Loaded once, up front, and reused for both `entity` and `plan_list` below —
        // loading it twice (once via `get_entity`, again here) would let a publish/rollback
        // land in between and tear this request between two registry versions (see the
        // struct doc comment's "one snapshot per call" invariant).
        let metadata = self.metadata.load();
        let Some(entity) = metadata.get_entity(entity_name).cloned() else {
            tracing::debug!(entity = entity_name, "list rejected: entity not found");
            return Ok(ServiceResult::err(404, "entity_not_found"));
        };

        let decision = self.permissions.can_read_entity(context, &entity.name).await?;
        if !decision.allowed {
            return Ok(forbidden(decision));
        }

        let tenant_id = self.permissions.scoped_tenant(context)?;
        let snapshot = self.permissions.load_snapshot(tenant_id, &entity.name).await?;
        let record_policies = snapshot.get_record_policies(EntityAction::Read);

        let planned = match plan_list(
            &metadata,
            &self.permissions,
            &entity.name,
            input,
            context,
            record_policies,
        ) {
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
        let mut tx = match self.router.begin(tenant_id.into()).await {
            Ok(tx) => tx,
            Err(e) => {
                if let Some(result) = router_unavailable(&e) {
                    return Ok(result);
                }
                return Err(e);
            }
        };
        let query = apply_params(sqlx::query(&sql), &planned.params);
        let rows = query.fetch_all(&mut *tx).await?;
        tx.commit().await?;

        let has_more = rows.len() as i64 > planned.limit;
        let page_rows: Vec<_> = if has_more {
            rows.into_iter().take(planned.limit as usize).collect()
        } else {
            rows
        };
        let page_dtos: Vec<RecordDto> = page_rows.into_iter().map(row_to_dto).collect::<anyhow::Result<_>>()?;

        let next_cursor = if has_more {
            page_dtos.last().map(|last| {
                encode_cursor(&Cursor {
                    field: planned.resolved_sort.field.clone(),
                    value: sort_field_value(last, &planned.resolved_sort.field),
                    id: last.id.to_string(),
                    dir: if planned.resolved_sort.descending {
                        SortDir::Desc
                    } else {
                        SortDir::Asc
                    },
                })
            })
        } else {
            None
        };

        let data: Vec<RecordDto> = page_dtos
            .into_iter()
            .map(|dto| mask_record_for_read(&entity, context, &snapshot, dto))
            .collect();

        Ok(ServiceResult::ok_with_page(
            data,
            PageInfo {
                limit: planned.limit,
                next_cursor,
            },
        ))
    }

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
        let Some(existing) = fetch_existing(&mut *tx, id, tenant_id, &entity.name).await? else {
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
        let row = match sqlx::query(&format!(
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
        let record = row_to_dto(row)?;

        emit_created(&mut *tx, &entity, record.id, &data).await?;
        tx.commit().await?;
        tracing::info!(entity = entity.name, record_id = %record.id, "record created");

        Ok(ServiceResult::ok(mask_record_for_read(
            &entity, context, &snapshot, record,
        )))
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
        let Some(existing) = fetch_existing(&mut *precheck_tx, id, tenant_id, &entity.name).await? else {
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
        let row = match sqlx::query(&format!(
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
        .await
        {
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
        let record = row_to_dto(row)?;

        emit_updated(&mut *tx, &entity, record.id, &data, record.version).await?;
        tx.commit().await?;
        tracing::info!(entity = entity.name, record_id = %record.id, version = record.version, "record updated");

        Ok(ServiceResult::ok(mask_record_for_read(
            &entity, context, &snapshot, record,
        )))
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
        let Some(existing) = fetch_existing(&mut *precheck_tx, id, tenant_id, &entity.name).await? else {
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

        let mut next_data = existing.data.clone();
        next_data.insert(workflow.state_field.clone(), Value::String(to_state.clone()));

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
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                expected_version,
                "transition rejected: version conflict"
            );
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = row_to_dto(row)?;

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
        let Some(existing) = fetch_existing(&mut *precheck_tx, id, tenant_id, &entity.name).await? else {
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
        // before it, so this stays consistent with whatever the delete itself observes.
        let metadata = self.metadata.load();
        for (ref_entity, ref_field) in referencing_fields(&metadata, &entity.name) {
            let referenced: Option<i32> = sqlx::query_scalar(
                "SELECT 1 FROM records \
                 WHERE tenant_id = $1 AND entity = $2 AND deleted = false AND data ->> $3 = $4 LIMIT 1",
            )
            .bind(tenant_id)
            .bind(&ref_entity)
            .bind(&ref_field)
            .bind(id.to_string())
            .fetch_optional(&mut *tx)
            .await?;
            if referenced.is_some() {
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
        }

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
            tracing::warn!(
                entity = entity.name,
                record_id = %id,
                expected_version,
                "delete rejected: version conflict"
            );
            return Ok(ServiceResult::err(409, "version_conflict"));
        };
        let record = row_to_dto(row)?;

        emit_deleted(&mut *tx, &entity, record.id).await?;
        tx.commit().await?;
        tracing::info!(entity = entity.name, record_id = %record.id, "record deleted");

        Ok(ServiceResult::ok(mask_record_for_read(
            &entity, context, &snapshot, record,
        )))
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
        Some(field) => {
            ServiceResult::err_with_field_errors(403, reason, HashMap::from([(field, vec!["forbidden".to_string()])]))
        }
        None => ServiceResult::err(403, reason),
    }
}

/// A DB unique-index violation on `records` — `EntityField.unique: true` (`docs/roadmap.md`
/// Phase 11 field builder flags) is enforced purely as a Postgres unique index
/// (`crates/metap-peripherals/src/index_reconciler.rs::ensure_index`, named
/// `uniq_records_<entity, dots as underscores>_<field>`), not pre-checked here — so a
/// racing/duplicate write must be caught after the fact, at the `INSERT`/`UPDATE` call site,
/// rather than surfacing as an unhandled 500 (`?` on the query result would otherwise convert
/// straight to `anyhow::Error`, mirrors the same catch `routes/admin.rs::create_user` does for
/// `email_taken`, just generalized to any entity's `unique` field). Returns `None` for any
/// other database error, so the caller's `Err(e) => return Err(e.into())` fallback still
/// applies.
fn unique_violation<T>(entity_name: &str, error: &sqlx::Error) -> Option<ServiceResult<T>> {
    let sqlx::Error::Database(db_err) = error else {
        return None;
    };
    if !db_err.is_unique_violation() {
        return None;
    }
    let prefix = format!("uniq_records_{}_", entity_name.replace('.', "_"));
    let field = db_err
        .constraint()
        .and_then(|c| c.strip_prefix(&prefix))
        .map(str::to_string);
    Some(match field {
        Some(field) => ServiceResult::err_with_field_errors(
            409,
            "unique_violation",
            HashMap::from([(field, vec!["A record with this value already exists.".to_string()])]),
        ),
        None => ServiceResult::err(409, "unique_violation"),
    })
}

/// `Router::begin` fails with `metap_control::RouterError` for tenant states that are a normal,
/// expected part of the tenant lifecycle (suspended for non-payment, mid-migration, still
/// provisioning, trial expired) rather than a bug — those get a clean 4xx/5xx instead of falling
/// through to the generic `?` -> 500 path. Any other error (DB connectivity, an
/// `InvalidSchemaName` that should never occur from real `control.tenants` data) returns `None`
/// so the caller's `return Err(e)` fallback still applies — same shape as `unique_violation`
/// above.
fn router_unavailable<T>(error: &anyhow::Error) -> Option<ServiceResult<T>> {
    match error.downcast_ref::<RouterError>()? {
        RouterError::TenantSuspended | RouterError::TenantExpired => {
            Some(ServiceResult::err(403, "tenant_unavailable"))
        }
        RouterError::TenantMigrating | RouterError::TenantProvisioning => {
            Some(ServiceResult::err(503, "tenant_unavailable"))
        }
        RouterError::TenantDeleted => Some(ServiceResult::err(404, "tenant_not_found")),
        RouterError::InvalidSchemaName(_) => None,
    }
}

/// Every `(entity, field)` pair across the whole registry where `field` is a `Reference` kind
/// pointing at `target_entity` — the set `delete()` checks for orphan references. Includes
/// self-references (an entity referencing itself, e.g. a manager hierarchy) — deleting a record
/// other records of the *same* entity still point to is exactly the same orphan-reference risk.
fn referencing_fields(metadata: &MetadataRegistry, target_entity: &str) -> Vec<(String, String)> {
    let mut result = Vec::new();
    for summary in metadata.list_entities() {
        for field in &summary.fields {
            if field.kind == FieldKind::Reference && field.ref_entity.as_deref() == Some(target_entity) {
                result.push((summary.name.clone(), field.name.clone()));
            }
        }
    }
    result
}

async fn fetch_existing<'e, E: PgExecutor<'e>>(
    executor: E,
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
    .fetch_optional(executor)
    .await?;
    row.map(row_to_dto).transpose()
}

/// Raw `data` fetch for one hop of cross-record permission enrichment (see
/// `CrudService::enrich_record_for_actions`) — deliberately not `fetch_existing` (no need for
/// the full `RecordDto`/`RECORD_COLUMNS` shape, just the JSONB blob to merge into a subject)
/// and deliberately no permission check on the related record: this never leaves the server as
/// a response, it's only ever fed into `PolicyCondition` evaluation for the *current* record.
async fn fetch_related_data<'e, E: PgExecutor<'e>>(
    executor: E,
    id: Uuid,
    tenant_id: Uuid,
    entity_name: &str,
) -> anyhow::Result<Option<JsonObject>> {
    let row =
        sqlx::query("SELECT data FROM records WHERE id = $1 AND tenant_id = $2 AND entity = $3 AND deleted = false")
            .bind(id)
            .bind(tenant_id)
            .bind(entity_name)
            .fetch_optional(executor)
            .await?;
    let Some(row) = row else {
        return Ok(None);
    };
    let data_value: Value = row.try_get("data")?;
    Ok(data_value.as_object().cloned())
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
    let code = if filtered_data.contains_key("code") {
        row.code
    } else {
        None
    };
    let status = match state_field {
        Some(sf) if !filtered_data.contains_key(sf) => None,
        _ => row.status,
    };
    RecordDto {
        code,
        status,
        data: filtered_data,
        ..row
    }
}

fn compute_capabilities(
    entity: &EntityDefinition,
    context: &RequestContext,
    snapshot: &PermissionSnapshot,
    existing_data: &JsonObject,
) -> RecordCapabilities {
    let all_field_names: Vec<String> = entity.fields.iter().map(|f| f.name.clone()).collect();
    let writable_fields = snapshot.writable_fields(context, &all_field_names, Some(existing_data));

    let record_decision = snapshot.can_perform_record_condition(context, existing_data, EntityAction::Update);
    let can_update = record_decision.allowed;
    // Separate from `can_update` (`docs/roadmap.md`'s permission-review findings, 2026-08-21):
    // "can edit fields" and "can change state" are now two different policy-gated actions, so
    // a caller who can update fields but not transition (or vice versa) sees the right
    // capability hint instead of one standing in for the other.
    let transition_decision = snapshot.can_perform_record_condition(context, existing_data, EntityAction::Transition);

    let mut transitions = Vec::new();
    let current_state = entity
        .workflow
        .as_ref()
        .and_then(|w| existing_data.get(&w.state_field))
        .and_then(Value::as_str);

    if let (Some(workflow), Some(current_state)) = (&entity.workflow, current_state) {
        for transition in &workflow.transitions {
            if transition.from != current_state {
                continue;
            }

            if !transition_decision.allowed {
                transitions.push(TransitionAvailability {
                    action: transition.action.clone(),
                    available: false,
                    reason: transition_decision.reason.clone(),
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

    RecordCapabilities {
        writable_fields,
        can_update,
        transitions,
    }
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
