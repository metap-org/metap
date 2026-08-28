//! `ExecutorConfig`/`SmtpConfig` — everything `super::dispatch`'s handlers need that isn't
//! itself a job's `target_config` (service JWT, callback base URL, SMTP settings).

#[derive(Debug, Clone)]
pub struct ExecutorConfig {
    /// Base URL of the `crm-server` instance `workflow_transition`/`bulk_query_action` jobs
    /// call back into.
    pub target_base_url: String,
    /// A pre-minted JWT (`pnpm mint-token`) for a service account with whatever role the jobs
    /// it will run need. Its `tenantId` claim fixes which tenant's jobs this executor can
    /// actually run: `crm-server` resolves tenant scope from the token alone, never from a
    /// caller-supplied value, so a job whose `tenant_id` doesn't match this token's tenant
    /// fails at execution time (record/entity not found) rather than silently crossing
    /// tenants — see `docs/roadmap.md` Phase 13 for this constraint.
    pub service_jwt: String,
    /// SMTP settings for `TargetType::Email` jobs (`run_email`) — `metap_infra::AppConfig`'s
    /// `smtp_*` fields, carried in `ExecutorConfig` rather than read from env again here so
    /// this stays testable/constructible without touching the environment.
    pub smtp: SmtpConfig,
}

#[derive(Debug, Clone, Default)]
pub struct SmtpConfig {
    pub host: Option<String>,
    pub port: u16,
    pub user: Option<String>,
    pub password: Option<String>,
    pub from: Option<String>,
}
