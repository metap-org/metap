//! Control-plane skeleton for SaaS multi-tenancy (`docs/multi-tenant-platform-design.md` §2,
//! `docs/architectures/09-adr.md`'s tenant-isolation decision) — `Router` is the seam
//! `metap-crud`'s `CrudService` opens every tenant-scoped transaction through, instead of a bare
//! `PgPool`. See `Router::begin`'s doc comment for the unregistered-tenant compatibility
//! fallback this stage relies on. No HTTP, no business-entity knowledge — a plain library, same
//! shape as `metap-permission`.

mod auth_context;
mod aws_secrets_manager_store;
mod cache;
mod gcp_secret_manager_store;
mod hostname;
mod policy_store;
mod provisioning;
mod registry;
mod router;
mod secret_store;
mod secret_store_factory;
mod tenant;
mod vault_store;

pub use auth_context::{resolve_request_context, ContextAttributesCache};
pub use aws_secrets_manager_store::{AwsSecretsManagerStore, AwsSecretsManagerStoreConfig};
pub use cache::RegistryCache;
pub use gcp_secret_manager_store::GcpSecretManagerStore;
pub use hostname::{normalize_hostname, set_tenant_hostname, tenant_id_for_hostname};
pub use policy_store::PostgresPolicyStore;
pub use provisioning::{provision_dedicated_db_tenant, provision_schema_tenant, ProvisionedTenant};
pub use registry::{PostgresTenantRegistry, TenantRegistry, TenantSummary};
pub use router::{validate_schema_name, Router, RouterError};
pub use secret_store::{tenant_secret_ref, DbCreds, EnvStore, SecretStore};
pub use secret_store_factory::build_secret_store;
pub use tenant::{TenantId, TenantRouting, TenantStatus, TenantStrategy, PLATFORM_TENANT_ID};
pub use vault_store::VaultStore;
