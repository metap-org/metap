//! Mirrors `packages/core/src/core/permission/policy-condition.ts` field-for-field: same
//! operators, same role-gate/condition-gate evaluation order, same "record" vs "context"
//! subject selection.

use serde::{Deserialize, Serialize};

use crate::context::RequestContext;
use crate::policy_store::{PolicyEffect, PolicyRow};

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum PolicyValue {
    Literal {
        literal: serde_json::Value,
    },
    FromContext {
        #[serde(rename = "fromContext")]
        from_context: String,
    },
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum ConditionOp {
    Eq,
    Neq,
    In,
    NotIn,
    /// Numeric/lexicographic ordering — `actual`/`expected` must both be JSON numbers (compared
    /// as `f64`) or both JSON strings (compared lexicographically); any other pairing (a type
    /// mismatch, or either side `null`/an array/object) fails closed (`false`), matching this
    /// module's existing fail-closed posture elsewhere. Added (`AUDIT_2.md`) because a guard
    /// like "amount > 10000 needs senior approval" had no way to express itself before this —
    /// `journal_entry_entity.rs`'s `post` guard had to fake "at least one side is positive" with
    /// `Neq 0`, which wrongly also accepted a negative amount.
    Gt,
    Gte,
    Lt,
    Lte,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum PolicyCondition {
    Attribute {
        attribute: String,
        op: ConditionOp,
        value: PolicyValue,
    },
    All {
        all: Vec<PolicyCondition>,
    },
    Any {
        any: Vec<PolicyCondition>,
    },
}

/// `pub` so callers besides this module's own `evaluate_condition` can resolve a `PolicyValue`
/// against a `RequestContext` — `metap-workflow`'s `set_fields` (a transition's declarative
/// post-function: "set this field to a literal or `fromContext` value on transition") reuses
/// this exact resolution instead of re-implementing the `Literal`/`FromContext` match.
pub fn resolve_value(value: &PolicyValue, context: &RequestContext) -> serde_json::Value {
    match value {
        PolicyValue::Literal { literal } => literal.clone(),
        PolicyValue::FromContext { from_context } => context
            .to_value()
            .get(from_context)
            .cloned()
            .unwrap_or(serde_json::Value::Null),
    }
}

/// `attribute` may be a dotted path (e.g. `"project.ownerId"`) to reach into a related
/// record's data that `CrudService` has merged onto `subject` under the relation field's own
/// name (see `required_relation_fields` below) — a single-segment path is just one `.get()`
/// call, unchanged from before cross-record conditions existed.
fn resolve_attribute_path(subject: &serde_json::Value, attribute: &str) -> serde_json::Value {
    let mut current = subject;
    for segment in attribute.split('.') {
        match current.get(segment) {
            Some(next) => current = next,
            None => return serde_json::Value::Null,
        }
    }
    current.clone()
}

/// The top-level relation field name of a dotted attribute path (`"project.ownerId"` ->
/// `"project"`), or `None` for a plain, same-record attribute. Only one hop is ever resolved —
/// a path with more than one dot still only names its first segment as the relation to fetch,
/// matching the one-hop-only scope `CrudService`'s enrichment implements.
fn relation_field_of(attribute: &str) -> Option<&str> {
    attribute.split_once('.').map(|(head, _)| head)
}

fn collect_relation_fields(condition: &PolicyCondition, out: &mut Vec<String>) {
    match condition {
        PolicyCondition::Attribute { attribute, .. } => {
            if let Some(field) = relation_field_of(attribute) {
                if !out.iter().any(|f| f == field) {
                    out.push(field.to_string());
                }
            }
        }
        PolicyCondition::All { all } => {
            for inner in all {
                collect_relation_fields(inner, out);
            }
        }
        PolicyCondition::Any { any } => {
            for inner in any {
                collect_relation_fields(inner, out);
            }
        }
    }
}

/// Every relation field name referenced by a dotted attribute path (`"project.ownerId"`) across
/// `policies`' conditions — the set `CrudService` needs to fetch and merge onto the record
/// subject before evaluating these policies. Deliberately cheap and synchronous (no I/O, no
/// entity-metadata lookup): a caller with no cross-record conditions at all gets an empty `Vec`
/// back and does zero extra work, which is the whole point of resolving this per-action instead
/// of unconditionally fetching every `Reference` field on every record-level permission check.
pub fn required_relation_fields(policies: &[PolicyRow]) -> Vec<String> {
    let mut out = Vec::new();
    for policy in policies {
        if let Some(condition) = &policy.condition {
            collect_relation_fields(condition, &mut out);
        }
    }
    out
}

/// `None` for any pairing `Gt`/`Gte`/`Lt`/`Lte` can't meaningfully order — a type mismatch, or
/// either side `null`/an array/object — so the caller can fail closed instead of guessing.
fn compare_ordering(actual: &serde_json::Value, expected: &serde_json::Value) -> Option<std::cmp::Ordering> {
    match (actual, expected) {
        (serde_json::Value::Number(a), serde_json::Value::Number(b)) => a.as_f64()?.partial_cmp(&b.as_f64()?),
        (serde_json::Value::String(a), serde_json::Value::String(b)) => Some(a.cmp(b)),
        _ => None,
    }
}

fn match_operator(op: ConditionOp, actual: &serde_json::Value, expected: &serde_json::Value) -> bool {
    use std::cmp::Ordering;
    match op {
        ConditionOp::Eq => actual == expected,
        ConditionOp::Neq => actual != expected,
        ConditionOp::In => expected.as_array().is_some_and(|arr| arr.contains(actual)),
        ConditionOp::NotIn => expected.as_array().is_some_and(|arr| !arr.contains(actual)),
        ConditionOp::Gt => compare_ordering(actual, expected) == Some(Ordering::Greater),
        ConditionOp::Gte => matches!(
            compare_ordering(actual, expected),
            Some(Ordering::Greater | Ordering::Equal)
        ),
        ConditionOp::Lt => compare_ordering(actual, expected) == Some(Ordering::Less),
        ConditionOp::Lte => matches!(
            compare_ordering(actual, expected),
            Some(Ordering::Less | Ordering::Equal)
        ),
    }
}

/// No policy roles at all (`null`/empty) means the gate is open to everyone — matches
/// `roleGatePassed`'s "no role restriction declared" semantics exactly.
pub fn role_gate_passed(policy_roles: Option<&[String]>, caller_roles: Option<&[String]>) -> bool {
    match policy_roles {
        None => true,
        Some([]) => true,
        Some(policy_roles) => caller_roles
            .unwrap_or(&[])
            .iter()
            .any(|role| policy_roles.iter().any(|r| r == role)),
    }
}

pub fn evaluate_policy_row(
    policy: &PolicyRow,
    context: &RequestContext,
    record_subject: Option<&serde_json::Value>,
) -> bool {
    if !role_gate_passed(policy.roles.as_deref(), context.roles.as_deref()) {
        return false;
    }

    let Some(condition) = &policy.condition else {
        return true;
    };

    let subject = if policy.subject == "record" {
        record_subject.cloned().unwrap_or_else(|| context.to_value())
    } else {
        context.to_value()
    };

    evaluate_condition(condition, &subject, context) == ConditionResult::Passed
}

/// Result of evaluating a whole set of policies together, with `Deny`-overrides-`Allow`
/// semantics — see `PolicyEffect`'s doc comment. `NoMatch` (nothing in the set matched at all)
/// is deliberately distinct from `Deny` (something matched and explicitly refused) purely so a
/// caller can log/report the two differently; both mean "not allowed".
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PolicyVerdict {
    Allow,
    Deny,
    NoMatch,
}

impl PolicyVerdict {
    pub fn is_allowed(&self) -> bool {
        matches!(self, PolicyVerdict::Allow)
    }
}

/// Aggregates every policy in `policies` that actually matches `context`/`subject_kind_subject`
/// (role gate + condition, via `evaluate_policy_row`) into one verdict: any matching `Deny`
/// wins outright; otherwise `Allow` if at least one matching `Allow`; otherwise `NoMatch`. This
/// is the one place deny-overrides-allow is decided — `check_action`,
/// `PermissionSnapshot::filter_readable_fields`/`writable_fields`/`can_perform_record_condition`
/// all route through this instead of each re-implementing the same fold.
pub fn evaluate_policies<'a>(
    policies: impl IntoIterator<Item = &'a PolicyRow>,
    context: &RequestContext,
    record_subject: Option<&serde_json::Value>,
) -> PolicyVerdict {
    let mut any_allow = false;
    for policy in policies {
        if evaluate_policy_row(policy, context, record_subject) {
            match policy.effect {
                PolicyEffect::Deny => return PolicyVerdict::Deny,
                PolicyEffect::Allow => any_allow = true,
            }
        }
    }
    if any_allow {
        PolicyVerdict::Allow
    } else {
        PolicyVerdict::NoMatch
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ConditionResult {
    Passed,
    Failed(String),
}

impl ConditionResult {
    pub fn is_passed(&self) -> bool {
        matches!(self, ConditionResult::Passed)
    }

    pub fn reason(&self) -> Option<&str> {
        match self {
            ConditionResult::Passed => None,
            ConditionResult::Failed(reason) => Some(reason),
        }
    }
}

pub fn evaluate_condition(
    condition: &PolicyCondition,
    subject: &serde_json::Value,
    context: &RequestContext,
) -> ConditionResult {
    match condition {
        PolicyCondition::All { all } => {
            for inner in all {
                let result = evaluate_condition(inner, subject, context);
                if !result.is_passed() {
                    return result;
                }
            }
            ConditionResult::Passed
        }
        PolicyCondition::Any { any } => {
            let mut last_failure: Option<String> = None;
            for inner in any {
                let result = evaluate_condition(inner, subject, context);
                if result.is_passed() {
                    return ConditionResult::Passed;
                }
                last_failure = result.reason().map(String::from);
            }
            ConditionResult::Failed(last_failure.unwrap_or_else(|| "no condition in 'any' matched".to_string()))
        }
        PolicyCondition::Attribute { attribute, op, value } => {
            let actual = resolve_attribute_path(subject, attribute);
            let expected = resolve_value(value, context);
            if match_operator(*op, &actual, &expected) {
                ConditionResult::Passed
            } else {
                let op_str = match op {
                    ConditionOp::Eq => "eq",
                    ConditionOp::Neq => "neq",
                    ConditionOp::In => "in",
                    ConditionOp::NotIn => "notIn",
                    ConditionOp::Gt => "gt",
                    ConditionOp::Gte => "gte",
                    ConditionOp::Lt => "lt",
                    ConditionOp::Lte => "lte",
                };
                ConditionResult::Failed(format!(
                    "condition failed: {attribute} {op_str} {expected} (got {actual})"
                ))
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use uuid::Uuid;

    fn context(tenant_id: &str, roles: Option<Vec<&str>>) -> RequestContext {
        RequestContext {
            tenant_id: tenant_id.to_string(),
            user_id: None,
            roles: roles.map(|r| r.into_iter().map(String::from).collect()),
            function_id: None,
            context_attributes: None,
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
