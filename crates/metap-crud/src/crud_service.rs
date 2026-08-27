//! Mirrors `packages/core/src/core/crud/crud-service.ts`. No injected `QueryPlanner`/
//! `WorkflowEngine`/`OutboxService` — those are the free-function modules built in
//! Migration Order steps 5/6 (`metap_query::plan_list`, `metap_workflow::*`), called
//! directly instead of held as constructor dependencies that wrap nothing.
//!
//! `entity` is fetched as an owned, cloned `EntityDefinition` at the top of every method
//! rather than borrowed from `self.metadata` — a deliberate simplicity choice (entities are
//! small; this sidesteps any borrow-across-`.await` friction) that can be revisited if
//! profiling ever shows it matters, not a performance decision made ahead of evidence.

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use arc_swap::ArcSwap;
use metap_control::{Router, RouterError};
use metap_metadata::{field_has_real_column, EntityDefinition, EntityField, FieldKind, MetadataRegistry};
use metap_permission::{EntityAction, PermissionDecision, PermissionService, PermissionSnapshot, RequestContext};
use metap_query::{
    apply_params, encode_cursor, plan_list, CrossRecordConditionInListError, Cursor, InvalidCursorError,
    InvalidJqlError, ListInput, SortDir, UnknownListViewError,
};
use metap_workflow::{
    apply_set_fields, emit_created, emit_deleted, emit_transitioned, emit_updated, find_transition, get_initial_status,
    record_event, run_guard, run_validator,
};
use serde_json::Value;
use sqlx::{PgExecutor, Row};
use uuid::Uuid;

use crate::dto::{JsonObject, RecordCapabilities, RecordDto, TransitionAvailability};
use crate::result::{PageInfo, ServiceResult};
use crate::validation::validate_payload;

const RECORD_COLUMNS: &str = "id, entity, code, status, data, version, created_at, updated_at";
/// Same shape minus `entity` — a table-per-entity table (`table_name != "records"`) has no
/// discriminator column, one table already means one entity. `row_to_dto_dedicated` fills
/// `RecordDto.entity` in from the already-known entity name instead.
const RECORD_COLUMNS_DEDICATED: &str = "id, code, status, data, version, created_at, updated_at";

