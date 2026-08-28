//! Evaluating one `PolicyCondition`/one `PolicyRow`/a whole policy set against a subject and
//! `RequestContext` — the role-gate-then-condition-gate order this module's doc comment
//! describes.

use crate::context::RequestContext;
use crate::policy_store::{PolicyEffect, PolicyRow};

use super::resolve::{resolve_attribute_path, resolve_value};
use super::types::{ConditionOp, PolicyCondition};

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
