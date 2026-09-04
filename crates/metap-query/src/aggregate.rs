//! Aggregation planner — the `GROUP BY`/`COUNT`/`SUM` counterpart to `query_planner::plan_list`.
//!
//! Why this exists: every analytics screen built on this platform (a dashboard widget, a
//! "blocked requests per day" chart, a "top attacking countries" table) needs numbers the
//! generic list API cannot produce — `plan_list` returns raw rows, so a caller wanting a count
//! either pulled every row over the wire and counted client-side, or dropped out of the platform
//! and wrote its own SQL against `records`. Both defeat the point of a metadata-driven platform:
//! the second one also loses tenant scoping and record-level permission, which is exactly the
//! kind of thing this crate exists to make impossible to forget.
//!
//! Same boundaries as `plan_list`, deliberately — this is the *only* other place list-shaped
//! client input becomes SQL, and it inherits every rule that one already enforces:
//! - tenant scope is always applied, never optional;
//! - record-level read policies (ABAC) are applied to the rows *before* they are aggregated, so
//!   a user who cannot read a row cannot learn it exists by counting it either;
//! - every field named by the caller (metric field, group-by field, time field, filter field)
//!   must come from entity metadata — an unknown or non-aggregatable field is a `400`, never
//!   interpolated into SQL. Nothing here ever takes a caller-supplied SQL operator or column.
//!
//! Field names reaching SQL text: a group-by/metric field name is validated against the entity's
//! own field list first, and a dedicated-table column name is then quoted. A field name that
//! isn't in metadata never reaches the string at all, so there is no injection path through it —
//! the same argument `query_planner::sort_field_expression` relies on.

use std::collections::HashSet;

use metap_metadata::{field_has_real_column, EntityDefinition, EntityField, FieldKind, MetadataRegistry};
use metap_permission::{PermissionService, PolicyRow, RequestContext};

use crate::condition_to_sql::record_policy_where_clause;
use crate::sql_builder::{BindValue, ParamBuilder};

/// Caller asked for something this planner refuses to turn into SQL — an unknown field, a
/// non-numeric field under `sum`, a group-by on a field nobody indexed. Downcast to a clean
/// `400` by `CrudService::aggregate`, same pattern as `InvalidCursorError`/`UnknownListViewError`.
#[derive(Debug)]
pub struct InvalidAggregateError(pub String);

impl std::fmt::Display for InvalidAggregateError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::error::Error for InvalidAggregateError {}

fn invalid(message: impl Into<String>) -> anyhow::Error {
    InvalidAggregateError(message.into()).into()
}

/// The five aggregate functions this planner will emit. Deliberately a closed enum, not a
/// caller-supplied string — "which SQL functions may run" is not client input.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AggregateFn {
    Count,
    Sum,
    Avg,
    Min,
    Max,
}

impl AggregateFn {
    fn sql_name(&self) -> &'static str {
        match self {
            AggregateFn::Count => "count",
            AggregateFn::Sum => "sum",
            AggregateFn::Avg => "avg",
            AggregateFn::Min => "min",
            AggregateFn::Max => "max",
        }
    }
}

/// One requested metric. `field` is `None` only for `Count` (`count(*)`); every other function
/// needs a field, and that field must be numeric — enforced in `plan_aggregate`, not here, so the
/// error can name the entity too.
#[derive(Debug, Clone)]
pub struct AggregateMetric {
    pub func: AggregateFn,
    pub field: Option<String>,
}

impl AggregateMetric {
    /// Parses the wire form: `"count"`, or `"<fn>:<field>"` (e.g. `"sum:bytesBlocked"`). Keeping
    /// the parse here rather than in the HTTP layer means the gRPC/GraphQL surfaces get the same
    /// spelling for free, the same way `ListInput`'s `sort`/`filters` are parsed once.
    pub fn parse(raw: &str) -> anyhow::Result<Self> {
        let (func_raw, field) = match raw.split_once(':') {
            Some((f, field)) => (f, Some(field.trim().to_string())),
            None => (raw, None),
        };
        let func = match func_raw.trim() {
            "count" => AggregateFn::Count,
            "sum" => AggregateFn::Sum,
            "avg" => AggregateFn::Avg,
            "min" => AggregateFn::Min,
            "max" => AggregateFn::Max,
            other => return Err(invalid(format!("Unknown aggregate function: {other}"))),
        };
        if func != AggregateFn::Count && field.is_none() {
            return Err(invalid(format!(
                "`{}` needs a field — write it as `{}:<field>`.",
                func.sql_name(),
                func.sql_name()
            )));
        }
        Ok(Self { func, field })
    }

