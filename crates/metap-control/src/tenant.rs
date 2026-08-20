use std::fmt;

use uuid::Uuid;

/// Newtype over the tenant's `Uuid` so `Router::begin`/`TenantRegistry::get` signatures can't
/// be confused with any other `Uuid`-shaped id (record id, user id) at the call site.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct TenantId(pub Uuid);

impl From<Uuid> for TenantId {
    fn from(id: Uuid) -> Self {
        TenantId(id)
    }
}

impl fmt::Display for TenantId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.0)
    }
}

/// The sentinel tenant a platform-superadmin's JWT carries as `tenantId` (Phase 16 Giai đoạn
/// 3, `docs/roadmap.md`). Not a real tenant — never has a `control.tenants` row and is never
/// routed to by `Router::begin`; it exists purely so `users`/`user_roles` (always `public`,
/// never Router-scoped) has somewhere to hold a platform-admin's identity, reusing the exact
/// same JWT/role machinery every tenant admin already goes through
/// (`metap_peripherals::create_user`/`assign_role`/`mint_jwt`,
/// `metap_peripherals::get_roles_for_user`) instead of a second auth model. A user in this
/// tenant with the `"platform_admin"` role (a role name like any other — nothing in the schema
/// distinguishes it) is what `metap-http`'s `PlatformAdminContext` extractor checks for.
/// `Uuid::nil()` (all-zero) — verified unused by any other reserved id in this codebase.
pub const PLATFORM_TENANT_ID: Uuid = Uuid::nil();

/// Mirrors `control.tenants.status` (`crates/migrations/0012_control_tenants.sql`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TenantStatus {
    Provisioning,
    Active,
    Migrating,
    Suspended,
    Expired,
    /// Terminal, never reversible via `PATCH /platform/tenants/{id}/status` the way `Suspended`
    /// is via `resume` (`docs/roadmap.md` Phase 16, deprovisioning — 2026-08-21). Set only by
    /// `DELETE /platform/tenants/{id}`. Deliberately does **not** drop the tenant's physical
    /// data — a `dedicated_db` tenant's database is left alone (an operator drops it by hand
    /// once they're sure no backup/audit need remains), and a `schema` tenant's rows in the
    /// shared `records`/`users`/... tables are left alone too (real per-tenant purge needs
    /// data-plane isolation — §3, not built — to be safe against a bug in a `WHERE tenant_id`
    /// clause touching another tenant sharing the same table). This status only ever changes
    /// what `Router::begin` does: refuse permanently, so nothing new can be written or read
    /// through the generic execution engine for this tenant id again.
    Deleted,
}

impl TenantStatus {
    pub fn as_str(&self) -> &'static str {
        match self {
            TenantStatus::Provisioning => "provisioning",
            TenantStatus::Active => "active",
            TenantStatus::Migrating => "migrating",
            TenantStatus::Suspended => "suspended",
            TenantStatus::Expired => "expired",
            TenantStatus::Deleted => "deleted",
        }
    }

    pub fn parse(s: &str) -> anyhow::Result<Self> {
        match s {
            "provisioning" => Ok(TenantStatus::Provisioning),
            "active" => Ok(TenantStatus::Active),
            "migrating" => Ok(TenantStatus::Migrating),
            "suspended" => Ok(TenantStatus::Suspended),
            "expired" => Ok(TenantStatus::Expired),
            "deleted" => Ok(TenantStatus::Deleted),
            other => anyhow::bail!("unknown control.tenants.status value: {other}"),
        }
    }
}

/// Mirrors `control.tenants.strategy` plus whichever of `schema_name`/`dsn_secret_ref` that
/// strategy uses — `docs/multi-tenant-platform-design.md` §2.1/§2.2.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TenantStrategy {
    Schema { schema_name: String },
    DedicatedDb { dsn_secret_ref: String },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TenantRouting {
    pub status: TenantStatus,
    pub strategy: TenantStrategy,
}
