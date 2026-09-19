//! Mirrors `packages/core/src/core/permission/policy-condition.ts` field-for-field: same
//! operators, same role-gate/condition-gate evaluation order, same "record" vs "context"
//! subject selection.
//!
//! Split into `types` (the declarative `PolicyValue`/`ConditionOp`/`PolicyCondition` shapes),
//! `resolve` (resolving a value/attribute path against a subject), and `evaluate` (the actual
//! condition/policy-row/policy-set matching) purely to keep each file a manageable size — every
//! item this module used to export directly is re-exported here unchanged.

mod evaluate;
mod resolve;
mod types;

pub use evaluate::{
    evaluate_condition, evaluate_policies, evaluate_policy_row, role_gate_passed, ConditionResult, PolicyVerdict,
};
pub use resolve::{required_relation_fields, resolve_value};
pub use types::{ConditionOp, PolicyCondition, PolicyValue};

#[cfg(test)]
mod tests {
    use super::*;
    use crate::context::RequestContext;
    use crate::policy_store::{PolicyEffect, PolicyRow};
    use serde_json::json;
    use uuid::Uuid;

    fn context(tenant_id: &str, roles: Option<Vec<&str>>) -> RequestContext {
        RequestContext {
            tenant_id: tenant_id.to_string(),
            user_id: None,
            roles: roles.map(|r| r.into_iter().map(String::from).collect()),
            function_id: None,
            context_attributes: None,
            forwarded_bearer_token: None,
        }
    }

    fn policy(roles: Option<Vec<&str>>, condition: Option<PolicyCondition>, subject: &str) -> PolicyRow {
        policy_with_effect(roles, condition, subject, PolicyEffect::Allow)
    }

    fn policy_with_effect(
        roles: Option<Vec<&str>>,
        condition: Option<PolicyCondition>,
        subject: &str,
        effect: PolicyEffect,
    ) -> PolicyRow {
        PolicyRow {
            id: Uuid::new_v4(),
            tenant_id: Uuid::new_v4(),
            entity: "crm.customers".to_string(),
            action: "read".to_string(),
            field: None,
            subject: subject.to_string(),
            roles: roles.map(|r| r.into_iter().map(String::from).collect()),
            condition,
            created_by: None,
            effect,
        }
    }

    #[test]
    fn role_gate_open_when_no_roles_declared() {
        assert!(role_gate_passed(None, Some(&["sales".to_string()])));
        assert!(role_gate_passed(Some(&[]), None));
    }

    #[test]
    fn role_gate_requires_overlap() {
        let policy_roles = vec!["sales".to_string(), "support".to_string()];
        assert!(role_gate_passed(Some(&policy_roles), Some(&["support".to_string()])));
        assert!(!role_gate_passed(Some(&policy_roles), Some(&["ops".to_string()])));
        assert!(!role_gate_passed(Some(&policy_roles), None));
    }

    #[test]
    fn eq_and_neq_operators() {
        let ctx = context("t1", None);
        let subject = json!({ "status": "active" });

        let eq_cond = PolicyCondition::Attribute {
            attribute: "status".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal {
                literal: json!("active"),
            },
        };
        assert!(evaluate_condition(&eq_cond, &subject, &ctx).is_passed());

        let neq_cond = PolicyCondition::Attribute {
            attribute: "status".to_string(),
            op: ConditionOp::Neq,
            value: PolicyValue::Literal {
                literal: json!("active"),
            },
        };
        assert!(!evaluate_condition(&neq_cond, &subject, &ctx).is_passed());
    }

    #[test]
    fn in_and_not_in_operators() {
        let ctx = context("t1", None);
        let subject = json!({ "region": "eu" });

        let in_cond = PolicyCondition::Attribute {
            attribute: "region".to_string(),
            op: ConditionOp::In,
            value: PolicyValue::Literal {
                literal: json!(["eu", "us"]),
            },
        };
        assert!(evaluate_condition(&in_cond, &subject, &ctx).is_passed());

        let not_in_cond = PolicyCondition::Attribute {
            attribute: "region".to_string(),
            op: ConditionOp::NotIn,
            value: PolicyValue::Literal {
                literal: json!(["apac"]),
            },
        };
        assert!(evaluate_condition(&not_in_cond, &subject, &ctx).is_passed());
    }

