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
    /// The reverse of `In`: `actual` must be a JSON array containing `expected` as an element,
    /// rather than `expected` being an array containing `actual`. Added so a `context`-subject
    /// policy can gate on an array-shaped context attribute — the motivating case is
    /// `metap-http::auth`'s `oauthScope` (an OAuth2 `client_credentials`/`authorization_code`
    /// token's granted scope, always an array even when it has one element), which `In`/`NotIn`
    /// can't express: those need `expected` to be the array and `actual` the scalar, the opposite
    /// of "does this array attribute contain this literal scope string". Fails closed (`false`)
    /// when `actual` isn't an array at all, same posture `Gt`/`Gte`/`Lt`/`Lte` already take for a
    /// type mismatch.
    Contains,
    NotContains,
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
