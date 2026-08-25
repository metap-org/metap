//! Mirrors `packages/core/src/core/query/query-planner.ts`. Per
//! `docs/architectures/09-adr.md`, `QueryPlanner` is
//! the actual Postgres-dialect seam in this codebase (`jsonb_extract_path_text`, keyset
//! pagination, FTS) — the highest-precision-risk module in the whole Rust port. Verified
//! against the real dev Postgres
//! in `tests/query_planner_postgres.rs`, not just unit-tested in isolation.

use std::collections::HashSet;

use metap_metadata::{field_has_real_column, EntityField, MetadataRegistry};
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

/// `?listView=<name>` requested a list view that doesn't exist on this entity
/// (`docs/features/01-fe-platform-overhaul.md`'s first scoped gap — `plan_list` used to only
/// ever look at `list_views.first()`, with no way to reach a second list view like
/// `accounting.journal`'s `ledger` through the API at all). Downcast to a clean `400` by
/// `CrudService::list`, same pattern as `InvalidCursorError`.
#[derive(Debug)]
pub struct UnknownListViewError(pub String);

impl std::fmt::Display for UnknownListViewError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Unknown list view: {}", self.0)
    }
}
impl std::error::Error for UnknownListViewError {}

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
    /// Selects which of an entity's `list_views` to use for `max_limit`/`filters`/`default_sort`
    /// — `None` (the default, and every caller before this field existed) keeps the original
    /// behavior of always using `list_views.first()`. An unknown name is a `400`
    /// (`UnknownListViewError`) rather than a silent fallback to `first()` (unlike an
    /// unrecognized `sort`/filter field, which `plan_list` tolerates by falling back/ignoring):
    /// silently switching a caller to a different list view than the one they asked for could
    /// mean a totally different filter/column set with no indication anything went wrong,
    /// whereas one extra ignored filter is a much smaller degradation.
    pub list_view: Option<String>,
    /// `crate::jql`'s small query language — `AND`ed together with `filters`/the list view's
    /// implicit conditions, not a replacement for them (a caller can combine `?status=done&jql=...`
    /// freely). `None` (every caller before this field existed) skips JQL parsing entirely.
    pub jql: Option<String>,
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
        Some(ResolvedSort {
            field: field.to_string(),
            descending,
        })
    } else {
        None
    }
}

enum SortColType {
    Timestamptz,
    Uuid,
    Text,
}

impl SortColType {
    /// Cast suffix for a bind placeholder — a real (non-JSONB) column needs its comparison value
    /// cast to match, since sqlx binds every parameter as text/unknown by default and Postgres
    /// has no implicit text→uuid/timestamptz cast for `=`/`<`/`>`.
    fn cast_suffix(&self) -> &'static str {
        match self {
            SortColType::Timestamptz => "::timestamptz",
            SortColType::Uuid => "::uuid",
            SortColType::Text => "",
        }
    }
}

/// `field_def`/`dedicated_table` let a table-per-entity field with a real physical column
/// (`field_has_real_column`, `crates/metap-metadata/src/entity.rs`) resolve to that column
/// directly instead of the `jsonb_extract_path_text(data, ...)` fallback every field on the
/// shared `records` table uses. Only `Reference`/`Id` fields (the only kinds with a real column,
/// per `compile.rs`) get `SortColType::Uuid` — everything else promoted to a real column is
/// still a text-typed column, same cast behavior as the JSONB fallback.
fn sort_field_expression(
    field_name: &str,
    field_def: Option<&EntityField>,
    dedicated_table: bool,
    params: &mut ParamBuilder,
) -> (String, SortColType) {
    match field_name {
        "createdAt" => return ("created_at".to_string(), SortColType::Timestamptz),
        "updatedAt" => return ("updated_at".to_string(), SortColType::Timestamptz),
        _ => {}
    }
    if dedicated_table {
        if let Some(field) = field_def {
            if field_has_real_column(field) {
                let col_type = if matches!(
                    field.kind,
                    metap_metadata::FieldKind::Reference | metap_metadata::FieldKind::Id
                ) {
                    SortColType::Uuid
                } else {
                    SortColType::Text
                };
                return (format!("\"{}\"", field.name), col_type);
            }
        }
    }
    let ph = params.push(BindValue::Text(field_name.to_string()));
    (format!("jsonb_extract_path_text(data, {ph})"), SortColType::Text)
}