    #[test]
    fn contains_and_not_contains_operators() {
        let ctx = context("t1", None);
        let subject = json!({ "tags": ["urgent", "billing"] });

        let contains_cond = PolicyCondition::Attribute {
            attribute: "tags".to_string(),
            op: ConditionOp::Contains,
            value: PolicyValue::Literal {
                literal: json!("urgent"),
            },
        };
        assert!(evaluate_condition(&contains_cond, &subject, &ctx).is_passed());

        let not_contains_cond = PolicyCondition::Attribute {
            attribute: "tags".to_string(),
            op: ConditionOp::NotContains,
            value: PolicyValue::Literal {
                literal: json!("archived"),
            },
        };
        assert!(evaluate_condition(&not_contains_cond, &subject, &ctx).is_passed());

        let missing_cond = PolicyCondition::Attribute {
            attribute: "tags".to_string(),
            op: ConditionOp::Contains,
            value: PolicyValue::Literal {
                literal: json!("archived"),
            },
        };
        assert!(!evaluate_condition(&missing_cond, &subject, &ctx).is_passed());
    }

    #[test]
    fn contains_fails_closed_when_the_attribute_is_not_an_array() {
        let ctx = context("t1", None);
        let subject = json!({ "tags": "urgent" });
        let cond = PolicyCondition::Attribute {
            attribute: "tags".to_string(),
            op: ConditionOp::Contains,
            value: PolicyValue::Literal {
                literal: json!("urgent"),
            },
        };
        // A single string is not a JSON array, even though it "contains" the substring
        // textually — same "fail closed on a type mismatch" posture `Gt`/`Gte`/`Lt`/`Lte` take.
        assert!(!evaluate_condition(&cond, &subject, &ctx).is_passed());
    }

    /// The motivating real-world case for `Contains`/`NotContains`: a tenant gating access by an
    /// OAuth2 grant's scope. `metap-http::auth`'s `AuthContext` folds a `client_credentials`/
    /// `authorization_code` token's granted scope into `RequestContext.context_attributes` as
    /// `oauthScope` (always a JSON array, even for a single scope token) — a `context`-subject
    /// policy condition resolves `attribute` against `context.to_value()` directly (see
    /// `permission_snapshot::match_stability`'s doc comment for why `subject == "context"` passes
    /// the context itself, not a record, as the subject), so this is exactly what a tenant would
    /// write to require `"read:widgets"` before a context-subject allow policy grants anything.
    #[test]
    fn oauth_scope_gating_via_context_subject_and_contains() {
        let ctx_with_scope = RequestContext {
            tenant_id: "t1".to_string(),
            user_id: None,
            roles: None,
            function_id: None,
            context_attributes: Some(
                json!({ "oauthScope": ["read:widgets", "write:widgets"] })
                    .as_object()
                    .unwrap()
                    .clone(),
            ),
            forwarded_bearer_token: None,
        };
        let ctx_without_scope = RequestContext {
            context_attributes: None,
            ..ctx_with_scope.clone()
        };

        let requires_read_scope = PolicyCondition::Attribute {
            attribute: "oauthScope".to_string(),
            op: ConditionOp::Contains,
            value: PolicyValue::Literal {
                literal: json!("read:widgets"),
            },
        };

        // `subject == "context"` policies evaluate against `context.to_value()`, not a record —
        // an empty object subject here proves the condition never touches it.
        assert!(evaluate_condition(&requires_read_scope, &ctx_with_scope.to_value(), &ctx_with_scope).is_passed());
        assert!(
            !evaluate_condition(&requires_read_scope, &ctx_without_scope.to_value(), &ctx_without_scope).is_passed(),
            "an ordinary session token (no oauthScope claim at all) must not satisfy a scope requirement"
        );
    }

    /// Regression for the finding in `AUDIT_2.md`: before `Gt`/`Gte`/`Lt`/`Lte` existed, a guard
    /// like "amount > 10000" was inexpressible — `journal_entry_entity.rs`'s `post` guard had to
    /// fake "at least one side is positive" with `Neq 0`, which wrongly also accepted a negative
    /// amount. Confirms `Gt` correctly rejects a negative value that `Neq 0` would have wrongly
    /// let through.
    #[test]
    fn gt_correctly_excludes_a_negative_value_that_neq_zero_would_wrongly_accept() {
        let ctx = context("t1", None);
        let negative_subject = json!({ "debitAmount": -50 });
        let positive_subject = json!({ "debitAmount": 50 });

        let gt_zero = PolicyCondition::Attribute {
            attribute: "debitAmount".to_string(),
            op: ConditionOp::Gt,
            value: PolicyValue::Literal { literal: json!(0) },
        };
        assert!(
            !evaluate_condition(&gt_zero, &negative_subject, &ctx).is_passed(),
            "a negative amount must not satisfy 'debitAmount > 0'"
        );
        assert!(evaluate_condition(&gt_zero, &positive_subject, &ctx).is_passed());

        // The bug this replaces: `Neq 0` treats -50 as satisfying "non-zero".
        let neq_zero = PolicyCondition::Attribute {
            attribute: "debitAmount".to_string(),
            op: ConditionOp::Neq,
            value: PolicyValue::Literal { literal: json!(0) },
        };
        assert!(
            evaluate_condition(&neq_zero, &negative_subject, &ctx).is_passed(),
            "demonstrates why Neq 0 was the wrong operator for this guard"
        );
    }

