//! Mirrors `packages/core/src/core/query/query-planner.ts`. Per
//! `docs/architecture-review-2026-08-07.md` Part 1's Repository finding, `QueryPlanner` is
//! the actual Postgres-dialect seam in this codebase (`jsonb_extract_path_text`, keyset
//! pagination, FTS) — the highest-precision-risk module in the whole port, per
//! `docs/rust-core-viability.md`'s Migration Order. Verified against the real dev Postgres
//! in `tests/query_planner_postgres.rs`, not just unit-tested in isolation.

use std::collections::HashSet;

use metap_metadata::MetadataRegistry;
use metap_permission::{PermissionService, PolicyRow, RequestContext};
use uuid::Uuid;

use crate::condition_to_sql::record_policy_where_clause;
use crate::cursor::{decode_cursor, SortDir};
use crate::sql_builder::{BindValue, ParamBuilder};

#[derive(Debug)]
pub struct InvalidCursorError(pub String);

impl std::fmt::Display for InvalidCursorError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::error::Error for InvalidCursorError {}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResolvedSort {
    pub field: String,
    pub descending: bool,
}

#[derive(Debug, Clone, Default)]
pub struct ListInput {
    pub limit: i64,
    pub sort: Option<String>,
    /// `Vec`, not a map — deterministic iteration order matters here (the generated `$N`
    /// numbering must be reproducible for the same input, and HTTP query params already
    /// arrive in a stable order).
    pub filters: Vec<(String, String)>,
    pub cursor: Option<String>,
}

pub struct PlannedListQuery {
    pub where_sql: String,
    pub order_by_sql: String,
    pub params: Vec<BindValue>,
    pub limit: i64,
    pub resolved_sort: ResolvedSort,
}

fn parse_sort(candidate: Option<&str>, sortable_fields: &HashSet<String>) -> Option<ResolvedSort> {
    let candidate = candidate?;
    let descending = candidate.starts_with('-');
    let field = candidate.strip_prefix('-').unwrap_or(candidate);
    if sortable_fields.contains(field) {
        Some(ResolvedSort { field: field.to_string(), descending })
    } else {
        None
    }
}

enum SortColType {
    Timestamptz,
    Text,
}

fn sort_field_expression(field_name: &str, params: &mut ParamBuilder) -> (String, SortColType) {
    match field_name {
        "createdAt" => ("created_at".to_string(), SortColType::Timestamptz),
        "updatedAt" => ("updated_at".to_string(), SortColType::Timestamptz),
        _ => {
            let ph = params.push(BindValue::Text(field_name.to_string()));
            (format!("jsonb_extract_path_text(data, {ph})"), SortColType::Text)
        }
    }
}

fn bind_cursor_value(
    col_type: &SortColType,
    value: &str,
    params: &mut ParamBuilder,
) -> String {
    match col_type {
        SortColType::Timestamptz => {
            format!("{}::timestamptz", params.push(BindValue::Text(value.to_string())))
        }
        SortColType::Text => params.push(BindValue::Text(value.to_string())),
    }
}