    /// Key this metric appears under in each result row. `count` stays `count`; everything else
    /// is `<fn>_<field>` (`sum_bytesBlocked`) so two metrics over different fields can't collide.
    pub fn alias(&self) -> String {
        match &self.field {
            Some(field) => format!("{}_{}", self.func.sql_name(), field),
            None => self.func.sql_name().to_string(),
        }
    }
}

/// Time granularity for a time-series aggregation — maps 1:1 onto `date_trunc`'s own units, and
/// is a closed enum for the same reason `AggregateFn` is.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TimeBucket {
    Hour,
    Day,
    Week,
    Month,
}

impl TimeBucket {
    pub fn parse(raw: &str) -> anyhow::Result<Self> {
        match raw.trim() {
            "hour" => Ok(TimeBucket::Hour),
            "day" => Ok(TimeBucket::Day),
            "week" => Ok(TimeBucket::Week),
            "month" => Ok(TimeBucket::Month),
            other => Err(invalid(format!(
                "Unknown time bucket: {other} (expected hour/day/week/month)"
            ))),
        }
    }

    fn trunc_unit(&self) -> &'static str {
        match self {
            TimeBucket::Hour => "hour",
            TimeBucket::Day => "day",
            TimeBucket::Week => "week",
            TimeBucket::Month => "month",
        }
    }
}

#[derive(Debug, Clone)]
pub struct AggregateInput {
    /// At least one — an aggregation with no metric has no answer to give.
    pub metrics: Vec<AggregateMetric>,
    /// Field to group rows by (`action`, `country`, `status`, ...). `None` aggregates the whole
    /// filtered set into a single row.
    pub group_by: Option<String>,
    /// Adds a time dimension: one row per bucket (per group, when combined with `group_by`).
    pub bucket: Option<TimeBucket>,
    /// Which timestamp the bucket and the `since`/`until` window read. Defaults to `createdAt`.
    pub time_field: Option<String>,
    /// Same `(field, value)` equality shape `ListInput::filters` uses, and constrained the same
    /// way — a field must be listed in the entity's list-view `filters` to be usable here.
    pub filters: Vec<(String, String)>,
    /// Inclusive lower / exclusive upper bound on `time_field`, RFC3339. Bound as text and cast
    /// in SQL, never interpolated.
    pub since: Option<String>,
    pub until: Option<String>,
    /// Caps the number of returned groups. Clamped to `MAX_GROUPS` — an unbounded `GROUP BY` on a
    /// high-cardinality field (a source IP, say) is how an analytics endpoint becomes a way to
    /// dump a table.
    pub limit: i64,
}

impl Default for AggregateInput {
    fn default() -> Self {
        Self {
            metrics: vec![AggregateMetric {
                func: AggregateFn::Count,
                field: None,
            }],
            group_by: None,
            bucket: None,
            time_field: None,
            filters: Vec::new(),
            since: None,
            until: None,
            limit: DEFAULT_GROUPS,
        }
    }
}

/// Hard ceiling on returned groups regardless of what the caller asks for — the aggregation
/// counterpart to `EntityListView::max_limit`, which this planner has no equivalent of (a list
/// view describes columns/filters for rows, not groups).
pub const MAX_GROUPS: i64 = 500;
pub const DEFAULT_GROUPS: i64 = 100;
/// Ceiling on how many metrics one request may ask for — each becomes its own aggregate
/// expression over the same scan, so this bounds the query's width, not the number of scans.
const MAX_METRICS: usize = 10;

