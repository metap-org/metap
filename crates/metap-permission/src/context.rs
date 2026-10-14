//! Mirrors `packages/core/src/core/permission/permission-service.ts`'s `RequestContext`/
//! `PermissionDecision`/`EntityAction`.

use serde::{Deserialize, Serialize};

/// `context_attributes` key `RequestContext::apply_oauth_grant` writes the granted scope under.
pub const OAUTH_SCOPE_ATTRIBUTE: &str = "oauthScope";
/// `context_attributes` key for the OAuth2 client a scoped token was issued to.
pub const OAUTH_CLIENT_ID_ATTRIBUTE: &str = "oauthClientId";
/// The one scope token that is not `<verb>:<entity>` — required to reach admin/platform surfaces.
pub const ADMIN_SCOPE: &str = "admin";

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
    /// The caller's own bearer token, verbatim — set only when this context was built from a
    /// live inbound request whose token this process is itself allowed to hand onward to an
    /// upstream it calls on the caller's behalf (currently: only `graphql-gateway`, whose
    /// upstream and this process share one signing keypair — see
    /// `graphql-gateway/src/server.rs::authenticate`'s doc comment). `None` everywhere else
    /// (`metap-http::auth`, `metap-control::auth_context`, workers, tests) — those either have
    /// no upstream to re-authenticate against, or don't forward. `#[serde(skip)]`, not
    /// `skip_serializing_if` — this is a transport concern for `metap-grpc::GrpcBackend`, not
    /// ABAC-visible data, so it must never leak into a policy condition's `context[...]` lookup.
    #[serde(skip)]
    pub forwarded_bearer_token: Option<String>,
}

impl RequestContext {
    pub fn is_admin(&self) -> bool {
        self.roles
            .as_ref()
            .is_some_and(|roles| roles.iter().any(|r| r == "admin"))
    }

    /// Folds an OAuth2 grant (the `scope`/`clientId` claims of a token minted by
    /// `metap-oauth-server`) into `context_attributes`, where both a tenant's own
    /// `fromContext.oauthScope` ABAC condition and this crate's scope gate
    /// ([`Self::oauth_scope`]) read it back. Every transport that turns a bearer token into a
    /// `RequestContext` must call this when the token carries a `scope` claim — a transport that
    /// forgot would silently serve a scoped token as an unrestricted session.
    pub fn apply_oauth_grant(&mut self, scope: &str, client_id: Option<String>) {
        let attributes = self.context_attributes.get_or_insert_with(Default::default);
        attributes.insert(
            OAUTH_SCOPE_ATTRIBUTE.to_string(),
            serde_json::Value::Array(
                scope
                    .split_whitespace()
                    .map(|s| serde_json::Value::String(s.to_string()))
                    .collect(),
            ),
        );
        if let Some(client_id) = client_id {
            attributes.insert(
                OAUTH_CLIENT_ID_ATTRIBUTE.to_string(),
                serde_json::Value::String(client_id),
            );
        }
    }

    /// The granted OAuth2 scope tokens, or `None` for an ordinary session (login cookie/JWT
    /// without a `scope` claim) — `None` means "no scope restriction", while `Some(vec![])`
    /// (a token granted an empty scope) means "granted nothing" and denies everything below.
    pub fn oauth_scope(&self) -> Option<Vec<&str>> {
        let value = self.context_attributes.as_ref()?.get(OAUTH_SCOPE_ATTRIBUTE)?;
        let tokens = value.as_array()?;
        Some(tokens.iter().filter_map(|t| t.as_str()).collect())
    }