/// Same signature shape as `QueryPlanner.planList` (metadata + permissions injected, not
/// held as struct state — there's no other state to hold, so a plain function is the
/// faithful equivalent here rather than a single-method struct).
pub fn plan_list(
    metadata: &MetadataRegistry,
    permissions: &PermissionService,
    entity_name: &str,
    input: &ListInput,
    context: &RequestContext,
    record_read_policies: &[PolicyRow],
) -> anyhow::Result<PlannedListQuery> {
    let entity = metadata
        .get_entity(entity_name)
        .ok_or_else(|| anyhow::anyhow!("Entity not found: {entity_name}"))?;

    let tenant_id = permissions.scoped_tenant(context)?;
    let list_view = entity.list_views.first();
    let limit = input.limit.min(list_view.map(|lv| lv.max_limit as i64).unwrap_or(100));

    let mut params = ParamBuilder::new();
    let mut conditions: Vec<String> = Vec::new();

    conditions.push(format!("tenant_id = {}", params.push(BindValue::Uuid(tenant_id))));
    conditions.push(format!("entity = {}", params.push(BindValue::Text(entity.name.clone()))));
    conditions.push("deleted = false".to_string());

    if let Some(record_condition) = record_policy_where_clause(record_read_policies, context, &mut params)? {
        conditions.push(record_condition);
    }

    let allowed_filter_fields: HashSet<&str> =
        list_view.map(|lv| lv.filters.iter().map(String::as_str).collect()).unwrap_or_default();
    let fields_by_name: std::collections::HashMap<&str, &metap_metadata::EntityField> =
        entity.fields.iter().map(|f| (f.name.as_str(), f)).collect();

    for (field, value) in &input.filters {
        if !allowed_filter_fields.contains(field.as_str()) {
            continue;
        }

        let (field_expr, _) = sort_field_expression(field, &mut params);
        let field_def = fields_by_name.get(field.as_str());

        let is_fts = field_def.is_some_and(|f| {
            f.searchable.unwrap_or(false) && f.search_mode.as_deref() == Some("fts")
        });
        let is_substring_search = field_def.is_some_and(|f| f.searchable.unwrap_or(false)) && !is_fts;

        if is_fts {
            let ph = params.push(BindValue::Text(value.clone()));
            conditions.push(format!(
                "to_tsvector('simple', {field_expr}) @@ plainto_tsquery('simple', {ph})"
            ));
        } else if is_substring_search {
            let escaped: String = value
                .chars()
                .flat_map(|c| if matches!(c, '\\' | '%' | '_') { vec!['\\', c] } else { vec![c] })
                .collect();
            let ph = params.push(BindValue::Text(format!("%{escaped}%")));
            conditions.push(format!("{field_expr} ILIKE {ph}"));
        } else {
            let ph = params.push(BindValue::Text(value.clone()));
            conditions.push(format!("{field_expr} = {ph}"));
        }
    }

    let mut sortable_fields: HashSet<String> = entity
        .fields
        .iter()
        .filter(|f| f.sortable.unwrap_or(false))
        .map(|f| f.name.clone())
        .collect();
    sortable_fields.insert("createdAt".to_string());
    sortable_fields.insert("updatedAt".to_string());

    let resolved_sort = parse_sort(input.sort.as_deref(), &sortable_fields)
        .or_else(|| parse_sort(list_view.and_then(|lv| lv.default_sort.as_deref()), &sortable_fields))
        .unwrap_or(ResolvedSort { field: "createdAt".to_string(), descending: true });

    let (sort_expr, sort_col_type) = sort_field_expression(&resolved_sort.field, &mut params);

    if let Some(raw_cursor) = &input.cursor {
        let cursor = decode_cursor(raw_cursor)
            .ok_or_else(|| InvalidCursorError("Cursor does not match the current sort".to_string()))?;

        let cursor_dir_matches = cursor.dir == if resolved_sort.descending { SortDir::Desc } else { SortDir::Asc };
        if cursor.field != resolved_sort.field || !cursor_dir_matches {
            return Err(InvalidCursorError("Cursor does not match the current sort".to_string()).into());
        }

        let cursor_id = Uuid::parse_str(&cursor.id)
            .map_err(|_| InvalidCursorError("Cursor does not match the current sort".to_string()))?;
        let value_ph = bind_cursor_value(&sort_col_type, &cursor.value, &mut params);
        let id_ph = params.push(BindValue::Uuid(cursor_id));

        let tiebreak = format!("({sort_expr} = {value_ph} AND id > {id_ph})");
        let cursor_condition = if resolved_sort.descending {
            format!("(({sort_expr} < {value_ph}) OR {tiebreak})")
        } else {
            format!("(({sort_expr} > {value_ph}) OR {tiebreak})")
        };
        conditions.push(cursor_condition);
    }

    let direction = if resolved_sort.descending { "DESC" } else { "ASC" };
    let order_by_sql = format!("{sort_expr} {direction}, id ASC");

    Ok(PlannedListQuery {
        where_sql: conditions.join(" AND "),
        order_by_sql,
        params: params.params,
        limit,
        resolved_sort,
    })
}
