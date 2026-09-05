//! Consumes `cron.job.due` (`crates/metap-cron`'s `ROUTING_KEY`) and actually runs the job.
//! `workflow_transition`/`bulk_query_action` call back into the owning `crm-server`'s own
//! `/api/:entity/...` HTTP surface — reusing its permission checks, field validation,
//! optimistic-locking, and workflow audit trail for free — rather than this binary linking
//! `metap-crud`/`metap-metadata` directly, which would give an ops binary business-entity
//! knowledge (`CLAUDE.md`'s boundary rules forbid that). `webhook` calls an arbitrary
//! external URL instead.
//!
//! Split into one file per `TargetType` (`workflow_transition`/`webhook`/`email`) plus
//! `config` (`ExecutorConfig`/`SmtpConfig`), `dispatch` (the consume loop and per-`TargetType`
//! router), and `steps` (`TargetType::Steps`/`TargetType::WaitEvent` chain execution) — purely
//! to keep each file a manageable size. Every item this module used to export directly is
//! re-exported here unchanged.

mod config;
mod dispatch;
mod email;
mod ssrf_guard;
mod steps;
mod webhook;
mod workflow_transition;

pub use config::{ExecutorConfig, SmtpConfig};
pub use dispatch::{execute, run_executor, QUEUE};
pub use ssrf_guard::WebhookPolicy;
pub use steps::resume_steps;