    #[test]
    fn ordering_operators_cover_numbers_and_strings() {
        let ctx = context("t1", None);

        let gte_cond = |v: i64| PolicyCondition::Attribute {
            attribute: "amount".to_string(),
            op: ConditionOp::Gte,
            value: PolicyValue::Literal { literal: json!(v) },
        };
        assert!(evaluate_condition(&gte_cond(100), &json!({"amount": 100}), &ctx).is_passed());
        assert!(!evaluate_condition(&gte_cond(101), &json!({"amount": 100}), &ctx).is_passed());

        let lte_cond = |v: i64| PolicyCondition::Attribute {
            attribute: "amount".to_string(),
            op: ConditionOp::Lte,
            value: PolicyValue::Literal { literal: json!(v) },
        };
        assert!(evaluate_condition(&lte_cond(100), &json!({"amount": 100}), &ctx).is_passed());
        assert!(!evaluate_condition(&lte_cond(99), &json!({"amount": 100}), &ctx).is_passed());

        let lt_cond = PolicyCondition::Attribute {
            attribute: "name".to_string(),
            op: ConditionOp::Lt,
            value: PolicyValue::Literal {
                literal: json!("banana"),
            },
        };
        assert!(evaluate_condition(&lt_cond, &json!({"name": "apple"}), &ctx).is_passed());
        assert!(!evaluate_condition(&lt_cond, &json!({"name": "cherry"}), &ctx).is_passed());
    }

    #[test]
    fn ordering_operator_with_mismatched_types_fails_closed() {
        let ctx = context("t1", None);
        let cond = PolicyCondition::Attribute {
            attribute: "amount".to_string(),
            op: ConditionOp::Gt,
            value: PolicyValue::Literal { literal: json!(10) },
        };
        // `amount` is a string on this subject, not a number — not comparable, must fail closed.
        assert!(!evaluate_condition(&cond, &json!({"amount": "not-a-number"}), &ctx).is_passed());
    }

    #[test]
    fn from_context_resolves_against_caller_context() {
        let ctx = context("tenant-42", None);
        let subject = json!({ "tenantId": "tenant-42" });
        let cond = PolicyCondition::Attribute {
            attribute: "tenantId".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::FromContext {
                from_context: "tenantId".to_string(),
            },
        };
        assert!(evaluate_condition(&cond, &subject, &ctx).is_passed());
    }

