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
    evaluate_condition, evaluate_policy_row, role_gate_passed, ConditionOp, ConditionResult,
    PolicyCondition, PolicyValue,
};
pub use policy_explainer::{explain_policies, Gate, PolicyExplanation, PolicyTraceEntry};
pub use policy_store::{ExplainOptions, PolicyRow, PolicyStore, PolicySubject, PostgresPolicyStore};
