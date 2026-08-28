//! E2E tests for `TargetType::WaitEvent` (`docs/features/02-workflow-engine.md` Increment 3 —
//! durable pause/resume for a `TargetType::Steps` chain). `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention. Covers the
//! `metap-cron` store layer (`pause_workflow_run`/`dispatch_on_wait_event_*_matches`); the
//! `cron-scheduler::executor::resume_steps`/`trigger` round-trip (actually resuming the chain's
//! remaining steps) is verified live, same posture Increment 1/2's HTTP+RabbitMQ+Postgres round
//! trips were.

use metap_cron::{DispatchMode, NewCronJob, TargetType, TriggerType, WaitEventTargetConfig};
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Row};
use uuid::Uuid;

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(2)
        .connect(&database_url)
        .await
        .unwrap()
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    sqlx::query("DELETE FROM workflow_runs WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM cron_job_runs WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM cron_jobs WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
}

/// Creates a `"steps"` job (one `wait_event` step, matching `crm.customers`/`approved`) plus a
/// real `cron_job_runs` + `workflow_runs` row already paused at that step — the state
/// `pause_workflow_run` would have left a real firing in. Same "create the job, fire it for
/// real via `dispatch_on_transition_matches`" pattern `workflow_runs_postgres.rs`'s
/// `create_steps_job_and_run` uses, plus the extra `start_workflow_run`/`pause_workflow_run`
/// calls `cron-scheduler::executor::run_step_range` would have made. `fire_action` is the
/// **initial** `on_transition` trigger that fires this chain's first dispatch — deliberately a
/// distinct value per call within a test (not related to `wait`'s own action/event) so firing one
/// chain's job doesn't also re-fire an already-created sibling chain's job sharing the same
/// tenant (`dispatch_on_transition_matches` matches every enabled job with a given
/// `(entity, action)`, not just the one this call just created).
async fn create_paused_chain(
    pool: &PgPool,
    tenant_id: Uuid,
    fire_action: &str,
    wait: WaitEventTargetConfig,
) -> (Uuid, Uuid, Uuid) {
    let job = metap_cron::create_job(
        pool,
        tenant_id,
        NewCronJob {
            name: "approval-chain".to_string(),
            trigger_type: TriggerType::OnTransition.as_str().to_string(),
            trigger_config: Some(json!({ "entity": "crm.customers", "action": fire_action })),
            cron_expr: None,
            timezone: "UTC".to_string(),
            target_type: TargetType::Steps.as_str().to_string(),
            target_config: json!({
                "steps": [
                    { "targetType": "wait_event", "targetConfig": serde_json::to_value(&wait).unwrap() },
                    { "targetType": "webhook", "targetConfig": { "url": "https://example.invalid/after-wait" } },
                ]
            }),
            dispatch_mode: DispatchMode::Outbox.as_str().to_string(),
            max_attempts: 1,
            retry_backoff_seconds: 30,
            enabled: true,
        },
        None,
    )
    .await
    .expect("create_job");

    let result =
        metap_cron::dispatch_on_transition_matches(pool, tenant_id, "crm.customers", fire_action, Uuid::new_v4())
            .await
            .expect("dispatch_on_transition_matches");
    assert_eq!(result.claimed, 1);

    let cron_job_run_id = sqlx::query("SELECT id FROM cron_job_runs WHERE job_id = $1")
        .bind(job.id)
        .fetch_one(pool)
        .await
        .expect("cron_job_runs row")
        .get::<Uuid, _>("id");

    let workflow_run_id = metap_cron::start_workflow_run(pool, tenant_id, job.id, cron_job_run_id, 2)
        .await
        .expect("start_workflow_run");

    metap_cron::pause_workflow_run(pool, workflow_run_id, cron_job_run_id, 0, &wait)
        .await
        .expect("pause_workflow_run");

    (job.id, cron_job_run_id, workflow_run_id)
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn pause_workflow_run_marks_both_rows_waiting_with_the_wait_criteria() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let wait = WaitEventTargetConfig {
        entity: "crm.customers".to_string(),
        action: Some("approve".to_string()),
        event: None,
    };
    let (_job_id, cron_job_run_id, workflow_run_id) = create_paused_chain(&pool, tenant_id, "block", wait).await;

    let run = metap_cron::get_workflow_run_by_cron_job_run(&pool, tenant_id, cron_job_run_id)
        .await
        .expect("get_workflow_run_by_cron_job_run")
        .expect("row must exist");
    assert_eq!(run.id, workflow_run_id);
    assert_eq!(run.status, "waiting");
    assert_eq!(run.current_step_index, 0, "still points at the wait step itself");
    assert_eq!(run.wait_entity.as_deref(), Some("crm.customers"));
    assert_eq!(run.wait_action.as_deref(), Some("approve"));
    assert!(run.wait_record_event.is_none());
    assert!(run.finished_at.is_none(), "a waiting run is not finished");

    let cron_job_run_status: String = sqlx::query("SELECT status FROM cron_job_runs WHERE id = $1")
        .bind(cron_job_run_id)
        .fetch_one(&pool)
        .await
        .expect("cron_job_runs row")
        .get("status");
    assert_eq!(cron_job_run_status, "waiting");

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn matching_transition_resumes_the_waiting_chain_and_not_a_non_matching_one() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let (_job_a, run_a, workflow_run_a) = create_paused_chain(
        &pool,
        tenant_id,
        "block-a",
        WaitEventTargetConfig {
            entity: "crm.customers".to_string(),
            action: Some("approve".to_string()),
            event: None,
        },
    )
    .await;
    // A second chain waiting on a *different* action — must not be resumed by the "approve" event.
    let (_job_b, run_b, _workflow_run_b) = create_paused_chain(
        &pool,
        tenant_id,
        "block-b",
        WaitEventTargetConfig {
            entity: "crm.customers".to_string(),
            action: Some("reject".to_string()),
            event: None,
        },
    )
    .await;

    let resuming_record_id = Uuid::new_v4();
    let resumed = metap_cron::dispatch_on_wait_event_transition_matches(
        &pool,
        tenant_id,
        "crm.customers",
        "approve",
        resuming_record_id,
    )
    .await
    .expect("dispatch_on_wait_event_transition_matches");

    assert_eq!(resumed.len(), 1);
    let resumed_run = &resumed[0];
    assert_eq!(resumed_run.cron_job_run_id, run_a);
    assert_eq!(resumed_run.workflow_run_id, workflow_run_a);
    assert_eq!(
        resumed_run.resume_from_step_index, 1,
        "resumes right after the wait step"
    );
    assert_eq!(resumed_run.resuming_record_id, resuming_record_id);
    assert_eq!(resumed_run.resuming_entity, "crm.customers");

    let a_status: String = sqlx::query("SELECT status FROM workflow_runs WHERE id = $1")
        .bind(workflow_run_a)
        .fetch_one(&pool)
        .await
        .expect("row")
        .get("status");
    assert_eq!(a_status, "running", "resumed chain leaves waiting immediately");

    let b_status: String = sqlx::query("SELECT status FROM cron_job_runs WHERE id = $1")
        .bind(run_b)
        .fetch_one(&pool)
        .await
        .expect("row")
        .get("status");
    assert_eq!(b_status, "waiting", "non-matching chain is untouched");

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn matching_record_event_resumes_a_chain_waiting_on_a_record_event() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let (_job, _run, workflow_run_id) = create_paused_chain(
        &pool,
        tenant_id,
        "block",
        WaitEventTargetConfig {
            entity: "crm.customers".to_string(),
            action: None,
            event: Some("updated".to_string()),
        },
    )
    .await;

    let resumed =
        metap_cron::dispatch_on_wait_event_record_matches(&pool, tenant_id, "crm.customers", "updated", Uuid::new_v4())
            .await
            .expect("dispatch_on_wait_event_record_matches");
    assert_eq!(resumed.len(), 1);
    assert_eq!(resumed[0].workflow_run_id, workflow_run_id);

    // A record `updated` event never matches a chain waiting on `wait_action` — cross-kind
    // isolation, not just cross-value.
    let resumed_again =
        metap_cron::dispatch_on_wait_event_record_matches(&pool, tenant_id, "crm.customers", "updated", Uuid::new_v4())
            .await
            .expect("dispatch_on_wait_event_record_matches");
    assert!(
        resumed_again.is_empty(),
        "already-resumed chain (now `running`, not `waiting`) does not match a second time"
    );

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_waiting_chain_never_resumes_for_a_matching_event_in_another_tenant() {
    let pool = connect().await;
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();

    let (_job, _run, _workflow_run_id) = create_paused_chain(
        &pool,
        tenant_a,
        "block",
        WaitEventTargetConfig {
            entity: "crm.customers".to_string(),
            action: Some("approve".to_string()),
            event: None,
        },
    )
    .await;

    let resumed = metap_cron::dispatch_on_wait_event_transition_matches(
        &pool,
        tenant_b,
        "crm.customers",
        "approve",
        Uuid::new_v4(),
    )
    .await
    .expect("dispatch_on_wait_event_transition_matches");
    assert!(
        resumed.is_empty(),
        "tenant B's matching event must not resume tenant A's chain"
    );

    cleanup(&pool, tenant_a).await;
    cleanup(&pool, tenant_b).await;
}
