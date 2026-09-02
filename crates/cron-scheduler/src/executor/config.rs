//! `ExecutorConfig`/`SmtpConfig` — everything `super::dispatch`'s handlers need that isn't
//! itself a job's `target_config` (service token, callback base URL, SMTP settings).

#[derive(Debug, Clone)]
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
}

#[derive(Debug, Clone, Default)]
pub struct SmtpConfig {
    pub host: Option<String>,
    pub port: u16,
    pub user: Option<String>,
    pub password: Option<String>,
    pub from: Option<String>,
}
