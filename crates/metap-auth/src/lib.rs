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

mod oidc;
pub use oidc::{
    oidc_authorize_url, oidc_config, oidc_verify_callback, resolve_client_secret_env, OidcConfig, VerifiedIdentity,
};

pub use metap_peripherals::AuthUser;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuthProviderKind {
    Local,
    Basic,
    Oidc,
}

impl AuthProviderKind {
    pub fn as_str(self) -> &'static str {
        match self {
            AuthProviderKind::Local => "local",
            AuthProviderKind::Basic => "basic",
            AuthProviderKind::Oidc => "oidc",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "local" => Some(AuthProviderKind::Local),
            "basic" => Some(AuthProviderKind::Basic),
            "oidc" => Some(AuthProviderKind::Oidc),
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

/// A user JIT-provisioned (or previously linked) by a prior OIDC login for this tenant — looked
/// up by `external_subject` (the IdP's stable `sub` claim), never by email, since email can
/// change at the IdP but `sub` does not.
pub async fn find_oidc_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    external_subject: &str,
) -> anyhow::Result<Option<AuthUser>> {
    let row = sqlx::query(
        "SELECT id, tenant_id, email FROM users \
         WHERE tenant_id = $1 AND auth_provider = 'oidc' AND external_subject = $2",
    )
    .bind(tenant_id)
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

/// First-ever OIDC login for this `(tenant_id, external_subject)` — auto-creates the local user
/// row (JIT provisioning, project owner decision 2026-08-24: no admin pre-creation required).
/// `password_hash` stays `NULL` (`crates/migrations/0020_users_oidc_columns.sql` made it
/// nullable for exactly this) — an OIDC-only user has no local password to verify against. No
/// role is assigned here: a JIT-provisioned user starts with zero roles, same
/// deny-by-default posture `PermissionService` already applies to any roleless caller; an admin
/// grants roles afterward via the existing `POST /admin/users/{userId}/roles`.
pub async fn jit_provision_oidc_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    email: &str,
    external_subject: &str,
) -> anyhow::Result<AuthUser> {
    let row = sqlx::query(
        "INSERT INTO users (tenant_id, email, auth_provider, external_subject) \
         VALUES ($1, $2, 'oidc', $3) RETURNING id, tenant_id, email",
    )
    .bind(tenant_id)
    .bind(email)
    .bind(external_subject)
    .fetch_one(executor)
    .await?;
    Ok(AuthUser {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        email: row.try_get("email")?,
    })
}
