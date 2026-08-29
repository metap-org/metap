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
    /// Caller attributes beyond identity/role — populated by `metap-http`'s `AuthContext`
    /// extractor from a configured "membership" entity's record when `AUTH_CONTEXT_ENTITY` is
    /// set (`docs/features/03-organization-identity.md`), e.g. `{"departmentId": "..."}` for an
    /// org-scoped policy's `fromContext` to read. `#[serde(flatten)]` so `to_value()` exposes
    /// these keys at the top level, same as the fixed fields — avoid naming an attribute
    /// `tenantId`/`userId`/`roles`/`functionId` (undefined precedence with the fixed field of
    /// the same name, not an error but not something to rely on). `None` when the feature is
    /// off or the caller has no matching record — this crate
    /// stays entity-agnostic, it never queries the record itself, only carries what
    /// `metap-http` already resolved.
    #[serde(flatten, skip_serializing_if = "Option::is_none")]
    pub context_attributes: Option<serde_json::Map<String, serde_json::Value>>,
}

impl RequestContext {
    pub fn is_admin(&self) -> bool {
        self.roles
            .as_ref()
            .is_some_and(|roles| roles.iter().any(|r| r == "admin"))
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
        Self {
            allowed: true,
            reason: None,
            field: None,
        }
    }

    pub fn forbidden() -> Self {
        Self {
            allowed: false,
            reason: Some("forbidden".to_string()),
            field: None,
        }
    }

    pub fn forbidden_field(field: impl Into<String>) -> Self {
        Self {
            allowed: false,
            reason: Some("forbidden".to_string()),
            field: Some(field.into()),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum EntityAction {
    Read,
    Create,
    Update,
    Delete,
    /// A workflow transition — previously checked as `Update`, split out so a policy can grant
    /// "edit fields" without also granting "change state" (or vice versa).
    Transition,
}

impl EntityAction {
    /// Every action a policy can grant — the single source of truth for `metap-http`'s
    /// `KNOWN_ACTIONS`/`seed_default_policies` default and the `GET /metadata/actions` route, so
    /// the two can't drift the way `metap-http`'s own hand-written `[&str; 5]` mirror already
    /// had (used to silently duplicate this list rather than derive from it).
    pub const ALL: [EntityAction; 5] = [
        EntityAction::Read,
        EntityAction::Create,
        EntityAction::Update,
        EntityAction::Delete,
        EntityAction::Transition,
    ];

    pub const fn as_str(&self) -> &'static str {
        match self {
            EntityAction::Read => "read",
            EntityAction::Create => "create",
            EntityAction::Update => "update",
            EntityAction::Delete => "delete",
            EntityAction::Transition => "transition",
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn base(context_attributes: Option<serde_json::Map<String, serde_json::Value>>) -> RequestContext {
        RequestContext {
            tenant_id: "t1".to_string(),
            user_id: Some("u1".to_string()),
            roles: Some(vec!["employee".to_string()]),
            function_id: None,
            context_attributes,
        }
    }

    #[test]
    fn context_attributes_none_produces_no_extra_keys() {
        let value = base(None).to_value();
        assert_eq!(
            value,
            serde_json::json!({ "tenantId": "t1", "userId": "u1", "roles": ["employee"] })
        );
    }

    #[test]
    fn context_attributes_are_flattened_to_the_top_level() {
        let mut attrs = serde_json::Map::new();
        attrs.insert("departmentId".to_string(), serde_json::json!("dept-sales"));
        let value = base(Some(attrs)).to_value();
        assert_eq!(value["departmentId"], serde_json::json!("dept-sales"));
        // fixed fields must still be present, untouched
        assert_eq!(value["tenantId"], serde_json::json!("t1"));
    }
}
