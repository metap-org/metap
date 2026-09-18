//! Tenant-selectable auth providers (`docs/roadmap.md`'s tenant-auth phase) — distinct from the
//! low-code platform's own admin auth, this is how a *tenant's own users* authenticate. A tenant
//! can enable more than one provider at once (`tenant_auth_configs` allows N rows per tenant,
//! not a single exclusive strategy), so `AuthProviderKind` is a discriminant tenant config picks
//! from, not a trait object every provider must uniformly implement — Local/Basic/OIDC take
//! genuinely different inputs (password vs an OIDC redirect code), so forcing one `dyn` call
//! signature across them would just be indirection with no shared behavior underneath.
//!
//! Bearer (JWT) itself is not a provider here — it's the *session* mechanism every successful
//! login (regardless of provider) ends in (`metap_peripherals::mint_jwt`), verified per-request
//! by `crates/metap-http/src/auth.rs`'s `AuthContext`, unchanged by this crate.
//!
//! No HTTP, no business-entity knowledge — a plain library, same shape as `metap-permission`.

use sqlx::{PgExecutor, Row};
use uuid::Uuid;

mod oauth2_login;
mod oidc;
pub use oauth2_login::{
    oauth2_login_authorize_url, oauth2_login_config, oauth2_login_verify_callback, OAuth2LoginConfig,
};
pub use oidc::{
    oidc_authorize_url, oidc_config, oidc_verify_callback, resolve_client_secret_env, OidcConfig, VerifiedIdentity,
};

pub use metap_peripherals::AuthUser;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuthProviderKind {
    Local,
    Basic,
    Oidc,
    /// Plain OAuth2 authorization-code login — distinct from `Oidc` above (see
    /// `oauth2_login.rs`'s doc comment): no discovery, no id_token, identity resolved by calling
    /// a configured userinfo endpoint with the obtained access token instead of decoding a JWT.
    OAuth2,
}

impl AuthProviderKind {
    pub fn as_str(self) -> &'static str {
        match self {
            AuthProviderKind::Local => "local",
            AuthProviderKind::Basic => "basic",
            AuthProviderKind::Oidc => "oidc",
            AuthProviderKind::OAuth2 => "oauth2",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "local" => Some(AuthProviderKind::Local),
            "basic" => Some(AuthProviderKind::Basic),
            "oidc" => Some(AuthProviderKind::Oidc),
            "oauth2" => Some(AuthProviderKind::OAuth2),
            _ => None,
        }
    }
}

/// The only provider today — thin wrapper over `metap_peripherals::verify_credentials`, kept
/// generic over `PgExecutor` (not boxed behind a trait) so callers can pass either a bare pool
/// or an already-open `Router::begin`-opened transaction, exactly as `POST /auth/login` already
/// does. Also backs `basic` (HTTP Basic auth verifies the exact same password credential, only
/// the HTTP transport that carries it differs) — one verifier, two ways a client presents it.
pub struct LocalPasswordProvider;

impl LocalPasswordProvider {
    pub fn kind(&self) -> AuthProviderKind {
        AuthProviderKind::Local
    }

    pub async fn verify<'e>(
        &self,
        executor: impl PgExecutor<'e>,
        email: &str,
        password: &str,
    ) -> anyhow::Result<Option<AuthUser>> {
        metap_peripherals::verify_credentials(executor, email, password).await
    }
}

/// Which providers a tenant currently has enabled — `crates/migrations/0019_tenant_auth_configs.sql`
/// backfilled `local` for every pre-existing tenant, so this is never empty for a real tenant,
/// only for one that predates that migration by way of some path that skipped provisioning
/// entirely (dev-only fixed tenant ids resolved through `Router`'s unregistered-tenant fallback —
/// callers must treat an empty result as "local only", not "nothing works", to match that
/// existing fallback behavior).
pub async fn enabled_providers<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
) -> anyhow::Result<Vec<AuthProviderKind>> {
    let kinds: Vec<String> =
        sqlx::query_scalar("SELECT provider_kind FROM tenant_auth_configs WHERE tenant_id = $1 AND enabled = true")
            .bind(tenant_id)
            .fetch_all(executor)
            .await?;
    Ok(kinds.iter().filter_map(|k| AuthProviderKind::parse(k)).collect())
}

/// A user JIT-provisioned (or previously linked) by a prior login through an external-identity
/// provider (`"oidc"` or `"oauth2"` — `users.auth_provider` is a plain free-text column, no
/// schema change needed to add the second value) for this tenant — looked up by
/// `external_subject` (the IdP's stable subject identifier), never by email, since email can
/// change at the IdP but the subject does not. [`find_oidc_user`] is now a thin `"oidc"`-fixed
/// wrapper so no existing caller changes.
pub async fn find_external_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    provider: &str,
    external_subject: &str,
) -> anyhow::Result<Option<AuthUser>> {
    let row = sqlx::query(
        "SELECT id, tenant_id, email FROM users \
         WHERE tenant_id = $1 AND auth_provider = $2 AND external_subject = $3",
    )
    .bind(tenant_id)
    .bind(provider)
    .bind(external_subject)
    .fetch_optional(executor)
    .await?;
    row.map(|r| {
        Ok(AuthUser {
            id: r.try_get("id")?,
            tenant_id: r.try_get("tenant_id")?,
            email: r.try_get("email")?,
        })
    })
    .transpose()
}

pub async fn find_oidc_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    external_subject: &str,
) -> anyhow::Result<Option<AuthUser>> {
    find_external_user(executor, tenant_id, "oidc", external_subject).await
}

/// First-ever login through an external-identity provider for this `(tenant_id, provider,
/// external_subject)` — auto-creates the local user row (JIT provisioning, project owner
/// decision 2026-08-24 for OIDC, extended to the OAuth2 login provider under the same reasoning:
/// no admin pre-creation required). `password_hash` stays `NULL`
/// (`crates/migrations/0020_users_oidc_columns.sql` made it nullable for exactly this) — neither
/// provider gives a local password to verify against. No role is assigned here: a JIT-provisioned
/// user starts with zero roles, same deny-by-default posture `PermissionService` already applies
/// to any roleless caller; an admin grants roles afterward via the existing
/// `POST /admin/users/{userId}/roles`. [`jit_provision_oidc_user`] is now a thin `"oidc"`-fixed
/// wrapper so no existing caller changes.
pub async fn jit_provision_external_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    provider: &str,
    email: &str,
    external_subject: &str,
) -> anyhow::Result<AuthUser> {
    let row = sqlx::query(
        "INSERT INTO users (tenant_id, email, auth_provider, external_subject) \
         VALUES ($1, $2, $3, $4) RETURNING id, tenant_id, email",
    )
    .bind(tenant_id)
    .bind(email)
    .bind(provider)
    .bind(external_subject)
    .fetch_one(executor)
    .await?;
    Ok(AuthUser {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        email: row.try_get("email")?,
    })
}

pub async fn jit_provision_oidc_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    email: &str,
    external_subject: &str,
) -> anyhow::Result<AuthUser> {
    jit_provision_external_user(executor, tenant_id, "oidc", email, external_subject).await
}
