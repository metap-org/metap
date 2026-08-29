//! Shared free functions used across `crud_service`'s per-operation submodules
//! (`list`/`get`/`create`/`update`/`transition`/`delete`) — error-response mapping, row<->DTO
//! conversion, field/record masking, capability computation, and the delete-time
//! reference-integrity guard. Split out of the single `crud_service.rs` file it used to all
//! live in (`docs/roadmap.md`) purely to keep each file a manageable size — no behavior change.

use std::collections::HashMap;

use metap_control::RouterError;
use metap_metadata::{field_has_real_column, EntityDefinition, FieldKind, MetadataRegistry};
use metap_permission::{EntityAction, PermissionDecision, PermissionSnapshot, RequestContext};
use metap_workflow::run_guard;
use serde_json::Value;
use sqlx::{PgExecutor, Row};
use uuid::Uuid;

use crate::dto::{JsonObject, RecordCapabilities, RecordDto, TransitionAvailability};
use crate::result::ServiceResult;

pub(crate) const RECORD_COLUMNS: &str = "id, entity, code, status, data, version, created_at, updated_at";
/// Same shape minus `entity` — a table-per-entity table (`table_name != "records"`) has no
/// discriminator column, one table already means one entity. `row_to_dto_dedicated` fills
/// `RecordDto.entity` in from the already-known entity name instead.
pub(crate) const RECORD_COLUMNS_DEDICATED: &str = "id, code, status, data, version, created_at, updated_at";

pub(crate) fn is_dedicated(entity: &EntityDefinition) -> bool {
    entity.table_name != "records"
}

pub(crate) fn parse_user_id(context: &RequestContext) -> anyhow::Result<Option<Uuid>> {
    Ok(context.user_id.as_deref().map(Uuid::parse_str).transpose()?)
}

pub(crate) fn forbidden<T>(decision: PermissionDecision) -> ServiceResult<T> {
    ServiceResult::err(403, decision.reason.unwrap_or_else(|| "forbidden".to_string()))
}

pub(crate) fn forbidden_with_field<T>(decision: PermissionDecision) -> ServiceResult<T> {
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
pub(crate) fn unique_violation<T>(entity_name: &str, error: &sqlx::Error) -> Option<ServiceResult<T>> {
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
pub(crate) fn router_unavailable<T>(error: &anyhow::Error) -> Option<ServiceResult<T>> {
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
pub(crate) struct ReferencingField {
    ref_entity: String,
    ref_field: String,
    ref_table: String,
    has_real_column: bool,
}

/// Every `(entity, field)` pair across the whole registry where `field` is a `Reference` kind
/// pointing at `target_entity` — the set `delete()` checks for orphan references. Includes
/// self-references (an entity referencing itself, e.g. a manager hierarchy) — deleting a record
/// other records of the *same* entity still point to is exactly the same orphan-reference risk.
pub(crate) fn referencing_fields(metadata: &MetadataRegistry, target_entity: &str) -> Vec<ReferencingField> {
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
pub(crate) async fn find_referencing_record(
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

pub(crate) async fn fetch_existing<'e, E: PgExecutor<'e>>(
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

/// Batched counterpart to `fetch_existing`, for `CrudService::get_many` — one query for every id
/// instead of one `fetch_existing` call per id. Unlike `fetch_related_records_batch` (which only
/// ever needs the raw `data` blob for cross-record permission evaluation), this returns full
/// `RecordDto`s since `get_many`'s caller-facing contract mirrors `get`'s, not an internal
/// enrichment hop's. Order is whatever `= ANY($1)` returns (not necessarily `ids`' order) —
/// `get_many` reorders to match the caller's `ids`.
pub(crate) async fn fetch_existing_batch<'e, E: PgExecutor<'e>>(
    executor: E,
    ids: &[Uuid],
    tenant_id: Uuid,
    entity: &EntityDefinition,
) -> anyhow::Result<Vec<RecordDto>> {
    let dedicated = is_dedicated(entity);
    let table = &entity.table_name;
    let rows = if dedicated {
        sqlx::query(&format!(
            "SELECT {RECORD_COLUMNS_DEDICATED} FROM {table} WHERE id = ANY($1) AND tenant_id = $2 AND deleted = false"
        ))
        .bind(ids)
        .bind(tenant_id)
        .fetch_all(executor)
        .await?
    } else {
        sqlx::query(&format!(
            "SELECT {RECORD_COLUMNS} FROM {table} \
             WHERE id = ANY($1) AND tenant_id = $2 AND entity = $3 AND deleted = false"
        ))
        .bind(ids)
        .bind(tenant_id)
        .bind(&entity.name)
        .fetch_all(executor)
        .await?
    };
    rows.into_iter()
        .map(|r| {
            if dedicated {
                row_to_dto_dedicated(r, &entity.name)
            } else {
                row_to_dto(r)
            }
        })
        .collect()
}

/// Raw `data` fetch for one hop of cross-record permission enrichment (see
/// `CrudService::enrich_record_for_actions`) — deliberately not `fetch_existing` (no need for
/// the full `RecordDto`/`RECORD_COLUMNS` shape, just the JSONB blob to merge into a subject)
/// and deliberately no permission check on the related record: this never leaves the server as
/// a response, it's only ever fed into `PolicyCondition` evaluation for the *current* record.
pub(crate) async fn fetch_related_data<'e, E: PgExecutor<'e>>(
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
pub(crate) async fn fetch_related_records_batch<'e, E: PgExecutor<'e>>(
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

pub(crate) fn row_to_dto(row: sqlx::postgres::PgRow) -> anyhow::Result<RecordDto> {
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
pub(crate) fn row_to_dto_dedicated(row: sqlx::postgres::PgRow, entity_name: &str) -> anyhow::Result<RecordDto> {
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
pub(crate) fn mask_record_for_read(
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

pub(crate) fn compute_capabilities(
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
pub(crate) fn sort_field_value(row: &RecordDto, field: &str) -> Option<String> {
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