    /// Whether the OAuth2 scope (if any) lets this caller perform `action` on `entity`. Always
    /// `true` for an ordinary session. Grammar: `read:<entity>` → read, `write:<entity>` →
    /// create + update, `delete:<entity>` → delete, `transition:<entity>` → workflow
    /// transition; `<entity>` is an exact entity name or `*`. Any other token (including
    /// `admin`) grants no entity access. The gate narrows what the user's own roles/policies
    /// allow — it never widens it, and it deliberately applies to admins too.
    pub fn scope_permits(&self, entity: &str, action: EntityAction) -> bool {
        let Some(tokens) = self.oauth_scope() else {
            return true;
        };
        let verb = match action {
            EntityAction::Read => "read",
            EntityAction::Create | EntityAction::Update => "write",
            EntityAction::Delete => "delete",
            EntityAction::Transition => "transition",
        };
        tokens.iter().any(|t| {
            t.split_once(':')
                .is_some_and(|(v, e)| v == verb && (e == "*" || e == entity))
        })
    }

    /// Whether the OAuth2 scope (if any) lets this caller reach the tenant-admin / platform
    /// surfaces (`/admin/*`-style GraphQL fields, `AdminContext`, `PlatformAdminContext`). An
    /// ordinary session is unaffected; a scoped token needs the explicit `admin` scope, so a
    /// third-party client registered by an admin can never act as that admin on those surfaces
    /// just because a user with the admin role approved it.
    pub fn scope_permits_admin_surface(&self) -> bool {
        match self.oauth_scope() {
            None => true,
            Some(tokens) => tokens.contains(&ADMIN_SCOPE),
        }
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

    fn scoped(scope: &str) -> RequestContext {
        let mut ctx = RequestContext {
            tenant_id: "t1".to_string(),
            user_id: Some("u1".to_string()),
            roles: Some(vec!["admin".to_string()]),
            function_id: None,
            context_attributes: None,
            forwarded_bearer_token: None,
        };
        ctx.apply_oauth_grant(scope, Some("c1".to_string()));
        ctx
    }

    #[test]
    fn ordinary_session_has_no_scope_restriction() {
        let ctx = RequestContext {
            tenant_id: "t1".to_string(),
            user_id: None,
            roles: None,
            function_id: None,
            context_attributes: None,
            forwarded_bearer_token: None,
        };
        assert!(ctx.oauth_scope().is_none());
        assert!(ctx.scope_permits("waf.zones", EntityAction::Delete));
        assert!(ctx.scope_permits_admin_surface());
    }

    #[test]
    fn scope_maps_verbs_to_actions_and_matches_entities_exactly_or_by_wildcard() {
        let ctx = scoped("read:waf.zones write:waf.rules delete:*");
        assert!(ctx.scope_permits("waf.zones", EntityAction::Read));
        assert!(!ctx.scope_permits("waf.zones", EntityAction::Create));
        assert!(!ctx.scope_permits("waf.rules", EntityAction::Read));
        assert!(ctx.scope_permits("waf.rules", EntityAction::Create));
        assert!(ctx.scope_permits("waf.rules", EntityAction::Update));
        assert!(!ctx.scope_permits("waf.rules", EntityAction::Transition));
        assert!(ctx.scope_permits("anything.at_all", EntityAction::Delete));
        assert!(
            !ctx.scope_permits("waf.zones.extra", EntityAction::Read),
            "no prefix matching"
        );
    }

    #[test]
    fn empty_or_unrecognised_scope_grants_nothing_even_to_an_admin_user() {
        for scope in ["", "profile email", "admin"] {
            let ctx = scoped(scope);
            assert!(ctx.oauth_scope().is_some());
            assert!(!ctx.scope_permits("waf.zones", EntityAction::Read), "scope {scope:?}");
        }
    }

    #[test]
    fn admin_surfaces_need_the_explicit_admin_scope() {
        assert!(!scoped("read:*").scope_permits_admin_surface());
        assert!(!scoped("").scope_permits_admin_surface());
        assert!(scoped("admin").scope_permits_admin_surface());
    }

    fn base(context_attributes: Option<serde_json::Map<String, serde_json::Value>>) -> RequestContext {
        RequestContext {
            tenant_id: "t1".to_string(),
            user_id: Some("u1".to_string()),
            roles: Some(vec!["employee".to_string()]),
            function_id: None,
            context_attributes,
            forwarded_bearer_token: None,
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
