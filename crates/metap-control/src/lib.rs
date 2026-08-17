//! Control-plane skeleton for SaaS multi-tenancy (`docs/multi-tenant-platform-design.md` §2,
//! `docs/architectures/09-adr.md`'s tenant-isolation decision) — `Router` is the seam
//! `metap-crud`'s `CrudService` opens every tenant-scoped transaction through, instead of a bare
//! `PgPool`. See `Router::begin`'s doc comment for the unregistered-tenant compatibility
//! fallback this stage relies on. No HTTP, no business-entity knowledge — a plain library, same
//! shape as `metap-permission`.

mod cache;
mod provisioning;
mod registry;
mod router;
mod secret_store;
mod tenant;
mod vault_store;

pub use cache::RegistryCache;
pub use provisioning::{provision_dedicated_db_tenant, provision_schema_tenant, ProvisionedTenant};
pub use registry::{PostgresTenantRegistry, TenantRegistry, TenantSummary};
pub use router::{validate_schema_name, Router, RouterError};
pub use secret_store::{DbCreds, EnvStore, SecretStore};
pub use tenant::{TenantId, TenantRouting, TenantStatus, TenantStrategy, PLATFORM_TENANT_ID};
pub use vault_store::VaultStore;