#[derive(Debug)]
pub struct PlannedAggregateQuery {
    /// Complete `SELECT`, ready to execute — unlike `PlannedListQuery`'s fragments, since an
    /// aggregation's projection/`GROUP BY`/`ORDER BY` are meaningless apart from each other and
    /// no caller has a reason to recombine them differently.
    pub sql: String,
    pub params: Vec<BindValue>,
}

/// Resolves one field name to the SQL expression holding its value, for either storage shape
/// (a dedicated table's real column, or the shared `records` table's JSONB path). Mirrors
/// `query_planner::sort_field_expression`; kept separate rather than shared because that one
/// also decides a cast type for keyset comparison, which means nothing here.
fn value_expression(field: &EntityField, dedicated_table: bool, params: &mut ParamBuilder) -> String {
    if dedicated_table && field_has_real_column(field) {
        return format!("\"{}\"", field.name);
    }
    let ph = params.push(BindValue::Text(field.name.clone()));
    format!("jsonb_extract_path_text(data, {ph})")
}

/// Numeric expression for `sum`/`avg`/`min`/`max`. `NULLIF(..., '')` first: on the shared table a
/// missing/blank JSON value comes back as `''`, and `''::numeric` is a hard error at the database
/// rather than the `NULL` (i.e. "not counted") every aggregate function already handles correctly.
fn numeric_expression(field: &EntityField, dedicated_table: bool, params: &mut ParamBuilder) -> String {
    let raw = value_expression(field, dedicated_table, params);
    format!("NULLIF({raw}, '')::numeric")
}

/// Timestamp expression for the bucket/window. `createdAt`/`updatedAt` are real columns on both
/// storage shapes (platform columns, not metadata fields), so they never need a metadata lookup —
/// which is also why they are accepted as a `time_field` even though no entity declares them.
fn timestamp_expression(
    field_name: &str,
    entity: &EntityDefinition,
    dedicated_table: bool,
    params: &mut ParamBuilder,
) -> anyhow::Result<String> {
    match field_name {
        "createdAt" => return Ok("created_at".to_string()),
        "updatedAt" => return Ok("updated_at".to_string()),
        _ => {}
    }
    let field = entity
        .fields
        .iter()
        .find(|f| f.name == field_name)
        .ok_or_else(|| invalid(format!("Unknown field: {field_name}")))?;
    if !matches!(field.kind, FieldKind::Date | FieldKind::Datetime) {
        return Err(invalid(format!(
            "`{field_name}` is not a date field and cannot be used as a time axis."
        )));
    }
    let raw = value_expression(field, dedicated_table, params);
    Ok(format!("NULLIF({raw}, '')::timestamptz"))
}

/// Which fields may be grouped by. Not "any field": grouping scans and sorts the whole filtered
/// set, so this is restricted to fields the entity author already marked as cheap to filter on —
/// an `indexed` field, an enum (bounded cardinality by construction), or a field the entity's
/// list view already exposes as a filter. `status` is always allowed: it is the workflow state
/// field every entity that has one groups by first, and it always has an index.
fn assert_groupable(field: &EntityField, entity: &EntityDefinition) -> anyhow::Result<()> {
    let in_list_view_filters = entity
        .list_views
        .iter()
        .any(|lv| lv.filters.iter().any(|f| f == &field.name));
    let groupable =
        field.name == "status" || field.indexed.unwrap_or(false) || field.enum_values.is_some() || in_list_view_filters;
    if !groupable {
        return Err(invalid(format!(
            "`{}` cannot be grouped by — mark it `indexed`, give it `enum_values`, or add it to a \
             list view's filters first.",
            field.name
        )));
    }
    Ok(())
}

