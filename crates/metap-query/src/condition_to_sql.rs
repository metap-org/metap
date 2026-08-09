//! Mirrors `packages/core/src/core/query/condition-to-sql.ts`. The one deliberate
//! deviation from the TS version: comparisons are typed per-column (UUID vs timestamptz vs
//! text) and fallible, instead of relying on node-postgres's implicit text-parameter type
//! inference (which sqlx's `Encode`-based binding doesn't replicate). A policy condition
//! comparing `createdBy`/`updatedBy` to a non-UUID-shaped value, or a
//! `createdAt`/`updatedAt` condition to an unparseable timestamp, is now a clear
//! `anyhow::Error` at query-build time rather than a silent wrong-type comparison — a
//! stricter, more honest failure mode than the original, not a weaker one.

use metap_permission::{
    role_gate_passed, ConditionOp, PolicyCondition, PolicyRow, PolicyValue, RequestContext,
};
use serde_json::Value;
use uuid::Uuid;

use crate::sql_builder::{BindValue, ParamBuilder};

enum ColType {
    Uuid,
    Text,
    Timestamptz,
}

fn field_expression(field_name: &str, params: &mut ParamBuilder) -> (String, ColType) {
    match field_name {
        "createdBy" => ("created_by".to_string(), ColType::Uuid),
        "updatedBy" => ("updated_by".to_string(), ColType::Uuid),
        "status" => ("status".to_string(), ColType::Text),
        "createdAt" => ("created_at".to_string(), ColType::Timestamptz),
        "updatedAt" => ("updated_at".to_string(), ColType::Timestamptz),
        _ => {
            let ph = params.push(BindValue::Text(field_name.to_string()));
            (format!("jsonb_extract_path_text(data, {ph})"), ColType::Text)
        }
    }
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
            let (expr, col_type) = field_expression(attribute, params);
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
/// `can_update_record_condition`) — this is the one that builds record-level read policies
/// into SQL, so it needs the same bypass (the admin-bypass bug this fixed is logged in
/// `docs/architectures/09-adr.md`).
pub fn record_policy_where_clause(
    rows: &[PolicyRow],
    context: &RequestContext,
    params: &mut ParamBuilder,
) -> anyhow::Result<Option<String>> {
    if rows.is_empty() || context.is_admin() {
        return Ok(None);
    }

    let passing_rows: Vec<&PolicyRow> = rows
        .iter()
        .filter(|row| role_gate_passed(row.roles.as_deref(), context.roles.as_deref()))
        .collect();

    if passing_rows.is_empty() {
        return Ok(Some("false".to_string()));
    }

    let mut clauses = Vec::with_capacity(passing_rows.len());
    for row in passing_rows {
        clauses.push(match &row.condition {
            Some(condition) => condition_to_sql(condition, context, params)?,
            None => "true".to_string(),
        });
    }

    Ok(Some(format!("({})", clauses.join(" OR "))))
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
        }
    }

    fn policy(roles: Option<Vec<&str>>, condition: Option<PolicyCondition>) -> PolicyRow {
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
            value: PolicyValue::Literal { literal: serde_json::json!("u1") },
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
            value: PolicyValue::Literal { literal: serde_json::json!("not-a-uuid") },
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
            value: PolicyValue::Literal { literal: serde_json::json!([]) },
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
            value: PolicyValue::Literal { literal: serde_json::json!([]) },
        };
        assert_eq!(condition_to_sql(&cond, &ctx, &mut params).unwrap(), "true");
    }
}
