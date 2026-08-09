//! `cron-scheduler`: the dispatch loop for `crates/metap-cron`'s job definitions
//! (`docs/roadmap.md` Phase 13). Two halves, run concurrently by `src/main.rs`:
//! `ticker::run_ticker` polls `cron_jobs` for due entries and writes a `cron.job.due` outbox
//! event per firing (reusing the existing `outbox-publisher` to actually get it onto
//! RabbitMQ — this crate never publishes directly); `executor::run_executor` subscribes to
//! that routing key and runs the job. Split into two functions (not two binaries) because
//! nothing else needs to consume `cron.job.due` independently the way `notification-worker`
//! consumes `#.workflow.transitioned` — there's exactly one thing that ever needs to react to
//! a job becoming due.

pub mod executor;
pub mod ticker;

pub use executor::{execute, run_executor, ExecutorConfig};
pub use ticker::{run_ticker, TickerConfig};