fn is_dedicated(entity: &EntityDefinition) -> bool {
    entity.table_name != "records"
}

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
            let Some(ref_entity_def) = self.get_entity(ref_entity) else {
                continue;
            };
            if let Some(related_data) = fetch_related_data(&mut *tx, ref_id, tenant_id, &ref_entity_def).await? {
                enriched.insert(field_name, Value::Object(related_data));
            }
        }
        tx.commit().await?;
        Ok(enriched)
    }

    /// "Mode 2" batch display hydration (`docs/roadmap.md`) — the list-only counterpart to
    /// `enrich_record_for_actions` above, but for a different purpose: that method resolves
    /// relations *permission conditions* need, invisibly, never returned to the caller; this
    /// resolves relations a *list view* wants to display (any `Reference` field that declares
    /// `refDisplayField`), and its result — `RecordDto.related_display` — is exactly what gets
    /// serialized back. Solves the same problem a client fetching one relation's display value
    /// per row (N HTTP round trips for an N-row page) would otherwise hit: one batched
    /// `WHERE id = ANY($1)` query per such field for the whole page, not per row. Shares
    /// `list()`'s own transaction (`tx`) rather than opening a second one — `list()`'s
    /// `tx.commit()` now happens after this returns, not before.
    ///
    /// Zero extra cost for the overwhelmingly common case (no `Reference` field declares
    /// `refDisplayField`, or none of this page's rows have a value for one) — matches every
    /// other "pay for what you use" mechanism in this file. Runs after field-level read masking
    /// (`mask_record_for_read`) so a field the caller isn't allowed to see at all can't get a
    /// display value either, skips a field entirely if the caller can't read its target entity
    /// at all (`can_read_entity`), and — found in code review, 2026-08-22, since this used to
    /// stop at that coarse entity-level check — evaluates each related record's own record-level
    /// read policy (`PermissionSnapshot::can_perform_record_condition`, the same mechanism
    /// `get()` uses) before including its display value: a display convenience must not leak a
    /// value the caller would get a `403` for reading directly. Not a SQL-level `JOIN` (that's
    /// `metap_query::condition_to_sql`'s "not built" gap, Mode 3 in
    /// `docs/features/05-cross-entity-relations.md`) — the related rows are fetched first, then
    /// filtered in Rust, same shape `enrich_record_for_actions` already uses for its one-hop
    /// resolution.
    async fn hydrate_related_display(
        &self,
        entity: &EntityDefinition,
        context: &RequestContext,
        tenant_id: Uuid,
        tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
        records: Vec<RecordDto>,
    ) -> anyhow::Result<Vec<RecordDto>> {
        let display_fields: Vec<&EntityField> = entity
            .fields
            .iter()
            .filter(|f| f.kind == FieldKind::Reference && f.ref_display_field.is_some())
            .collect();
        if display_fields.is_empty() {
            return Ok(records);
        }

        // field name -> (related record id -> resolved display value)
        let mut resolved: HashMap<String, HashMap<Uuid, String>> = HashMap::new();
        for field in &display_fields {
            let ref_entity = field
                .ref_entity
                .as_deref()
                .expect("Reference field always has ref_entity");
            let display_field = field.ref_display_field.as_deref().expect("filtered on is_some above");

            let decision = self.permissions.can_read_entity(context, ref_entity).await?;
            if !decision.allowed {
                continue;
            }

            let ids: Vec<Uuid> = records
                .iter()
                .filter_map(|r| {
                    r.data
                        .get(&field.name)
                        .and_then(Value::as_str)
                        .and_then(|s| Uuid::parse_str(s).ok())
                })
                .collect::<HashSet<_>>()
                .into_iter()
                .collect();
            if ids.is_empty() {
                continue;
            }
            let Some(ref_entity_def) = self.get_entity(ref_entity) else {
                continue;
            };

            let related = fetch_related_records_batch(&mut **tx, &ids, tenant_id, &ref_entity_def).await?;
            if related.is_empty() {
                continue;
            }

            // Same record-level enforcement `get()` applies to a single related record
            // (`enrich_record_for_actions` + `can_perform_record_condition`), just evaluated
            // per row here instead of per relation-hop — a row this caller couldn't read
            // directly must not surface its display value through this entity's list either.
            let related_snapshot = self.permissions.load_snapshot(tenant_id, ref_entity).await?;
            let mut values: HashMap<Uuid, String> = HashMap::new();
            for (id, related_data) in related {
                let record_decision =
                    related_snapshot.can_perform_record_condition(context, &related_data, EntityAction::Read);
                if !record_decision.allowed {
                    continue;
                }
                if let Some(value) = related_data.get(display_field).and_then(Value::as_str) {
                    values.insert(id, value.to_string());
                }
            }
            resolved.insert(field.name.clone(), values);
        }

        if resolved.is_empty() {
            return Ok(records);
        }

        Ok(records
            .into_iter()
            .map(|record| {
                let mut display: HashMap<String, String> = HashMap::new();
                for field in &display_fields {
                    let Some(values) = resolved.get(&field.name) else {
                        continue;
                    };
                    let Some(id) = record
                        .data
                        .get(&field.name)
                        .and_then(Value::as_str)
                        .and_then(|s| Uuid::parse_str(s).ok())
                    else {
                        continue;
                    };
                    if let Some(value) = values.get(&id) {
                        display.insert(field.name.clone(), value.clone());
                    }
                }
                if display.is_empty() {
                    record
                } else {
                    RecordDto {
                        related_display: Some(display),
                        ..record
                    }
                }
            })
            .collect())
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
                if e.downcast_ref::<UnknownListViewError>().is_some() {
                    return Ok(ServiceResult::err_with_message(400, "unknown_list_view", e.to_string()));
                }
                if e.downcast_ref::<InvalidJqlError>().is_some() {
                    return Ok(ServiceResult::err_with_message(400, "invalid_jql", e.to_string()));
                }
                // Deterministic/permanent (an entity read-policy misconfiguration, not
                // something this specific request can fix) — still a `5xx`, but with its own
                // `code` and message rather than falling through to a generic, indistinguishable
                // `internal_error` (found in code review, 2026-08-22).
                if e.downcast_ref::<CrossRecordConditionInListError>().is_some() {
                    return Ok(ServiceResult::err_with_message(
                        500,
                        "unsupported_policy_condition",
                        e.to_string(),
                    ));
                }
                return Err(e);
            }
        };

        let dedicated = is_dedicated(&entity);
        let table = &entity.table_name;
        let columns = if dedicated {
            RECORD_COLUMNS_DEDICATED
        } else {
            RECORD_COLUMNS
        };
        let sql = format!(
            "SELECT {columns} FROM {table} WHERE {} ORDER BY {} LIMIT {}",
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

        let has_more = rows.len() as i64 > planned.limit;
        let page_rows: Vec<_> = if has_more {
            rows.into_iter().take(planned.limit as usize).collect()
        } else {
            rows
        };
        let page_dtos: Vec<RecordDto> = page_rows
            .into_iter()
            .map(|row| {
                if dedicated {
                    row_to_dto_dedicated(row, &entity.name)
                } else {
                    row_to_dto(row)
                }
            })
            .collect::<anyhow::Result<_>>()?;

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
        let data = self
            .hydrate_related_display(&entity, context, tenant_id, &mut tx, data)
            .await?;
        tx.commit().await?;

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
        let Some(existing) = fetch_existing(&mut *tx, id, tenant_id, &entity).await? else {
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

/// One `(entity, field)` pair `delete()` needs to check for an orphan reference, plus enough to
/// build the right query against wherever that entity's rows actually live.
struct ReferencingField {
    ref_entity: String,
    ref_field: String,
    ref_table: String,
    has_real_column: bool,
}

/// Every `(entity, field)` pair across the whole registry where `field` is a `Reference` kind
/// pointing at `target_entity` — the set `delete()` checks for orphan references. Includes
/// self-references (an entity referencing itself, e.g. a manager hierarchy) — deleting a record
/// other records of the *same* entity still point to is exactly the same orphan-reference risk.
fn referencing_fields(metadata: &MetadataRegistry, target_entity: &str) -> Vec<ReferencingField> {
    let mut result = Vec::new();
    for summary in metadata.list_entities() {
        for field in &summary.fields {
            if field.kind == FieldKind::Reference && field.ref_entity.as_deref() == Some(target_entity) {
                let ref_table = metadata
                    .get_entity(&summary.name)
                    .map(|e| e.table_name.clone())
                    .unwrap_or_else(|| "records".to_string());
                result.push(ReferencingField {
                    ref_entity: summary.name.clone(),
                    ref_field: field.name.clone(),
                    ref_table,
                    has_real_column: field_has_real_column(field),
                });
            }
        }
    }
    result
}

/// One combined query per **distinct physical table** among `referencing_fields`'s results
/// (`delete()`'s original one-query-per-pair loop, found too slow in code review 2026-08-22 —
/// an entity referenced by K fields used to cost K sequential round trips — got fixed by
/// combining onto one `records` query; table-per-entity now means a referencing entity might not
/// even be on `records`, so the combining has to happen per-table instead of unconditionally).
/// `AND id != $2` excludes the record's own row (self-references, e.g.
/// `crm.customers.referredBy`, are deliberately included in `refs` — without this exclusion a
/// record whose self-reference points at itself would match its own row and could never be
/// deleted, a second bug found in the same review pass).
///
/// A dedicated table holds exactly one entity's rows, so every `ReferencingField` grouped under
/// it shares the same `ref_entity` — no `entity` column to read back, unlike the `records` group.
/// If the same entity has two different fields both pointing at the target (rare), the field
/// reported is whichever appears first in that table's group — same tolerance the original
/// `records`-only version already had for the analogous case.
async fn find_referencing_record(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    tenant_id: Uuid,
    id: Uuid,
    refs: &[ReferencingField],
) -> anyhow::Result<Option<(String, String)>> {
    if refs.is_empty() {
        return Ok(None);
    }

    let mut by_table: std::collections::BTreeMap<&str, Vec<&ReferencingField>> = std::collections::BTreeMap::new();
    for r in refs {
        by_table.entry(r.ref_table.as_str()).or_default().push(r);
    }

    for (table, group) in by_table {
        if table == "records" {
            let mut sql =
                String::from("SELECT entity FROM records WHERE tenant_id = $1 AND deleted = false AND id != $2 AND (");
            let mut clauses = Vec::with_capacity(group.len());
            let mut param_idx = 3;
            for _ in &group {
                clauses.push(format!(
                    "(entity = ${} AND data ->> ${} = ${})",
                    param_idx,
                    param_idx + 1,
                    param_idx + 2
                ));
                param_idx += 3;
            }
            sql.push_str(&clauses.join(" OR "));
            sql.push_str(") LIMIT 1");

            let mut query = sqlx::query_scalar::<_, String>(&sql).bind(tenant_id).bind(id);
            for r in &group {
                query = query.bind(&r.ref_entity).bind(&r.ref_field).bind(id.to_string());
            }
            let matched_entity: Option<String> = query.fetch_optional(&mut **tx).await?;
            if let Some(matched) = matched_entity {
                if let Some(r) = group.iter().find(|r| r.ref_entity == matched) {
                    return Ok(Some((r.ref_entity.clone(), r.ref_field.clone())));
                }
            }
        } else {
            let mut clauses = Vec::with_capacity(group.len());
            for (i, r) in group.iter().enumerate() {
                let param_idx = i + 3;
                if r.has_real_column {
                    clauses.push(format!("\"{}\" = ${}::uuid", r.ref_field, param_idx));
                } else {
                    clauses.push(format!("data ->> '{}' = ${}", r.ref_field, param_idx));
                }
            }
            let sql = format!(
                "SELECT id FROM {table} WHERE tenant_id = $1 AND deleted = false AND id != $2 AND ({}) LIMIT 1",
                clauses.join(" OR ")
            );
            let mut query = sqlx::query_scalar::<_, Uuid>(&sql).bind(tenant_id).bind(id);
            for _ in &group {
                query = query.bind(id.to_string());
            }
            let matched: Option<Uuid> = query.fetch_optional(&mut **tx).await?;
            if matched.is_some() {
                let r = group[0];
                return Ok(Some((r.ref_entity.clone(), r.ref_field.clone())));
            }
        }
    }
    Ok(None)
}

async fn fetch_existing<'e, E: PgExecutor<'e>>(
    executor: E,
    id: Uuid,
    tenant_id: Uuid,
    entity: &EntityDefinition,
) -> anyhow::Result<Option<RecordDto>> {
    let dedicated = is_dedicated(entity);
    let table = &entity.table_name;
    let row = if dedicated {
        sqlx::query(&format!(
            "SELECT {RECORD_COLUMNS_DEDICATED} FROM {table} WHERE id = $1 AND tenant_id = $2 AND deleted = false"
        ))
        .bind(id)
        .bind(tenant_id)
        .fetch_optional(executor)
        .await?
    } else {
        sqlx::query(&format!(
            "SELECT {RECORD_COLUMNS} FROM {table} \
             WHERE id = $1 AND tenant_id = $2 AND entity = $3 AND deleted = false"
        ))
        .bind(id)
        .bind(tenant_id)
        .bind(&entity.name)
        .fetch_optional(executor)
        .await?
    };
    row.map(|r| {
        if dedicated {
            row_to_dto_dedicated(r, &entity.name)
        } else {
            row_to_dto(r)
        }
    })
    .transpose()
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
    ref_entity: &EntityDefinition,
) -> anyhow::Result<Option<JsonObject>> {
    let table = &ref_entity.table_name;
    let row = if is_dedicated(ref_entity) {
        sqlx::query(&format!(
            "SELECT data FROM {table} WHERE id = $1 AND tenant_id = $2 AND deleted = false"
        ))
        .bind(id)
        .bind(tenant_id)
        .fetch_optional(executor)
        .await?
    } else {
        sqlx::query(&format!(
            "SELECT data FROM {table} WHERE id = $1 AND tenant_id = $2 AND entity = $3 AND deleted = false"
        ))
        .bind(id)
        .bind(tenant_id)
        .bind(&ref_entity.name)
        .fetch_optional(executor)
        .await?
    };
    let Some(row) = row else {
        return Ok(None);
    };
    let data_value: Value = row.try_get("data")?;
    Ok(data_value.as_object().cloned())
}

/// Batched counterpart to `fetch_related_data`, for `CrudService::hydrate_related_display` —
/// one query for every id a whole list page needs from a given related entity, instead of one
/// query per row. Returns each related record's *whole* `data` (not just the display field, the
/// original, narrower version of this function did) — `hydrate_related_display` needs to run
/// `can_perform_record_condition` per row before deciding whether the display value is even
/// allowed to leave the server, and a record-level condition can reference any field, not just
/// the one being displayed.
async fn fetch_related_records_batch<'e, E: PgExecutor<'e>>(
    executor: E,
    ids: &[Uuid],
    tenant_id: Uuid,
    ref_entity: &EntityDefinition,
) -> anyhow::Result<HashMap<Uuid, JsonObject>> {
    let table = &ref_entity.table_name;
    let rows = if is_dedicated(ref_entity) {
        sqlx::query(&format!(
            "SELECT id, data FROM {table} WHERE id = ANY($1) AND tenant_id = $2 AND deleted = false"
        ))
        .bind(ids)
        .bind(tenant_id)
        .fetch_all(executor)
        .await?
    } else {
        sqlx::query(&format!(
            "SELECT id, data FROM {table} WHERE id = ANY($1) AND tenant_id = $2 AND entity = $3 AND deleted = false"
        ))
        .bind(ids)
        .bind(tenant_id)
        .bind(&ref_entity.name)
        .fetch_all(executor)
        .await?
    };
    let mut result = HashMap::new();
    for row in rows {
        let id: Uuid = row.try_get("id")?;
        let data: Value = row.try_get("data")?;
        if let Some(obj) = data.as_object() {
            result.insert(id, obj.clone());
        }
    }
    Ok(result)
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
        related_display: None,
    })
}

