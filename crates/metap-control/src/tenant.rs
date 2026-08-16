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

/// Mirrors `control.tenants.status` (`crates/migrations/0012_control_tenants.sql`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TenantStatus {
    Provisioning,
    Active,
    Migrating,
    Suspended,
    Expired,
}

impl TenantStatus {
    pub fn as_str(&self) -> &'static str {
        match self {
            TenantStatus::Provisioning => "provisioning",
            TenantStatus::Active => "active",
            TenantStatus::Migrating => "migrating",
            TenantStatus::Suspended => "suspended",
            TenantStatus::Expired => "expired",
        }
    }

    pub fn parse(s: &str) -> anyhow::Result<Self> {
        match s {
            "provisioning" => Ok(TenantStatus::Provisioning),
            "active" => Ok(TenantStatus::Active),
            "migrating" => Ok(TenantStatus::Migrating),
            "suspended" => Ok(TenantStatus::Suspended),
            "expired" => Ok(TenantStatus::Expired),
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
