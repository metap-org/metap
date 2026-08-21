pub mod context;
pub mod permission_service;
pub mod permission_snapshot;
pub mod policy_condition;
pub mod policy_explainer;
pub mod policy_store;

pub use context::{EntityAction, PermissionDecision, RequestContext};
pub use permission_service::PermissionService;
pub use permission_snapshot::{JsonObject, PermissionSnapshot};
pub use policy_condition::{
    evaluate_condition, evaluate_policies, evaluate_policy_row, role_gate_passed, ConditionOp, ConditionResult,
    PolicyCondition, PolicyValue, PolicyVerdict,
};
pub use policy_explainer::{explain_policies, Gate, PolicyExplanation, PolicyTraceEntry};
pub use policy_store::{row_from_sql, ExplainOptions, PolicyEffect, PolicyRow, PolicyStore, PolicySubject};
