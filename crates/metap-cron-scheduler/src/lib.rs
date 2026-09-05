//! `cron-scheduler`: the dispatch loop for `crates/metap-cron`'s job definitions
//! (`docs/roadmap.md` Phase 13, `docs/features/02-workflow-engine.md` Increment 1). Three
//! parts, run concurrently by `src/main.rs`: `ticker::run_ticker` polls `cron_jobs` for
//! schedule-due entries (and `cron_job_runs` for retries due) and writes a `cron.job.due`
//! outbox event per firing (reusing the existing `outbox-publisher` to actually get it onto
//! RabbitMQ — this crate never publishes directly); `executor::run_executor` subscribes to
//! that routing key and runs the job; `trigger::run_trigger_listener` subscribes to
//! `#.workflow.transitioned` and fires `trigger_type = "on_transition"` jobs whose
//! `trigger_config` matches. Split into separate functions (not separate binaries) because
//! nothing else needs to consume `cron.job.due` independently the way `notification-worker`
//! consumes `#.workflow.transitioned` — this binary already needs its own subscriber to that
//! routing key for `on_transition` triggers, so it's the same process either way.

pub mod executor;
pub mod ticker;
pub mod trigger;

pub use executor::{execute, run_executor, ExecutorConfig, SmtpConfig};
pub use ticker::{run_ticker, TickerConfig};
pub use trigger::run_trigger_listener;
