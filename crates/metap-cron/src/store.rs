//! Postgres-backed CRUD for `cron_jobs`/`cron_job_runs`, plus `claim_due_jobs` — the ticker's
//! whole job (see that function's doc comment). Plain `&PgPool` functions, not a trait —
//! matches `metap_peripherals::role_assignment`'s style, not `PolicyStore`'s: there's no
//! pluggable-storage requirement here the way there was for policies (see
//! `docs/architectures/09-adr.md`).
//!
//! Split into one file per concern (`job_crud`/`dispatch`/`run_lifecycle`/`workflow_run`) purely
//! to keep each file a manageable size — every item this module used to export directly is
//! re-exported here unchanged, so no downstream `metap_cron::store::*`/`metap_cron::*` caller
//! needs to change.

mod dispatch;
mod job_crud;
mod run_lifecycle;
mod workflow_run;

pub use dispatch::{
    claim_due_jobs, claim_due_retries, dispatch_on_record_event_matches, dispatch_on_transition_matches, ClaimResult,
};
pub use job_crud::{create_job, delete_job, get_job, list_job_runs, list_jobs, update_job, JobUpdate, NewCronJob};
pub use run_lifecycle::{finish_run, finish_run_with_retry, run_status, start_run};
pub use workflow_run::{
    advance_workflow_run, dispatch_on_wait_event_record_matches, dispatch_on_wait_event_transition_matches,
    fail_workflow_run, finish_workflow_run, get_workflow_run_by_cron_job_run, pause_workflow_run, start_workflow_run,
};
