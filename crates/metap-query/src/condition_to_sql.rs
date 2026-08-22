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
mod tests {
    use super::*;
    use metap_permission::PolicySubject;

    fn context(roles: Option<Vec<&str>>) -> RequestContext {
        RequestContext {
            tenant_id: Uuid::new_v4().to_string(),
            user_id: Some("u1".to_string()),
            roles: roles.map(|r| r.into_iter().map(String::from).collect()),
            function_id: None,
            context_attributes: None,
        }
    }

    fn policy(roles: Option<Vec<&str>>, condition: Option<PolicyCondition>) -> PolicyRow {
        policy_with_effect(roles, condition, PolicyEffect::Allow)
    }

    fn policy_with_effect(
        roles: Option<Vec<&str>>,
        condition: Option<PolicyCondition>,
        effect: PolicyEffect,
    ) -> PolicyRow {
        PolicyRow {
            id: Uuid::new_v4(),
            tenant_id: Uuid::new_v4(),
            entity: "crm.customers".to_string(),
            action: "read".to_string(),
            field: None,
            subject: PolicySubject::Record.as_str().to_string(),
            roles: roles.map(|r| r.into_iter().map(String::from).collect()),
            condition,
            created_by: None,
            effect,
        }
    }

    #[test]
    fn admin_bypasses_record_policy_where_clause() {
        let ctx = context(Some(vec!["admin"]));
        let mut params = ParamBuilder::new();
        let rows = vec![policy(None, None)];
        let clause = record_policy_where_clause(&rows, &ctx, &mut params).unwrap();
        assert_eq!(clause, None);
    }

    #[test]
    fn no_rows_means_no_restriction() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let clause = record_policy_where_clause(&[], &ctx, &mut params).unwrap();
        assert_eq!(clause, None);
    }

    #[test]
    fn role_gate_failure_yields_false() {
        let ctx = context(Some(vec!["sales"]));
        let mut params = ParamBuilder::new();
        let rows = vec![policy(Some(vec!["ops"]), None)];
        let clause = record_policy_where_clause(&rows, &ctx, &mut params).unwrap();
        assert_eq!(clause, Some("false".to_string()));
    }

    #[test]
    fn eq_condition_on_jsonb_field_produces_expected_sql_shape() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let cond = PolicyCondition::Attribute {
            attribute: "ownerId".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal {
                literal: serde_json::json!("u1"),
            },
        };
        let sql = condition_to_sql(&cond, &ctx, &mut params).unwrap();
        assert_eq!(sql, "jsonb_extract_path_text(data, $1) = $2");
        assert_eq!(params.params.len(), 2);
    }

    #[test]
    fn uuid_field_binds_natively_when_value_parses_as_uuid() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let cond = PolicyCondition::Attribute {
            attribute: "createdBy".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal {
                literal: serde_json::json!("123e4567-e89b-12d3-a456-426614174000"),
            },
        };
        let sql = condition_to_sql(&cond, &ctx, &mut params).unwrap();
        assert_eq!(sql, "created_by = $1");
        assert!(matches!(params.params[0], BindValue::Uuid(_)));
    }

    #[test]
    fn uuid_field_with_non_uuid_value_is_a_clear_error() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let cond = PolicyCondition::Attribute {
            attribute: "createdBy".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal {
                literal: serde_json::json!("not-a-uuid"),
            },
        };
        assert!(condition_to_sql(&cond, &ctx, &mut params).is_err());
    }

    #[test]
    fn empty_in_list_is_always_false() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let cond = PolicyCondition::Attribute {
            attribute: "status".to_string(),
            op: ConditionOp::In,
            value: PolicyValue::Literal {
                literal: serde_json::json!([]),
            },
        };
        assert_eq!(condition_to_sql(&cond, &ctx, &mut params).unwrap(), "false");
    }

    #[test]
    fn empty_not_in_list_is_always_true() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let cond = PolicyCondition::Attribute {
            attribute: "status".to_string(),
            op: ConditionOp::NotIn,
            value: PolicyValue::Literal {
                literal: serde_json::json!([]),
            },
        };
        assert_eq!(condition_to_sql(&cond, &ctx, &mut params).unwrap(), "true");
    }

    #[test]
    fn dotted_cross_record_attribute_is_a_clear_error_not_a_silent_no_op() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let cond = PolicyCondition::Attribute {
            attribute: "project.ownerId".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal {
                literal: serde_json::json!("u1"),
            },
        };
        let err = condition_to_sql(&cond, &ctx, &mut params).unwrap_err();
        assert!(err.to_string().contains("cross-record"), "unexpected error: {err}");
        // Typed, not just a message match — `CrudService::list()` downcasts on this exact type
        // (found missing in code review, 2026-08-22) to give it its own error code instead of
        // falling through to a generic, indistinguishable `internal_error`.
        assert!(
            err.downcast_ref::<CrossRecordConditionInListError>().is_some(),
            "expected a CrossRecordConditionInListError, got: {err:?}"
        );
    }

    #[test]
    fn deny_row_narrows_the_where_clause_with_and_not_instead_of_widening_it_via_or() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let allow = policy(None, None); // unconditional allow
        let deny = policy_with_effect(
            None,
            Some(PolicyCondition::Attribute {
                attribute: "status".to_string(),
                op: ConditionOp::Eq,
                value: PolicyValue::Literal {
                    literal: serde_json::json!("archived"),
                },
            }),
            PolicyEffect::Deny,
        );
        let clause = record_policy_where_clause(&[allow, deny], &ctx, &mut params).unwrap();
        let sql = clause.unwrap();
        assert!(sql.contains("AND NOT"), "expected AND NOT, got: {sql}");
        assert!(!sql.contains(" OR status"), "deny condition must not be OR'd in: {sql}");
    }

    #[test]
    fn deny_only_rows_with_no_allow_still_yield_false_not_unrestricted() {
        let ctx = context(None);
        let mut params = ParamBuilder::new();
        let deny_only = policy_with_effect(None, None, PolicyEffect::Deny);
        let clause = record_policy_where_clause(&[deny_only], &ctx, &mut params).unwrap();
        assert_eq!(clause, Some("false".to_string()));
    }
}