    #[test]
    fn all_requires_every_condition_to_pass() {
        let ctx = context("t1", None);
        let subject = json!({ "a": 1, "b": 2 });
        let cond = PolicyCondition::All {
            all: vec![
                PolicyCondition::Attribute {
                    attribute: "a".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!(1) },
                },
                PolicyCondition::Attribute {
                    attribute: "b".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!(99) },
                },
            ],
        };
        let result = evaluate_condition(&cond, &subject, &ctx);
        assert!(!result.is_passed());
    }

    #[test]
    fn any_passes_if_one_condition_passes() {
        let ctx = context("t1", None);
        let subject = json!({ "a": 1, "b": 2 });
        let cond = PolicyCondition::Any {
            any: vec![
                PolicyCondition::Attribute {
                    attribute: "a".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!(99) },
                },
                PolicyCondition::Attribute {
                    attribute: "b".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!(2) },
                },
            ],
        };
        assert!(evaluate_condition(&cond, &subject, &ctx).is_passed());
    }

    #[test]
    fn evaluate_policy_row_fails_closed_when_role_gate_fails() {
        let ctx = context("t1", Some(vec!["sales"]));
        let row = policy(Some(vec!["ops"]), None, "context");
        assert!(!evaluate_policy_row(&row, &ctx, None));
    }

    #[test]
    fn evaluate_policy_row_uses_record_subject_when_declared() {
        let ctx = context("t1", None);
        let record = json!({ "ownerId": "u1" });
        let cond = PolicyCondition::Attribute {
            attribute: "ownerId".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal { literal: json!("u1") },
        };
        let row = policy(None, Some(cond), "record");
        assert!(evaluate_policy_row(&row, &ctx, Some(&record)));
    }

    #[test]
    fn policy_condition_deserializes_from_json_matching_ts_wire_shape() {
        let raw = json!({
            "attribute": "ownerId",
            "op": "eq",
            "value": { "fromContext": "userId" }
        });
        let cond: PolicyCondition = serde_json::from_value(raw).unwrap();
        assert!(matches!(cond, PolicyCondition::Attribute { .. }));
    }

    #[test]
    fn deny_overrides_allow_even_when_an_allow_policy_also_matches() {
        let ctx = context("t1", Some(vec!["sales"]));
        let allow = policy(Some(vec!["sales"]), None, "context");
        let deny = policy_with_effect(Some(vec!["sales"]), None, "context", PolicyEffect::Deny);
        assert_eq!(evaluate_policies([&allow, &deny], &ctx, None), PolicyVerdict::Deny);
        // Order must not matter — deny wins regardless of which is evaluated first.
        assert_eq!(evaluate_policies([&deny, &allow], &ctx, None), PolicyVerdict::Deny);
    }

    #[test]
    fn deny_policy_that_does_not_match_the_caller_does_not_block_a_matching_allow() {
        let ctx = context("t1", Some(vec!["sales"]));
        let allow = policy(Some(vec!["sales"]), None, "context");
        let deny_for_someone_else = policy_with_effect(Some(vec!["ops"]), None, "context", PolicyEffect::Deny);
        assert_eq!(
            evaluate_policies([&allow, &deny_for_someone_else], &ctx, None),
            PolicyVerdict::Allow
        );
    }

    #[test]
    fn dotted_attribute_path_resolves_into_a_merged_relation_field() {
        let ctx = context("t1", None);
        let subject = json!({ "project": { "ownerId": "u1" } });
        let cond = PolicyCondition::Attribute {
            attribute: "project.ownerId".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal { literal: json!("u1") },
        };
        assert!(evaluate_condition(&cond, &subject, &ctx).is_passed());
    }

    #[test]
    fn dotted_attribute_path_is_null_when_the_relation_was_never_merged_in() {
        let ctx = context("t1", None);
        let subject = json!({ "status": "active" });
        let cond = PolicyCondition::Attribute {
            attribute: "project.ownerId".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal { literal: json!("u1") },
        };
        assert!(!evaluate_condition(&cond, &subject, &ctx).is_passed());
    }

    #[test]
    fn required_relation_fields_collects_distinct_first_segments_across_nested_conditions() {
        let deep = PolicyCondition::Any {
            any: vec![
                PolicyCondition::Attribute {
                    attribute: "project.ownerId".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!("u1") },
                },
                PolicyCondition::Attribute {
                    attribute: "project.status".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!("open") },
                },
            ],
        };
        let cond = PolicyCondition::All {
            all: vec![
                deep,
                PolicyCondition::Attribute {
                    attribute: "team.leadId".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!("u2") },
                },
                // Plain same-record attribute — must not show up as a relation.
                PolicyCondition::Attribute {
                    attribute: "status".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal {
                        literal: json!("active"),
                    },
                },
            ],
        };
        let row = policy(None, Some(cond), "record");
        let mut fields = required_relation_fields(&[row]);
        fields.sort();
        assert_eq!(fields, vec!["project".to_string(), "team".to_string()]);
    }

    #[test]
    fn required_relation_fields_is_empty_when_no_condition_uses_a_dotted_path() {
        let cond = PolicyCondition::Attribute {
            attribute: "ownerId".to_string(),
            op: ConditionOp::Eq,
            value: PolicyValue::Literal { literal: json!("u1") },
        };
        let row = policy(None, Some(cond), "record");
        assert!(required_relation_fields(&[row]).is_empty());
    }

    #[test]
    fn no_matching_policy_at_all_is_no_match_not_deny() {
        let ctx = context("t1", Some(vec!["sales"]));
        let allow_for_someone_else = policy(Some(vec!["ops"]), None, "context");
        assert_eq!(
            evaluate_policies([&allow_for_someone_else], &ctx, None),
            PolicyVerdict::NoMatch
        );
    }
}
