//! Metadata-driven scheduled jobs (`docs/roadmap.md` Phase 13): an operator defines a
//! recurring job (schedule + target action) through the admin API, the same way
//! entities/workflow/policies are defined today, instead of a developer hand-wiring a new
//! cron entry in code. This crate owns the job definition/run-history storage and the
//! due-job-claiming logic; the actual dispatch loop (ticker + executor) lives in the
//! `crates/cron-scheduler` ops binary built on top of it — kept separate so this crate stays
//! a plain library with no `tokio::main`/process-lifecycle concerns, matching
//! `metap-permission`/`metap-workflow`'s shape.

pub mod model;
pub mod schedule;
pub mod store;

pub use model::{
    ClaimedDirectJob, CronJob, CronJobDuePayload, CronJobRun, DispatchMode, RunStatus, TargetType, ROUTING_KEY,
};
pub use schedule::{next_run_at, validate as validate_schedule};
pub use store::{
    claim_due_jobs, create_job, delete_job, finish_run, get_job, list_job_runs, list_jobs, start_run, update_job,
    ClaimResult, JobUpdate, NewCronJob,
};