/// Same signature shape as `plan_list` (metadata + permissions injected, record read policies
/// passed in by the caller that already loaded the snapshot) — a caller that can build one can
/// build the other with no extra wiring.
pub fn plan_aggregate(
    metadata: &MetadataRegistry,
    permissions: &PermissionService,
    entity_name: &str,
    input: &AggregateInput,
    context: &RequestContext,
    record_read_policies: &[PolicyRow],
) -> anyhow::Result<PlannedAggregateQuery> {
    let entity = metadata.get_entity(entity_name).ok_or_else(|| {
        tracing::warn!(entity = entity_name, "aggregate rejected: entity not found");
        anyhow::anyhow!("Entity not found: {entity_name}")
    })?;

    if input.metrics.is_empty() {
        return Err(invalid("At least one metric is required."));
    }
    if input.metrics.len() > MAX_METRICS {
        return Err(invalid(format!("At most {MAX_METRICS} metrics per request.")));
    }

    let tenant_id = permissions.scoped_tenant(context)?;
    let dedicated_table = entity.table_name != "records";
    let fields_by_name: std::collections::HashMap<&str, &EntityField> =
        entity.fields.iter().map(|f| (f.name.as_str(), f)).collect();

    let mut params = ParamBuilder::new();
    let mut conditions: Vec<String> = Vec::new();

    conditions.push(format!("tenant_id = {}", params.push(BindValue::Uuid(tenant_id))));
    if !dedicated_table {
        conditions.push(format!(
            "entity = {}",
            params.push(BindValue::Text(entity.name.clone()))
        ));
    }
    conditions.push("deleted = false".to_string());

    // Record-level (ABAC) read policies, applied to the rows being counted — not to the result.
    // Without this an aggregate would happily count rows the same caller is forbidden to list,
    // which is a disclosure bug, not a rounding difference.
    if let Some(record_condition) = record_policy_where_clause(record_read_policies, context, &mut params)? {
        conditions.push(record_condition);
    }

    // Same list-view-constrained filter set `plan_list` allows, and the same silent-skip
    // behavior for a field outside it — consistent with how a list request treats the same
    // query string, so a chart and the list under it can share one filter bar.
    let allowed_filter_fields: HashSet<&str> = entity
        .list_views
        .iter()
        .flat_map(|lv| lv.filters.iter().map(String::as_str))
        .collect();
    for (field_name, value) in &input.filters {
        if !allowed_filter_fields.contains(field_name.as_str()) {
            tracing::debug!(
                entity = entity.name,
                field = field_name,
                "aggregate filter ignored: not in this entity's list-view filters"
            );
            continue;
        }
        let Some(field) = fields_by_name.get(field_name.as_str()) else {
            continue;
        };
        let expr = value_expression(field, dedicated_table, &mut params);
        if value.is_empty() {
            conditions.push(format!("{expr} IS NULL"));
        } else {
            let ph = params.push(BindValue::Text(value.clone()));
            let cast = if dedicated_table
                && field_has_real_column(field)
                && matches!(field.kind, FieldKind::Reference | FieldKind::Id)
            {
                "::uuid"
            } else {
                ""
            };
            conditions.push(format!("{expr} = {ph}{cast}"));
        }
    }

    let time_field = input.time_field.as_deref().unwrap_or("createdAt");
    // Built once and reused by the bucket and the window — two `date_trunc`s over two separately
    // built expressions would push the same field's placeholder twice for no reason.
    let time_expr = if input.bucket.is_some() || input.since.is_some() || input.until.is_some() {
        Some(timestamp_expression(time_field, entity, dedicated_table, &mut params)?)
    } else {
        None
    };

    if let Some(time_expr) = &time_expr {
        if let Some(since) = &input.since {
            let ph = params.push(BindValue::Text(since.clone()));
            conditions.push(format!("{time_expr} >= {ph}::timestamptz"));
        }
        if let Some(until) = &input.until {
            let ph = params.push(BindValue::Text(until.clone()));
            conditions.push(format!("{time_expr} < {ph}::timestamptz"));
        }
    }

    // Projection: the dimensions first (bucket, group), then one column per metric. Aliases are
    // the keys each result row comes back under — `to_jsonb` over this subquery below turns the
    // whole row into JSON in the database, so the caller never has to know each column's type.
    let mut select_parts: Vec<String> = Vec::new();
    let mut group_parts: Vec<String> = Vec::new();

    if let Some(bucket) = input.bucket {
        let time_expr = time_expr.as_ref().expect("time_expr is built whenever bucket is set");
        let expr = format!("date_trunc('{}', {time_expr})", bucket.trunc_unit());
        select_parts.push(format!("{expr} AS \"bucket\""));
        group_parts.push(expr);
    }

    if let Some(group_by) = &input.group_by {
        let field = fields_by_name
            .get(group_by.as_str())
            .copied()
            .ok_or_else(|| invalid(format!("Unknown field: {group_by}")))?;
        assert_groupable(field, entity)?;
        let expr = value_expression(field, dedicated_table, &mut params);
        // Cast to text unconditionally: a dedicated table's `Reference` column is a real `uuid`,
        // and the JSONB path already yields text — one shape out means the caller gets the same
        // JSON type for a group key whichever table the entity happens to live on.
        select_parts.push(format!("({expr})::text AS \"group\""));
        group_parts.push(format!("({expr})::text"));
    }

    for metric in &input.metrics {
        let alias = metric.alias();
        let expr = match (&metric.func, &metric.field) {
            (AggregateFn::Count, None) => "count(*)".to_string(),
            (func, Some(field_name)) => {
                let field = fields_by_name
                    .get(field_name.as_str())
                    .copied()
                    .ok_or_else(|| invalid(format!("Unknown field: {field_name}")))?;
                if *func == AggregateFn::Count {
                    // `count:<field>` means "rows where this field is set" — the one aggregate
                    // that is meaningful over a non-numeric field, so it skips the numeric check.
                    format!("count({})", value_expression(field, dedicated_table, &mut params))
                } else {
                    if !matches!(field.kind, FieldKind::Number | FieldKind::Money) {
                        return Err(invalid(format!(
                            "`{}` is not a numeric field — `{}` needs Number or Money.",
                            field.name,
                            func.sql_name()
                        )));
                    }
                    format!(
                        "{}({})",
                        func.sql_name(),
                        numeric_expression(field, dedicated_table, &mut params)
                    )
                }
            }
            (func, None) => {
                return Err(invalid(format!("`{}` needs a field.", func.sql_name())));
            }
        };
        // `::float8` on every metric: `count` is `bigint` and `sum`/`avg` over numeric come back
        // as Postgres `numeric`, which `to_jsonb` renders as a JSON number either way — but the
        // float cast keeps the JSON shape identical across metrics so a client charting library
        // never sees a string for one series and a number for another.
        select_parts.push(format!("({expr})::float8 AS \"{alias}\""));
    }

    let limit = input.limit.clamp(1, MAX_GROUPS);

    let group_by_sql = if group_parts.is_empty() {
        String::new()
    } else {
        format!(" GROUP BY {}", group_parts.join(", "))
    };

    // Ordering: chronological when there's a time axis (a chart reads left to right), otherwise
    // biggest-first on the first metric (a "top N" table reads top-down). Neither is caller
    // input — an `ORDER BY` this planner didn't build is exactly the kind of string this crate
    // exists to keep out of SQL.
    let order_by_sql = if input.bucket.is_some() {
        " ORDER BY \"bucket\" ASC".to_string()
    } else if input.group_by.is_some() {
        format!(" ORDER BY \"{}\" DESC NULLS LAST", input.metrics[0].alias())
    } else {
        String::new()
    };

    let table = &entity.table_name;
    let where_sql = conditions.join(" AND ");
    let inner = format!(
        "SELECT {} FROM {table} WHERE {where_sql}{group_by_sql}{order_by_sql} LIMIT {limit}",
        select_parts.join(", ")
    );
    // `to_jsonb(agg)` — one JSON object per result row, built in the database. The alternative
    // (reading dynamically-typed columns back through sqlx) needs the caller to know each
    // column's Postgres type at compile time, which is precisely what a metadata-driven,
    // caller-shaped projection cannot provide.
    let sql = format!("SELECT to_jsonb(agg) AS row FROM ({inner}) agg");

    Ok(PlannedAggregateQuery {
        sql,
        params: params.params,
    })
}

#[cfg(test)]
mod tests;
