//! `PolicyValue`/`ConditionOp`/`PolicyCondition` — the declarative shapes a `PolicyRow`'s
//! condition JSON deserializes into, evaluated by `super::evaluate`.

use serde::{Deserialize, Serialize};

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
