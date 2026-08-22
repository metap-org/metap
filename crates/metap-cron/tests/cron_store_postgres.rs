//! E2E tests writing real rows via the repo's dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention (unit tests
//! never touch a DB; these run explicitly via `cargo test -- --ignored`). Covers
//! `docs/features/02-workflow-engine.md` Increment 1's acceptance criteria: `on_transition`
//! trigger matching and retry-with-backoff.

use metap_cron::{
    claim_due_retries, dispatch_on_transition_matches, finish_run_with_retry, list_job_runs, DispatchMode, NewCronJob,
    RunStatus, TargetType, TriggerType,
};
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

async fn create_on_transition_job(pool: &PgPool, tenant_id: Uuid, dispatch_mode: DispatchMode) -> Uuid {
    let job = metap_cron::create_job(
        pool,
        tenant_id,
        NewCronJob {
            name: "on-activate".to_string(),
            trigger_type: TriggerType::OnTransition.as_str().to_string(),
            trigger_config: Some(json!({ "entity": "crm.customers", "action": "activate" })),
            cron_expr: None,
            timezone: "UTC".to_string(),
            target_type: TargetType::Webhook.as_str().to_string(),
            target_config: json!({ "url": "https://example.invalid/hook" }),
            dispatch_mode: dispatch_mode.as_str().to_string(),
            max_attempts: 3,
            retry_backoff_seconds: 30,
            enabled: true,
        },
        None,
    )
    .await
    .expect("create_job");
    assert_eq!(job.trigger_type, "on_transition");
    assert!(job.next_run_at.is_none(), "on_transition job must have no schedule");
    job.id
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn on_transition_job_fires_when_entity_and_action_match_outbox_mode() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let job_id = create_on_transition_job(&pool, tenant_id, DispatchMode::Outbox).await;

    let result = dispatch_on_transition_matches(&pool, tenant_id, "crm.customers", "activate")
        .await
        .expect("dispatch_on_transition_matches");
    assert_eq!(result.claimed, 1);
    assert!(
        result.direct_jobs.is_empty(),
        "outbox-mode job must not be a direct job"
    );

    let outbox_row = sqlx::query("SELECT topic, aggregate_id FROM outbox_events WHERE aggregate_id = $1")
        .bind(job_id)
        .fetch_one(&pool)
        .await
        .expect("outbox event must exist");
    assert_eq!(outbox_row.get::<String, _>("topic"), "cron.job.due");

    let runs = list_job_runs(&pool, tenant_id, job_id, 10)
        .await
        .expect("list_job_runs");
    assert_eq!(runs.len(), 1);
    assert_eq!(runs[0].attempt, 1);
    assert_eq!(runs[0].status, "enqueued");

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn on_transition_job_does_not_fire_for_a_non_matching_action() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    create_on_transition_job(&pool, tenant_id, DispatchMode::Outbox).await;

    let result = dispatch_on_transition_matches(&pool, tenant_id, "crm.customers", "block")
        .await
        .expect("dispatch_on_transition_matches");
    assert_eq!(
        result.claimed, 0,
        "action \"block\" must not match a job registered for \"activate\""
    );

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn on_transition_job_does_not_fire_for_another_tenant() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let other_tenant_id = Uuid::new_v4();
    create_on_transition_job(&pool, tenant_id, DispatchMode::Outbox).await;

    let result = dispatch_on_transition_matches(&pool, other_tenant_id, "crm.customers", "activate")
        .await
        .expect("dispatch_on_transition_matches");
    assert_eq!(
        result.claimed, 0,
        "a job registered for one tenant must never fire for another"
    );

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn on_transition_direct_mode_job_is_returned_for_immediate_execution() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    create_on_transition_job(&pool, tenant_id, DispatchMode::Direct).await;

    let result = dispatch_on_transition_matches(&pool, tenant_id, "crm.customers", "activate")
        .await
        .expect("dispatch_on_transition_matches");
    assert_eq!(result.claimed, 1);
    assert_eq!(
        result.direct_jobs.len(),
        1,
        "direct-mode job must come back as a direct job, not an outbox event"
    );
    assert_eq!(result.direct_jobs[0].attempt, 1);
    assert_eq!(result.direct_jobs[0].max_attempts, 3);

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn failed_run_with_attempts_remaining_schedules_a_retry_that_claim_due_retries_picks_up() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let job_id = create_on_transition_job(&pool, tenant_id, DispatchMode::Outbox).await;

    let result = dispatch_on_transition_matches(&pool, tenant_id, "crm.customers", "activate")
        .await
        .expect("dispatch_on_transition_matches");
    let run_id = sqlx::query("SELECT id FROM cron_job_runs WHERE job_id = $1")
        .bind(job_id)
        .fetch_one(&pool)
        .await
        .expect("run row")
        .get::<Uuid, _>("id");
    assert_eq!(result.claimed, 1);

    let payload = metap_cron::CronJobDuePayload {
        run_id,
        job_id,
        tenant_id,
        target_type: TargetType::Webhook.as_str().to_string(),
        target_config: json!({}),
        attempt: 1,
        max_attempts: 3,
        retry_backoff_seconds: 1,
        dispatch_mode: DispatchMode::Outbox.as_str().to_string(),
    };
    finish_run_with_retry(&pool, &payload, RunStatus::Failed, Some("connection refused"), None)
        .await
        .expect("finish_run_with_retry");

    let runs = list_job_runs(&pool, tenant_id, job_id, 10)
        .await
        .expect("list_job_runs");
    assert_eq!(runs.len(), 2, "original attempt + one scheduled retry");
    let retry = runs.iter().find(|r| r.attempt == 2).expect("attempt 2 must exist");
    assert_eq!(retry.status, "enqueued");

    // `retry_backoff_seconds: 1` in the payload above — the retry's `scheduled_for` is
    // ~1s out, so claiming "due now" finds nothing yet...
    let too_early = claim_due_retries(&pool, chrono::Utc::now(), 10)
        .await
        .expect("claim_due_retries");
    assert_eq!(
        too_early.claimed, 0,
        "retry must not be claimable before its backoff window elapses"
    );

    // ...but claiming "due 2s from now" does.
    let now_plus_2s = chrono::Utc::now() + chrono::Duration::seconds(2);
    let due = claim_due_retries(&pool, now_plus_2s, 10)
        .await
        .expect("claim_due_retries");
    assert_eq!(due.claimed, 1);

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn failed_run_with_no_attempts_remaining_does_not_schedule_a_retry() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let job_id = create_on_transition_job(&pool, tenant_id, DispatchMode::Outbox).await;

    dispatch_on_transition_matches(&pool, tenant_id, "crm.customers", "activate")
        .await
        .expect("dispatch_on_transition_matches");
    let run_id = sqlx::query("SELECT id FROM cron_job_runs WHERE job_id = $1")
        .bind(job_id)
        .fetch_one(&pool)
        .await
        .expect("run row")
        .get::<Uuid, _>("id");

    let payload = metap_cron::CronJobDuePayload {
        run_id,
        job_id,
        tenant_id,
        target_type: TargetType::Webhook.as_str().to_string(),
        target_config: json!({}),
        attempt: 3, // == max_attempts
        max_attempts: 3,
        retry_backoff_seconds: 1,
        dispatch_mode: DispatchMode::Outbox.as_str().to_string(),
    };
    finish_run_with_retry(&pool, &payload, RunStatus::Failed, Some("still failing"), None)
        .await
        .expect("finish_run_with_retry");

    let runs = list_job_runs(&pool, tenant_id, job_id, 10)
        .await
        .expect("list_job_runs");
    assert_eq!(runs.len(), 1, "no retry once max_attempts is exhausted");
    assert_eq!(runs[0].status, "failed");

    cleanup(&pool, tenant_id).await;
}
