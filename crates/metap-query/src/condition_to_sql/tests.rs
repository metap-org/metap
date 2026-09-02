use super::*;
use metap_permission::PolicySubject;

fn context(roles: Option<Vec<&str>>) -> RequestContext {
    RequestContext {
        tenant_id: Uuid::new_v4().to_string(),
        user_id: Some("u1".to_string()),
        roles: roles.map(|r| r.into_iter().map(String::from).collect()),
        function_id: None,
        context_attributes: None,
        forwarded_bearer_token: None,
    }
}

fn policy(roles: Option<Vec<&str>>, condition: Option<PolicyCondition>) -> PolicyRow {
    policy_with_effect(roles, condition, PolicyEffect::Allow)
}

fn policy_with_effect(roles: Option<Vec<&str>>, condition: Option<PolicyCondition>, effect: PolicyEffect) -> PolicyRow {
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

/// Regression for the finding in `AUDIT_2.md`: comparing a JSONB-extracted-as-text field
/// against a JSON *number* with a plain text `>` would sort lexicographically (`"9" >
/// "10"`) — wrong. Confirms `Gt` against a numeric literal casts both sides to `::numeric`.
#[test]
fn gt_condition_against_a_number_casts_both_sides_numeric() {
    let ctx = context(None);
    let mut params = ParamBuilder::new();
    let cond = PolicyCondition::Attribute {
        attribute: "amount".to_string(),
        op: ConditionOp::Gt,
        value: PolicyValue::Literal {
            literal: serde_json::json!(10000),
        },
    };
    let sql = condition_to_sql(&cond, &ctx, &mut params).unwrap();
    assert_eq!(sql, "(jsonb_extract_path_text(data, $1))::numeric > $2::numeric");
}

#[test]
fn gt_condition_against_a_string_stays_a_plain_text_comparison() {
    let ctx = context(None);
    let mut params = ParamBuilder::new();
    let cond = PolicyCondition::Attribute {
        attribute: "name".to_string(),
        op: ConditionOp::Lt,
        value: PolicyValue::Literal {
            literal: serde_json::json!("banana"),
        },
    };
    let sql = condition_to_sql(&cond, &ctx, &mut params).unwrap();
    assert_eq!(sql, "jsonb_extract_path_text(data, $1) < $2");
}

#[test]
fn gt_condition_on_a_uuid_field_is_rejected() {
    let ctx = context(None);
    let mut params = ParamBuilder::new();
    let cond = PolicyCondition::Attribute {
        attribute: "createdBy".to_string(),
        op: ConditionOp::Gt,
        value: PolicyValue::Literal {
            literal: serde_json::json!("123e4567-e89b-12d3-a456-426614174000"),
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
