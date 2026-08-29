pub mod auth;
pub mod context_attributes;
pub mod index_reconciler;
pub mod metadata_drift;
pub mod preferences;
pub mod role_assignment;

pub use auth::{
    create_user, decode_access_token, list_tenant_users, mint_jwt, verify_credentials, AccessClaims, AuthUser,
    JWT_AUDIENCE, JWT_ISSUER,
};
pub use context_attributes::fetch_context_attributes;
pub use index_reconciler::reconcile as reconcile_indexes;
pub use metadata_drift::check as check_metadata_drift;
pub use preferences::{get_locale, set_locale, DEFAULT_LOCALE};
pub use role_assignment::{assign_role, get_roles_for_user, list_users, revoke_role, UserRoles};