fn bind_cursor_value(col_type: &SortColType, value: &str, params: &mut ParamBuilder) -> String {
    format!(
        "{}{}",
        params.push(BindValue::Text(value.to_string())),
        col_type.cast_suffix()
    )
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
    let entity = metadata.get_entity(entity_name).ok_or_else(|| {
        tracing::warn!(entity = entity_name, "list rejected: entity not found");
        anyhow::anyhow!("Entity not found: {entity_name}")
    })?;

    let tenant_id = permissions.scoped_tenant(context)?;
    let list_view = match &input.list_view {
        Some(name) => {
            let found = entity.list_views.iter().find(|lv| &lv.name == name);
            if found.is_none() {
                tracing::debug!(
                    entity = entity.name,
                    list_view = name,
                    "list rejected: unknown list view"
                );
                return Err(UnknownListViewError(name.clone()).into());
            }
            found
        }
        None => entity.list_views.first(),
    };
    let limit = input.limit.min(list_view.map(|lv| lv.max_limit as i64).unwrap_or(100));

    let mut params = ParamBuilder::new();
    let mut conditions: Vec<String> = Vec::new();

    let dedicated_table = entity.table_name != "records";

    conditions.push(format!("tenant_id = {}", params.push(BindValue::Uuid(tenant_id))));
    if !dedicated_table {
        // A dedicated per-entity table (`table_name != "records"`) has no `entity` discriminator
        // column — one table already means one entity, nothing to filter by.
        conditions.push(format!(
            "entity = {}",
            params.push(BindValue::Text(entity.name.clone()))
        ));
    }
    conditions.push("deleted = false".to_string());

    if let Some(record_condition) = record_policy_where_clause(record_read_policies, context, &mut params)? {
        conditions.push(record_condition);
    }

    let allowed_filter_fields: HashSet<&str> = list_view
        .map(|lv| lv.filters.iter().map(String::as_str).collect())
        .unwrap_or_default();
    let fields_by_name: std::collections::HashMap<&str, &metap_metadata::EntityField> =
        entity.fields.iter().map(|f| (f.name.as_str(), f)).collect();

    for (field, value) in &input.filters {
        if !allowed_filter_fields.contains(field.as_str()) {
            tracing::debug!(
                entity = entity.name,
                field,
                "filter ignored: not in this entity's list-view filters"
            );
            continue;
        }

        let field_def = fields_by_name.get(field.as_str());
        let (field_expr, field_col_type) =
            sort_field_expression(field, field_def.copied(), dedicated_table, &mut params);

        let is_fts =
            field_def.is_some_and(|f| f.searchable.unwrap_or(false) && f.search_mode.as_deref() == Some("fts"));
        let is_substring_search = field_def.is_some_and(|f| f.searchable.unwrap_or(false)) && !is_fts;

        if is_fts {
            let ph = params.push(BindValue::Text(value.clone()));
            conditions.push(format!(
                "to_tsvector('simple', {field_expr}) @@ plainto_tsquery('simple', {ph})"
            ));
        } else if is_substring_search {
            let escaped: String = value
                .chars()
                .flat_map(|c| {
                    if matches!(c, '\\' | '%' | '_') {
                        vec!['\\', c]
                    } else {
                        vec![c]
                    }
                })
                .collect();
            let ph = params.push(BindValue::Text(format!("%{escaped}%")));
            conditions.push(format!("{field_expr} ILIKE {ph}"));
        } else if value.is_empty() {
            // `?field=` (empty value) means "field is unset" — the common REST convention, and
            // the only sane reading here: binding `""` as `$n::uuid` (or any other typed cast)
            // fails outright at the DB with "invalid input syntax", a real 500 found live while
            // filtering an optional `Reference` field (`sprint`) for "no sprint assigned yet".
            // `field_expr IS NULL` is correct for both storage shapes — a real nullable column,
            // or `data->>'field'` on the generic table, which already yields SQL NULL for either
            // a missing JSON key or an explicit JSON null.
            conditions.push(format!("{field_expr} IS NULL"));
        } else {
            let ph = params.push(BindValue::Text(value.clone()));
            conditions.push(format!("{field_expr} = {ph}{}", field_col_type.cast_suffix()));
        }
    }

    let jql_order = if let Some(jql) = input.jql.as_deref() {
        let (where_sql, order) = crate::jql::parse_and_compile_jql(jql, entity, dedicated_table, &mut params)?;
        if let Some(where_sql) = where_sql {
            conditions.push(where_sql);
        }
        order
    } else {
        None
    };

    let mut sortable_fields: HashSet<String> = entity
        .fields
        .iter()
        .filter(|f| f.sortable.unwrap_or(false))
        .map(|f| f.name.clone())
        .collect();
    sortable_fields.insert("createdAt".to_string());
    sortable_fields.insert("updatedAt".to_string());

    // `jql`'s own `ORDER BY` (already validated sortable by `crate::jql::parse_and_compile_jql`)
    // takes priority over the plain `?sort=` param when both are present — same "explicit query
    // wins" convention Jira's own JQL vs. quick-sort dropdown follows.
    let effective_sort: Option<String> = jql_order
        .map(|(field, descending)| format!("{}{field}", if descending { "-" } else { "" }))
        .or_else(|| input.sort.clone());

    if let Some(requested) = effective_sort.as_deref() {
        if parse_sort(Some(requested), &sortable_fields).is_none() {
            tracing::debug!(
                entity = entity.name,
                requested_sort = requested,
                "sort ignored: field not sortable, falling back to entity default"
            );
        }
    }
    let resolved_sort = parse_sort(effective_sort.as_deref(), &sortable_fields)
        .or_else(|| parse_sort(list_view.and_then(|lv| lv.default_sort.as_deref()), &sortable_fields))
        .unwrap_or(ResolvedSort {
            field: "createdAt".to_string(),
            descending: true,
        });

    let sort_field_def = fields_by_name.get(resolved_sort.field.as_str());
    let (sort_expr, sort_col_type) = sort_field_expression(
        &resolved_sort.field,
        sort_field_def.copied(),
        dedicated_table,
        &mut params,
    );

    if let Some(raw_cursor) = &input.cursor {
        let cursor = decode_cursor(raw_cursor).ok_or_else(|| {
            tracing::warn!(entity = entity.name, "list rejected: cursor failed to decode");
            InvalidCursorError("Cursor does not match the current sort".to_string())
        })?;

        let cursor_dir_matches = cursor.dir
            == if resolved_sort.descending {
                SortDir::Desc
            } else {
                SortDir::Asc
            };
        if cursor.field != resolved_sort.field || !cursor_dir_matches {
            tracing::warn!(
                entity = entity.name,
                cursor_field = cursor.field,
                resolved_field = resolved_sort.field,
                "list rejected: cursor sort field/direction doesn't match the resolved sort"
            );
            return Err(InvalidCursorError("Cursor does not match the current sort".to_string()).into());
        }

        let cursor_id = Uuid::parse_str(&cursor.id).map_err(|_| {
            tracing::warn!(entity = entity.name, "list rejected: cursor id is not a valid uuid");
            InvalidCursorError("Cursor does not match the current sort".to_string())
        })?;
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
