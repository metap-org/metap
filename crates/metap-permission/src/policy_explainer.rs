//! Mirrors `packages/core/src/core/permission/policy-explainer.ts`: a read-only trace of
//! every policy considered and why, for the admin-gated policy simulator.

use serde::Serialize;

use crate::context::RequestContext;
use crate::permission_snapshot::JsonObject;
use crate::policy_condition::{evaluate_condition, role_gate_passed};
use crate::policy_store::PolicyRow;

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
    pub role_gate: Gate,
    pub condition_gate: Gate,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub condition_reason: Option<String>,
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
        return PolicyExplanation { allowed: true, policies_considered: vec![] };
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
                    role_gate,
                    condition_gate: Gate::Open,
                    condition_reason: None,
                };
            }

            let Some(condition) = &row.condition else {
                return PolicyTraceEntry {
                    policy_id: row.id.to_string(),
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
                    role_gate,
                    condition_gate: Gate::Passed,
                    condition_reason: None,
                }
            } else {
                PolicyTraceEntry {
                    policy_id: row.id.to_string(),
                    role_gate,
                    condition_gate: Gate::Failed,
                    condition_reason: result.reason().map(String::from),
                }
            }
        })
        .collect();

    let allowed = entries
        .iter()
        .any(|entry| entry.role_gate != Gate::Failed && entry.condition_gate != Gate::Failed);

    PolicyExplanation { allowed, policies_considered: entries }
}