/// `row_to_dto`'s counterpart for a table-per-entity table (`RECORD_COLUMNS_DEDICATED` — no
/// `entity` column to read back), `entity_name` supplied by the caller instead (always already
/// known — every call site already resolved the `EntityDefinition` being queried).
fn row_to_dto_dedicated(row: sqlx::postgres::PgRow, entity_name: &str) -> anyhow::Result<RecordDto> {
    let data_value: Value = row.try_get("data")?;
    let data = data_value
        .as_object()
        .cloned()
        .ok_or_else(|| anyhow::anyhow!("dedicated table's data was not a JSON object"))?;
    Ok(RecordDto {
        id: row.try_get("id")?,
        entity: entity_name.to_string(),
        code: row.try_get("code")?,
        status: row.try_get("status")?,
        data,
        version: row.try_get("version")?,
        created_at: row.try_get("created_at")?,
        updated_at: row.try_get("updated_at")?,
        related_display: None,
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

/// `None` when the field is genuinely unset (missing key or explicit JSON `null`) — distinct
/// from `Some(String::new())`, an actual empty string. Collapsing the two used to be the root
/// cause of `AUDIT_2.md`'s keyset-pagination data-loss bug: see `Cursor::value`'s doc comment.
fn sort_field_value(row: &RecordDto, field: &str) -> Option<String> {
    match field {
        "createdAt" => Some(row.created_at.to_rfc3339()),
        "updatedAt" => Some(row.updated_at.to_rfc3339()),
        _ => match row.data.get(field) {
            Some(Value::String(s)) => Some(s.clone()),
            Some(Value::Number(n)) => Some(n.to_string()),
            Some(Value::Bool(b)) => Some(b.to_string()),
            Some(v) if !v.is_null() => Some(v.to_string()),
            _ => None,
        },
    }
}
