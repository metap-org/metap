//! E2E tests for `workflow_runs` (`docs/features/02-workflow-engine.md` Increment 2 — the
//! `TargetType::Steps` chained-activity progress table). `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use metap_cron::{DispatchMode, NewCronJob, TargetType, TriggerType};
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

/// Creates a `"steps"` job plus one real `cron_job_runs` row (via `dispatch_on_transition_matches`,
/// same as `cron_store_postgres.rs`'s helpers) to satisfy `workflow_runs.cron_job_run_id`'s FK —
/// `start_workflow_run` is only ever called against a real firing in production.
async fn create_steps_job_and_run(pool: &PgPool, tenant_id: Uuid) -> (Uuid, Uuid) {
    let job = metap_cron::create_job(
        pool,
        tenant_id,
        NewCronJob {
            name: "on-block-chain".to_string(),
            trigger_type: TriggerType::OnTransition.as_str().to_string(),
            trigger_config: Some(json!({ "entity": "crm.customers", "action": "block" })),
            cron_expr: None,
            timezone: "UTC".to_string(),
            target_type: TargetType::Steps.as_str().to_string(),
            target_config: json!({
                "steps": [
                    { "targetType": "webhook", "targetConfig": { "url": "https://example.invalid/step0" } },
                    { "targetType": "webhook", "targetConfig": { "url": "https://example.invalid/step1" } },
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

    let result = metap_cron::dispatch_on_transition_matches(pool, tenant_id, "crm.customers", "block", Uuid::new_v4())
        .await
        .expect("dispatch_on_transition_matches");
    assert_eq!(result.claimed, 1);

    let run_id = sqlx::query("SELECT id FROM cron_job_runs WHERE job_id = $1")
        .bind(job.id)
        .fetch_one(pool)
        .await
        .expect("cron_job_runs row")
        .get::<Uuid, _>("id");

    (job.id, run_id)
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn start_workflow_run_creates_a_running_row_with_zero_progress() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let (job_id, cron_job_run_id) = create_steps_job_and_run(&pool, tenant_id).await;

    let workflow_run_id = metap_cron::start_workflow_run(&pool, tenant_id, job_id, cron_job_run_id, 2)
        .await
        .expect("start_workflow_run");

    let run = metap_cron::get_workflow_run_by_cron_job_run(&pool, tenant_id, cron_job_run_id)
        .await
        .expect("get_workflow_run_by_cron_job_run")
        .expect("row must exist");
    assert_eq!(run.id, workflow_run_id);
    assert_eq!(run.job_id, job_id);
    assert_eq!(run.status, "running");
    assert_eq!(run.current_step_index, 0);
    assert_eq!(run.total_steps, 2);
    assert_eq!(run.context, json!({}));
    assert!(run.error.is_none());
    assert!(run.finished_at.is_none());

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn happy_path_chain_advances_through_every_step_then_finishes_success() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let (job_id, cron_job_run_id) = create_steps_job_and_run(&pool, tenant_id).await;
    let workflow_run_id = metap_cron::start_workflow_run(&pool, tenant_id, job_id, cron_job_run_id, 2)
        .await
        .expect("start_workflow_run");

    metap_cron::advance_workflow_run(&pool, workflow_run_id, 0, &json!({ "status": 200 }))
        .await
        .expect("advance step 0");
    let mid = metap_cron::get_workflow_run_by_cron_job_run(&pool, tenant_id, cron_job_run_id)
        .await
        .expect("lookup")
        .expect("row must exist");
    assert_eq!(mid.status, "running", "chain is not finished after step 0 of 2");
    assert_eq!(mid.current_step_index, 1);
    assert_eq!(mid.context, json!({ "step_0": { "status": 200 } }));

    metap_cron::advance_workflow_run(&pool, workflow_run_id, 1, &json!({ "status": 201 }))
        .await
        .expect("advance step 1");
    metap_cron::finish_workflow_run(&pool, workflow_run_id)
        .await
        .expect("finish_workflow_run");

    let done = metap_cron::get_workflow_run_by_cron_job_run(&pool, tenant_id, cron_job_run_id)
        .await
        .expect("lookup")
        .expect("row must exist");
    assert_eq!(done.status, "success");
    assert_eq!(done.current_step_index, 2);
    assert_eq!(
        done.context,
        json!({ "step_0": { "status": 200 }, "step_1": { "status": 201 } })
    );
    assert!(done.finished_at.is_some());

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_step_failure_stops_the_chain_and_records_which_step_and_why() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let (job_id, cron_job_run_id) = create_steps_job_and_run(&pool, tenant_id).await;
    let workflow_run_id = metap_cron::start_workflow_run(&pool, tenant_id, job_id, cron_job_run_id, 2)
        .await
        .expect("start_workflow_run");

    metap_cron::advance_workflow_run(&pool, workflow_run_id, 0, &json!({ "status": 200 }))
        .await
        .expect("advance step 0");
    metap_cron::fail_workflow_run(&pool, workflow_run_id, 1, "step 1 (webhook) failed: connection refused")
        .await
        .expect("fail_workflow_run");

    let run = metap_cron::get_workflow_run_by_cron_job_run(&pool, tenant_id, cron_job_run_id)
        .await
        .expect("lookup")
        .expect("row must exist");
    assert_eq!(run.status, "failed");
    assert_eq!(
        run.current_step_index, 1,
        "current_step_index stays pointed at the failed step, not advanced past it"
    );
    assert_eq!(run.context, json!({ "step_0": { "status": 200 } }), "step 0's result is still recorded");
    assert_eq!(run.error.as_deref(), Some("step 1 (webhook) failed: connection refused"));
    assert!(run.finished_at.is_some());

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn get_workflow_run_returns_none_for_a_cron_job_run_with_no_workflow_run() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    // A plain (non-"steps") job never gets a workflow_runs row at all.
    let job = metap_cron::create_job(
        &pool,
        tenant_id,
        NewCronJob {
            name: "plain-webhook".to_string(),
            trigger_type: TriggerType::OnTransition.as_str().to_string(),
            trigger_config: Some(json!({ "entity": "crm.customers", "action": "activate" })),
            cron_expr: None,
            timezone: "UTC".to_string(),
            target_type: TargetType::Webhook.as_str().to_string(),
            target_config: json!({ "url": "https://example.invalid/hook" }),
            dispatch_mode: DispatchMode::Outbox.as_str().to_string(),
            max_attempts: 1,
            retry_backoff_seconds: 30,
            enabled: true,
        },
        None,
    )
    .await
    .expect("create_job");
    let result = metap_cron::dispatch_on_transition_matches(&pool, tenant_id, "crm.customers", "activate", Uuid::new_v4())
        .await
        .expect("dispatch_on_transition_matches");
    assert_eq!(result.claimed, 1);
    let cron_job_run_id = sqlx::query("SELECT id FROM cron_job_runs WHERE job_id = $1")
        .bind(job.id)
        .fetch_one(&pool)
        .await
        .expect("cron_job_runs row")
        .get::<Uuid, _>("id");

    let run = metap_cron::get_workflow_run_by_cron_job_run(&pool, tenant_id, cron_job_run_id)
        .await
        .expect("get_workflow_run_by_cron_job_run");
    assert!(run.is_none());

    cleanup(&pool, tenant_id).await;
}
