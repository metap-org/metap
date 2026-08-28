//! Mirrors `packages/core/src/core/query/condition-to-sql.ts`. The one deliberate
//! deviation from the TS version: comparisons are typed per-column (UUID vs timestamptz vs
//! text) and fallible, instead of relying on node-postgres's implicit text-parameter type
//! inference (which sqlx's `Encode`-based binding doesn't replicate). A policy condition
//! comparing `createdBy`/`updatedBy` to a non-UUID-shaped value, or a
//! `createdAt`/`updatedAt` condition to an unparseable timestamp, is now a clear
//! `anyhow::Error` at query-build time rather than a silent wrong-type comparison — a
//! stricter, more honest failure mode than the original, not a weaker one.

use metap_permission::{
    role_gate_passed, ConditionOp, PolicyCondition, PolicyEffect, PolicyRow, PolicyValue, RequestContext,
};
use serde_json::Value;
use uuid::Uuid;

use crate::sql_builder::{BindValue, ParamBuilder};

enum ColType {
    Uuid,
    Text,
    Timestamptz,
}

/// A record-level policy's condition uses a dotted cross-record attribute path (e.g.
/// `"project.ownerId"`) that `list()` can't resolve (see `field_expression`'s doc comment).
/// Deterministic and permanent for as long as the policy stays configured this way — every
/// `list()` call against the entity/action it applies to fails identically, not a transient
/// error — so `CrudService::list()` downcasts for this specifically (found in code review,
/// 2026-08-22: it used to fall through to a generic, indistinguishable-from-any-other-bug
/// `500`), same pattern as `InvalidCursorError`/`UnknownListViewError`.
#[derive(Debug)]
pub struct CrossRecordConditionInListError(pub String);

impl std::fmt::Display for CrossRecordConditionInListError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::error::Error for CrossRecordConditionInListError {}

/// A dotted attribute path (`"project.ownerId"`) names a cross-record condition —
/// `metap-permission`'s `required_relation_fields`/`CrudService`'s enrichment resolve those by
/// fetching the related record and merging it onto an already-fetched subject, which only
/// single-record operations (get/update/delete/transition) do. `list()`'s SQL-generation path
/// has no equivalent (would need `QueryPlanner` JOIN support, not built), so treating the whole
/// path as one literal JSONB key here would silently look up a key that can never exist —
/// `jsonb_extract_path_text(data, 'project.ownerId')` returns SQL `NULL`, which makes an `eq`
/// condition simply never match and a `deny` condition never fire, wrong in the *unsafe*
/// direction (a policy author's deny quietly does nothing). Reject it explicitly instead,
/// matching this file's existing "clear error over silent wrong-type comparison" convention
/// (see the top doc comment).
fn field_expression(field_name: &str, params: &mut ParamBuilder) -> anyhow::Result<(String, ColType)> {
    if field_name.contains('.') {
        return Err(CrossRecordConditionInListError(format!(
            "policy condition attribute {field_name:?} is a cross-record path, not supported in \
             list() queries — cross-record conditions only apply to single-record operations \
             (get/update/delete/transition)"
        ))
        .into());
    }
    Ok(match field_name {
        "createdBy" => ("created_by".to_string(), ColType::Uuid),
        "updatedBy" => ("updated_by".to_string(), ColType::Uuid),
        "status" => ("status".to_string(), ColType::Text),
        "createdAt" => ("created_at".to_string(), ColType::Timestamptz),
        "updatedAt" => ("updated_at".to_string(), ColType::Timestamptz),
        _ => {
            let ph = params.push(BindValue::Text(field_name.to_string()));
            (format!("jsonb_extract_path_text(data, {ph})"), ColType::Text)
        }
    })
}

fn resolve_value(value: &PolicyValue, context: &RequestContext) -> Value {
    match value {
        PolicyValue::Literal { literal } => literal.clone(),
        PolicyValue::FromContext { from_context } => {
            context.to_value().get(from_context).cloned().unwrap_or(Value::Null)
        }
    }
}

