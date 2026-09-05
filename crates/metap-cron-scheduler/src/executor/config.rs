//! `ExecutorConfig`/`SmtpConfig` — everything `super::dispatch`'s handlers need that isn't
//! itself a job's `target_config` (service token, callback base URL, SMTP settings).

/// No `Debug`: `secrets` is a `dyn SecretStore` (not printable), and a config struct holding the
/// handle to every tenant's credentials is not something to make trivially loggable anyway.
#[derive(Clone)]
pub struct ExecutorConfig {
    /// Base URL of the `crm-server` instance `workflow_transition`/`bulk_query_action` jobs
    /// call back into.
    pub target_base_url: String,
    /// A service-account token this process logged into `crm-server`'s own `POST /auth/login`
    /// with, kept fresh in the background (`metap_runtime::service_token::ServiceTokenSource`) —
    /// replaced a static, hand-minted-once JWT (`CRON_SERVICE_JWT`) 2026-09-02, the same fix
    /// `graphql-gateway` got the same day after that exact pattern's 1h TTL expired mid-deployment
    /// and crashed a caller at boot (see `metap-grpc/src/client.rs`'s doc comment for that
    /// incident). Its `tenantId` claim fixes which tenant's jobs this executor can actually run:
    /// `crm-server` resolves tenant scope from the token alone, never from a caller-supplied
    /// value, so a job whose `tenant_id` doesn't match this account's tenant fails at execution
    /// time (record/entity not found) rather than silently crossing tenants — see
    /// `docs/roadmap.md` Phase 13 for this constraint.
    pub service_token: metap_runtime::service_token::ServiceTokenSource,
    /// SMTP settings for `TargetType::Email` jobs (`run_email`) — `metap_infra::AppConfig`'s
    /// `smtp_*` fields, carried in `ExecutorConfig` rather than read from env again here so
    /// this stays testable/constructible without touching the environment.
    pub smtp: SmtpConfig,
    /// Guardrails for `TargetType::Webhook`'s tenant-supplied target URL — see
    /// `super::ssrf_guard`. Carried here rather than read from env inside `run_webhook` for the
    /// same reason `smtp` is: this stays constructible/testable without touching the environment.
    pub webhook: crate::executor::WebhookPolicy,
    /// Resolves a tenant's stored webhook credential (`docs/features/18-config-tiers-db-backed.md`
    /// slice 3) — the same `SecretStore` backend `metap_control::build_secret_store` picks for
    /// every other process, so a deployment on Vault does not find this one reading somewhere else.
    ///
    /// `None` means no credential can be resolved and a job that asks for one fails saying so. That
    /// is the honest failure: a webhook silently sent *without* the `Authorization` it was
    /// configured to carry would look to the tenant like the upstream rejecting them.
    pub secrets: Option<std::sync::Arc<dyn metap_control::SecretStore>>,
}

#[derive(Debug, Clone, Default)]
pub struct SmtpConfig {
    pub host: Option<String>,
    pub port: u16,
    pub user: Option<String>,
    pub password: Option<String>,
    pub from: Option<String>,
}
