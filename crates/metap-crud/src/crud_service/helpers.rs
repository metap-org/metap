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

/// Fills in every `computed` field's value from the rest of `data` — see
/// `docs/features/13-computed-derived-field.md`. Called from `create`/`update` after
/// `validate_payload` has already accepted the payload (so `data` only contains known,
/// correctly-typed fields) and before the write lands, so REST/webhook/cron paths (which all go
/// through `CrudService`) can never disagree on a computed field's value. Always overwrites
/// whatever the client sent for a computed field, if anything — the server is the only source of
/// truth for it, matching `compiler::validate`'s rejection of `required: true` on a computed
/// field (nothing needs to be enforced about client input here, only the correct value written).
pub(crate) fn recompute_fields(entity: &EntityDefinition, data: &mut JsonObject) {
    for field in &entity.fields {
        let Some(computed) = &field.computed else { continue };
        let rendered = metap_metadata::render_expression(&computed.expression, |name| {
            data.get(name).map(computed_token_to_string)
        });
        data.insert(field.name.clone(), Value::String(rendered));
    }
}

/// How a dependency field's current value renders inside a computed-field template — `String`
/// values pass through as-is, `Null`/absent renders empty (handled by `render_expression`'s
/// `unwrap_or_default` when this returns `None`... this function only runs when the key IS
/// present, so it only needs to special-case `Value::Null` itself), everything else uses
/// `serde_json`'s own `Display` (numbers/bools print their literal, arrays/objects print as
/// compact JSON — acceptable for v1's "string template", not attempting pretty-printing).
fn computed_token_to_string(value: &Value) -> String {
    match value {
        Value::String(s) => s.clone(),
        Value::Null => String::new(),
        other => other.to_string(),
    }
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

/// A DB unique-index violation, caught after the fact at the `INSERT`/`UPDATE` call site (never
/// pre-checked) and turned into a `409` naming *what* collided, rather than surfacing as an
/// unhandled 500 (`?` on the query result would otherwise convert straight to `anyhow::Error`).
/// Returns `None` for any other database error, so the caller's `Err(e) => return Err(e.into())`
/// fallback still applies.
///
/// The field-name extraction has to reverse-engineer the violated constraint's identity from
/// its bare DB name, which comes from two different naming schemes depending on where the field
/// lives (`is_dedicated`): the shared `records` table's `uniq_records_<entity>_<field>`
/// (`metap-peripherals::index_reconciler::ensure_index`) or a dedicated table's
/// `uniq_<table>_<field>`/`uniq_<table>_<field1>_<field2>_...` (`metap_reconciler::compile()`'s
/// single- and composite-field naming — no `records_` in the middle). Found live, 2026-09-07: a
/// `waf.ddos_policies` create returned only `{"code":"unique_violation"}` to the browser, no
/// field/table at all, because this function only ever tried the `records` prefix — every
/// dedicated-table entity's violation silently fell through to the generic branch. Takes the
/// full `EntityDefinition` (not just the name) to pick the right prefix via `is_dedicated`, and
/// to try `entity.unique_constraints` by exact recomputed name *before* falling back to a plain
/// `strip_prefix` (which alone can't tell a composite constraint's joined field names apart from
/// one field literally named that way).
fn unnamed_unique_violation<T>(entity: &EntityDefinition) -> ServiceResult<T> {
    ServiceResult::err_with_message(
        409,
        "unique_violation",
        format!("A unique constraint was violated on \"{}\".", entity.name),
    )
}

/// `prefix` is already `uniq_records_<entity>_`/`uniq_<table>_` (the caller's own
/// `is_dedicated`-picked one) — this just joins the constraint's field names onto it, matching
/// `metap_reconciler::compile::composite_unique_index_name`'s naming exactly for any name that
/// didn't need that function's 63-byte truncate-with-hash fallback (see `unique_violation`'s doc
/// comment for why that fallback isn't reproduced here too).
fn composite_unique_index_name(prefix: &str, fields: &[String]) -> String {
    format!("{prefix}{}", fields.join("_"))
}

pub(crate) fn unique_violation<T>(entity: &EntityDefinition, error: &sqlx::Error) -> Option<ServiceResult<T>> {
    let sqlx::Error::Database(db_err) = error else {
        return None;
    };
    if !db_err.is_unique_violation() {
        return None;
    }
    let Some(constraint_name) = db_err.constraint() else {
        return Some(unnamed_unique_violation(entity));
    };

    let mangled = entity.name.replace('.', "_");
    let prefix = if is_dedicated(entity) {
        format!("uniq_{mangled}_")
    } else {
        format!("uniq_records_{mangled}_")
    };

    // Composite constraints first — exact-name match against what `compile()` would have built
    // (`metap_reconciler::compile::composite_unique_index_name`, duplicated here rather than
    // depended on: `metap-crud` sits below `metap-reconciler` in the layering, and this is the
    // same "duplicate the trivial pure naming logic" convention `metap-peripherals`'s own index
    // naming already uses instead of depending on `metap-reconciler` for it). A composite
    // constraint's fields all get blamed — a form UI highlighting all of them is more useful
    // than guessing which one "really" caused it.
    for constraint in &entity.unique_constraints {
        if composite_unique_index_name(&prefix, &constraint.fields) == constraint_name {
            let message = vec!["A record with this combination of values already exists.".to_string()];
            let field_errors = constraint.fields.iter().map(|f| (f.clone(), message.clone())).collect();
            return Some(ServiceResult::err_with_field_errors(
                409,
                "unique_violation",
                field_errors,
            ));
        }
    }

    let field = constraint_name.strip_prefix(&prefix).map(str::to_string);
    Some(match field {
        Some(field) => ServiceResult::err_with_field_errors(
            409,
            "unique_violation",
            HashMap::from([(field, vec!["A record with this value already exists.".to_string()])]),
        ),
        // Constraint name didn't match either known naming convention — either a composite
        // constraint whose name got hash-truncated (`compile()`'s 63-byte fallback; not
        // reproduced here, a rare case not worth a third copy of that hashing logic) or
        // something outside what this crate itself ever declares. Still say *which entity*,
        // never a bare code with zero context.
        None => ServiceResult::err_with_message(
            409,
            "unique_violation",
            format!("A unique constraint was violated on \"{}\".", entity.name),
        ),
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

/// One blocking row `delete()`'s reference-integrity guard found — enough for the API response to
/// name exactly which record is blocking the delete (`entity`/`id`), not just which field.
pub(crate) struct ReferencingRecordHit {
    pub(crate) entity: String,
    pub(crate) field: String,
    pub(crate) id: Uuid,
}

/// Total blocking rows reported across every referencing table combined — this only runs on the
/// (cold, delete-time) path, so this exists purely to keep the error response bounded, not for
/// query performance.
const MAX_REFERENCING_HITS: i64 = 50;

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
/// Returns every blocking row (up to [`MAX_REFERENCING_HITS`] total), not just the first —
/// `delete()` reports the full list so the caller can jump straight to what's blocking it instead
/// of discovering blockers one delete attempt at a time (found live, 2026-09-08, after a user
/// hit this on the WAF portal and asked for more context than a single field name).
///
/// A dedicated table holds exactly one entity's rows, so every `ReferencingField` grouped under
/// it shares the same `ref_entity` — no `entity` column to read back, unlike the `records` group.
/// If the same entity has two different fields both pointing at the target (rare), every matching
/// row in that table is attributed to the group's first field — same tolerance the original
/// single-hit version already had for the analogous case, now applied per row instead of
/// per query.
pub(crate) async fn find_referencing_records(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    tenant_id: Uuid,
    id: Uuid,
    refs: &[ReferencingField],
) -> anyhow::Result<Vec<ReferencingRecordHit>> {
    let mut hits = Vec::new();
    if refs.is_empty() {
        return Ok(hits);
    }

    let mut by_table: std::collections::BTreeMap<&str, Vec<&ReferencingField>> = std::collections::BTreeMap::new();
    for r in refs {
        by_table.entry(r.ref_table.as_str()).or_default().push(r);
    }

    for (table, group) in by_table {
        if hits.len() as i64 >= MAX_REFERENCING_HITS {
            break;
        }
        let remaining = MAX_REFERENCING_HITS - hits.len() as i64;

        if table == "records" {
            let mut sql = String::from(
                "SELECT id, entity FROM records WHERE tenant_id = $1 AND deleted = false AND id != $2 AND (",
            );
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
            sql.push_str(&format!(") LIMIT {remaining}"));

            let mut query = sqlx::query_as::<_, (Uuid, String)>(&sql).bind(tenant_id).bind(id);
            for r in &group {
                query = query.bind(&r.ref_entity).bind(&r.ref_field).bind(id.to_string());
            }
            for (row_id, row_entity) in query.fetch_all(&mut **tx).await? {
                if let Some(r) = group.iter().find(|r| r.ref_entity == row_entity) {
                    hits.push(ReferencingRecordHit {
                        entity: r.ref_entity.clone(),
                        field: r.ref_field.clone(),
                        id: row_id,
                    });
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
                "SELECT id FROM {table} WHERE tenant_id = $1 AND deleted = false AND id != $2 AND ({}) LIMIT {remaining}",
                clauses.join(" OR ")
            );
            let mut query = sqlx::query_scalar::<_, Uuid>(&sql).bind(tenant_id).bind(id);
            for _ in &group {
                query = query.bind(id.to_string());
            }
            for row_id in query.fetch_all(&mut **tx).await? {
                hits.push(ReferencingRecordHit {
                    entity: group[0].ref_entity.clone(),
                    field: group[0].ref_field.clone(),
                    id: row_id,
                });
            }
        }
    }
    Ok(hits)
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
    // Third distinct action for the same reason — see `RecordCapabilities::can_delete`. Note this
    // is only the *record-level* (ABAC) half, exactly as `can_update` is: `CrudService::delete`
    // additionally runs the entity-level `can_delete_entity` check and the reference-integrity
    // guard, neither of which this can predict, so `can_delete: true` means "no policy condition
    // stands in your way", not "this delete is guaranteed to succeed".
    let delete_decision = snapshot.can_perform_record_condition(context, existing_data, EntityAction::Delete);
    let can_delete = delete_decision.allowed;

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
        can_delete,
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