fn json_scalar_to_text(value: &Value) -> anyhow::Result<String> {
    match value {
        Value::String(s) => Ok(s.clone()),
        Value::Bool(b) => Ok(b.to_string()),
        Value::Number(n) => Ok(n.to_string()),
        _ => anyhow::bail!("policy condition value must be a string, bool, or number, got {value}"),
    }
}

fn bind_scalar(col_type: &ColType, value: &Value, params: &mut ParamBuilder) -> anyhow::Result<String> {
    match col_type {
        ColType::Uuid => {
            let s = value
                .as_str()
                .ok_or_else(|| anyhow::anyhow!("expected a UUID string, got {value}"))?;
            let uuid = Uuid::parse_str(s)?;
            Ok(params.push(BindValue::Uuid(uuid)))
        }
        ColType::Timestamptz => {
            let text = json_scalar_to_text(value)?;
            Ok(format!("{}::timestamptz", params.push(BindValue::Text(text))))
        }
        ColType::Text => Ok(params.push(BindValue::Text(json_scalar_to_text(value)?))),
    }
}

/// Builds the `(lhs, rhs)` pair for a `Gt`/`Gte`/`Lt`/`Lte` comparison — unlike `Eq`/`In`, ordering
/// needs a real numeric cast when comparing against a JSON *number* (a bare text comparison would
/// sort `"9"` after `"10"`), so the cast is chosen from the *value being compared against* rather
/// than a fixed column type — `condition_to_sql` has no entity-field-kind context to draw on the
/// way `metap-query::query_planner`'s `sort_field_expression` does. A `Timestamptz` field always
/// casts the bound side to match its own real column type; a `Uuid` field has no meaningful
/// ordering for this use case and is rejected outright rather than silently comparing byte order.
fn comparable_expr_and_value(
    expr: &str,
    col_type: &ColType,
    expected: &Value,
    params: &mut ParamBuilder,
) -> anyhow::Result<(String, String)> {
    match col_type {
        ColType::Uuid => anyhow::bail!("gt/gte/lt/lte is not supported on a UUID-typed field"),
        ColType::Timestamptz => {
            let text = json_scalar_to_text(expected)?;
            Ok((
                expr.to_string(),
                format!("{}::timestamptz", params.push(BindValue::Text(text))),
            ))
        }
        ColType::Text => match expected {
            Value::Number(_) => {
                let text = json_scalar_to_text(expected)?;
                Ok((
                    format!("({expr})::numeric"),
                    format!("{}::numeric", params.push(BindValue::Text(text))),
                ))
            }
            _ => Ok((expr.to_string(), bind_scalar(col_type, expected, params)?)),
        },
    }
}

pub fn condition_to_sql(
    condition: &PolicyCondition,
    context: &RequestContext,
    params: &mut ParamBuilder,
) -> anyhow::Result<String> {
    match condition {
        PolicyCondition::All { all } => {
            if all.is_empty() {
                return Ok("true".to_string());
            }
            let mut clauses = Vec::with_capacity(all.len());
            for inner in all {
                clauses.push(condition_to_sql(inner, context, params)?);
            }
            Ok(format!("({})", clauses.join(" AND ")))
        }
        PolicyCondition::Any { any } => {
            if any.is_empty() {
                return Ok("false".to_string());
            }
            let mut clauses = Vec::with_capacity(any.len());
            for inner in any {
                clauses.push(condition_to_sql(inner, context, params)?);
            }
            Ok(format!("({})", clauses.join(" OR ")))
        }
        PolicyCondition::Attribute { attribute, op, value } => {
            let (expr, col_type) = field_expression(attribute, params)?;
            let expected = resolve_value(value, context);

            match op {
                ConditionOp::Eq => {
                    let ph = bind_scalar(&col_type, &expected, params)?;
                    Ok(format!("{expr} = {ph}"))
                }
                ConditionOp::Neq => {
                    let ph = bind_scalar(&col_type, &expected, params)?;
                    Ok(format!("{expr} != {ph}"))
                }
                ConditionOp::In => {
                    let values = expected.as_array().cloned().unwrap_or_default();
                    if values.is_empty() {
                        return Ok("false".to_string());
                    }
                    let mut phs = Vec::with_capacity(values.len());
                    for v in &values {
                        phs.push(bind_scalar(&col_type, v, params)?);
                    }
                    Ok(format!("{expr} IN ({})", phs.join(", ")))
                }
                ConditionOp::NotIn => {
                    let values = expected.as_array().cloned().unwrap_or_default();
                    if values.is_empty() {
                        return Ok("true".to_string());
                    }
                    let mut phs = Vec::with_capacity(values.len());
                    for v in &values {
                        phs.push(bind_scalar(&col_type, v, params)?);
                    }
                    Ok(format!("{expr} NOT IN ({})", phs.join(", ")))
                }
                ConditionOp::Gt | ConditionOp::Gte | ConditionOp::Lt | ConditionOp::Lte => {
                    let (lhs, rhs) = comparable_expr_and_value(&expr, &col_type, &expected, params)?;
                    let sql_op = match op {
                        ConditionOp::Gt => ">",
                        ConditionOp::Gte => ">=",
                        ConditionOp::Lt => "<",
                        ConditionOp::Lte => "<=",
                        _ => unreachable!(),
                    };
                    Ok(format!("{lhs} {sql_op} {rhs}"))
                }
            }
        }
    }
}

