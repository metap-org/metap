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
    ClaimedDirectJob, CronJob, CronJobDuePayload, CronJobRun, DispatchMode, OnRecordEventTriggerConfig,
    OnTransitionTriggerConfig, ResumedWorkflowRun, RunStatus, TargetType, TriggerType, WaitEventTargetConfig,
    WorkflowRun, WorkflowRunStatus, ROUTING_KEY,
};
pub use schedule::{next_run_at, validate as validate_schedule};
pub use store::{
    advance_workflow_run, claim_due_jobs, claim_due_retries, create_job, delete_job, dispatch_on_record_event_matches,
    dispatch_on_transition_matches, dispatch_on_wait_event_record_matches, dispatch_on_wait_event_transition_matches,
    fail_workflow_run, finish_run, finish_run_with_retry, finish_workflow_run, get_job,
    get_workflow_run_by_cron_job_run, list_job_runs, list_jobs, pause_workflow_run, run_status, start_run,
    start_workflow_run, update_job, ClaimResult, JobUpdate, NewCronJob,
};
