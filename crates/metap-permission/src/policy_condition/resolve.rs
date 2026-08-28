//! Resolving a `PolicyValue`/attribute path against a subject/`RequestContext` — no
//! condition-matching logic here, that's `super::evaluate`.

use crate::context::RequestContext;
use crate::policy_store::PolicyRow;

use super::types::{PolicyCondition, PolicyValue};

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
pub(crate) fn resolve_attribute_path(subject: &serde_json::Value, attribute: &str) -> serde_json::Value {
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