/// Every other permission-decision entry point in this codebase bypasses policy evaluation
/// for admin (see `PermissionSnapshot`'s `filter_readable_fields`/`assert_writable_fields`/
/// `can_perform_record_condition`) — this is the one that builds record-level read policies
/// into SQL, so it needs the same bypass.
///
/// Deny-overrides-allow (`PolicyEffect`, `docs/roadmap.md`'s permission-review findings,
/// 2026-08-21) needs its own handling here, separate from `metap_permission::evaluate_policies`
/// — that helper evaluates one already-fetched record; this one has to build a WHERE clause
/// that filters *many* rows in the database without fetching them first, so `deny`/`allow`
/// conditions both become SQL fragments: `(allow1 OR allow2 OR ...) AND NOT (deny1 OR deny2 OR
/// ...)`, not the "either effect ORs together" bug this file had before deny existed (folding a
/// matching `deny` row into the same `OR` as `allow` rows would have *widened* access instead
/// of narrowing it).
pub fn record_policy_where_clause(
    rows: &[PolicyRow],
    context: &RequestContext,
    params: &mut ParamBuilder,
) -> anyhow::Result<Option<String>> {
    if rows.is_empty() || context.is_admin() {
        return Ok(None);
    }

    let matching_rows: Vec<&PolicyRow> = rows
        .iter()
        .filter(|row| role_gate_passed(row.roles.as_deref(), context.roles.as_deref()))
        .collect();

    let allow_rows: Vec<&&PolicyRow> = matching_rows
        .iter()
        .filter(|r| r.effect == PolicyEffect::Allow)
        .collect();
    if allow_rows.is_empty() {
        // Same semantics this function always had for "nothing matched": the mere existence of
        // record-level policies for this entity/action switches from "unrestricted" to "must
        // match an allow to be visible" — a deny-only match still grants nothing.
        return Ok(Some("false".to_string()));
    }

    let mut allow_clauses = Vec::with_capacity(allow_rows.len());
    for row in &allow_rows {
        allow_clauses.push(match &row.condition {
            Some(condition) => condition_to_sql(condition, context, params)?,
            None => "true".to_string(),
        });
    }
    let allow_sql = format!("({})", allow_clauses.join(" OR "));

    let deny_rows: Vec<&&PolicyRow> = matching_rows
        .iter()
        .filter(|r| r.effect == PolicyEffect::Deny)
        .collect();
    if deny_rows.is_empty() {
        return Ok(Some(allow_sql));
    }

    let mut deny_clauses = Vec::with_capacity(deny_rows.len());
    for row in &deny_rows {
        deny_clauses.push(match &row.condition {
            Some(condition) => condition_to_sql(condition, context, params)?,
            None => "true".to_string(),
        });
    }
    let deny_sql = format!("({})", deny_clauses.join(" OR "));

    Ok(Some(format!("({allow_sql} AND NOT {deny_sql})")))
}

#[cfg(test)]
mod tests;
