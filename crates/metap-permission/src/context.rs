//! Mirrors `packages/core/src/core/permission/permission-service.ts`'s `RequestContext`/
//! `PermissionDecision`/`EntityAction`.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RequestContext {
    pub tenant_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub roles: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub function_id: Option<String>,
}

impl RequestContext {
    pub fn is_admin(&self) -> bool {
        self.roles.as_ref().is_some_and(|roles| roles.iter().any(|r| r == "admin"))
    }

    /// `context[attribute]`-style lookup used when a condition's subject is the caller's
    /// own context rather than the record — serializes to a JSON object and reads the key
    /// generically, matching JS's ability to bracket-index a typed object by string key.
    pub fn to_value(&self) -> serde_json::Value {
        serde_json::to_value(self).unwrap_or(serde_json::Value::Null)
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PermissionDecision {
    pub allowed: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub field: Option<String>,
}

impl PermissionDecision {
    pub fn allowed() -> Self {
        Self { allowed: true, reason: None, field: None }
    }

    pub fn forbidden() -> Self {
        Self { allowed: false, reason: Some("forbidden".to_string()), field: None }
    }

    pub fn forbidden_field(field: impl Into<String>) -> Self {
        Self { allowed: false, reason: Some("forbidden".to_string()), field: Some(field.into()) }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum EntityAction {
    Read,
    Create,
    Update,
    Delete,
}

impl EntityAction {
    pub fn as_str(&self) -> &'static str {
        match self {
            EntityAction::Read => "read",
            EntityAction::Create => "create",
            EntityAction::Update => "update",
            EntityAction::Delete => "delete",
        }
    }
}
