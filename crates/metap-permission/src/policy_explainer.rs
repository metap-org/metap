//! Mirrors `packages/core/src/core/permission/policy-explainer.ts`: a read-only trace of
//! every policy considered and why, for the admin-gated policy simulator.

use serde::Serialize;

use crate::context::RequestContext;
use crate::permission_snapshot::JsonObject;
use crate::policy_condition::{evaluate_condition, role_gate_passed};
use crate::policy_store::{PolicyEffect, PolicyRow};

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Gate {
    Open,
    Passed,
    Failed,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PolicyTraceEntry {
    pub policy_id: String,
    /// Found missing live (`AUDIT_2.md`): without this, an admin looking at the trace has no way
    /// to see that a matched `Deny` policy is what actually decided the verdict — `allowed`
    /// below used to treat a matched `Deny` exactly like a matched `Allow` (both just "not
    /// Failed"), so this diagnostic tool could report `allowed: true` for a request real
    /// enforcement (`evaluate_policies`'s deny-overrides-allow fold) would reject with a 403.
    pub effect: PolicyEffect,
    pub role_gate: Gate,
    pub condition_gate: Gate,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub condition_reason: Option<String>,
}

impl PolicyTraceEntry {
    /// Both gates cleared (`Open` or `Passed`, never `Failed`) — this policy is one that would
    /// actually apply to the request, regardless of which `effect` it carries.
    fn matched(&self) -> bool {
        self.role_gate != Gate::Failed && self.condition_gate != Gate::Failed
    }
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PolicyExplanation {
    pub allowed: bool,
    pub policies_considered: Vec<PolicyTraceEntry>,
}

pub fn explain_policies(
    policy_rows: &[PolicyRow],
    context: &RequestContext,
    subject: Option<&JsonObject>,
) -> PolicyExplanation {
    if policy_rows.is_empty() {
        return PolicyExplanation {
            allowed: true,
            policies_considered: vec![],
        };
    }

    let entries: Vec<PolicyTraceEntry> = policy_rows
        .iter()
        .map(|row| {
            let role_passed = role_gate_passed(row.roles.as_deref(), context.roles.as_deref());
            let role_gate = match &row.roles {
                None => Gate::Open,
                Some(roles) if roles.is_empty() => Gate::Open,
                Some(_) if role_passed => Gate::Passed,
                Some(_) => Gate::Failed,
            };

            if !role_passed {
                return PolicyTraceEntry {
                    policy_id: row.id.to_string(),
                    effect: row.effect,
                    role_gate,
                    condition_gate: Gate::Open,
                    condition_reason: None,
                };
            }

            let Some(condition) = &row.condition else {
                return PolicyTraceEntry {
                    policy_id: row.id.to_string(),
                    effect: row.effect,
                    role_gate,
                    condition_gate: Gate::Open,
                    condition_reason: None,
                };
            };

            let condition_subject = if row.subject == "record" {
                subject
                    .map(|s| serde_json::Value::Object(s.clone()))
                    .unwrap_or_else(|| context.to_value())
            } else {
                context.to_value()
            };

            let result = evaluate_condition(condition, &condition_subject, context);

            if result.is_passed() {
                PolicyTraceEntry {
                    policy_id: row.id.to_string(),
                    effect: row.effect,
                    role_gate,
                    condition_gate: Gate::Passed,
                    condition_reason: None,
                }
            } else {
                PolicyTraceEntry {
                    policy_id: row.id.to_string(),
                    effect: row.effect,
                    role_gate,
                    condition_gate: Gate::Failed,
                    condition_reason: result.reason().map(String::from),
                }
            }
        })
        .collect();

    // Deny-overrides-allow, mirroring `evaluate_policies`'s real enforcement fold exactly
    // (`AUDIT_2.md`: this used to treat a matched `Deny` the same as a matched `Allow` — both
    // just "not Failed" — so this diagnostic could say `allowed: true` for a request real
    // enforcement would 403).
    let any_matching_deny = entries.iter().any(|e| e.matched() && e.effect == PolicyEffect::Deny);
    let any_matching_allow = entries.iter().any(|e| e.matched() && e.effect == PolicyEffect::Allow);
    let allowed = !any_matching_deny && any_matching_allow;

    PolicyExplanation {
        allowed,
        policies_considered: entries,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::policy_condition::evaluate_policies;
    use uuid::Uuid;

    fn context(roles: Option<Vec<&str>>) -> RequestContext {
        RequestContext {
            tenant_id: Uuid::new_v4().to_string(),
            user_id: None,
            roles: roles.map(|r| r.into_iter().map(String::from).collect()),
            function_id: None,
            context_attributes: None,
        }
    }

    fn row(roles: Option<Vec<&str>>, effect: PolicyEffect) -> PolicyRow {
        PolicyRow {
            id: Uuid::new_v4(),
            tenant_id: Uuid::new_v4(),
            entity: "crm.customers".to_string(),
            action: "read".to_string(),
            field: None,
            subject: "context".to_string(),
            roles: roles.map(|r| r.into_iter().map(String::from).collect()),
            condition: None,
            created_by: None,
            effect,
        }
    }

    /// Regression for the finding in `AUDIT_2.md`: a matched `Deny` used to be indistinguishable
    /// from a matched `Allow` in this trace — both just "gates not Failed" — so `allowed` could
    /// say `true` for a request real enforcement (`evaluate_policies`) would reject.
    #[test]
    fn a_matching_deny_overrides_a_matching_allow_and_is_visible_in_the_trace() {
        let ctx = context(Some(vec!["sales"]));
        let allow = row(Some(vec!["sales"]), PolicyEffect::Allow);
        let deny = row(Some(vec!["sales"]), PolicyEffect::Deny);

        let explanation = explain_policies(&[allow.clone(), deny.clone()], &ctx, None);
        assert!(
            !explanation.allowed,
            "a matching Deny must flip `allowed` to false, not be treated like an Allow"
        );
        let deny_entry = explanation
            .policies_considered
            .iter()
            .find(|e| e.policy_id == deny.id.to_string())
            .unwrap();
        assert_eq!(
            deny_entry.effect,
            PolicyEffect::Deny,
            "the trace must show which effect matched"
        );

        // Cross-check against real enforcement — the whole point of this fix is that the two
        // must agree.
        assert_eq!(
            evaluate_policies([&allow, &deny], &ctx, None).is_allowed(),
            explanation.allowed
        );
    }

    #[test]
    fn allow_only_is_allowed_and_deny_only_is_not() {
        let ctx = context(Some(vec!["sales"]));
        let allow_only = explain_policies(&[row(Some(vec!["sales"]), PolicyEffect::Allow)], &ctx, None);
        assert!(allow_only.allowed);

        let deny_only = explain_policies(&[row(Some(vec!["sales"]), PolicyEffect::Deny)], &ctx, None);
        assert!(!deny_only.allowed);
    }

    #[test]
    fn a_deny_that_does_not_match_the_caller_does_not_block_a_matching_allow() {
        let ctx = context(Some(vec!["sales"]));
        let allow = row(Some(vec!["sales"]), PolicyEffect::Allow);
        let deny_for_someone_else = row(Some(vec!["ops"]), PolicyEffect::Deny);

        let explanation = explain_policies(&[allow, deny_for_someone_else], &ctx, None);
        assert!(
            explanation.allowed,
            "a Deny that doesn't match this caller must not block an Allow that does"
        );
    }
}
