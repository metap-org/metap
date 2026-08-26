//! E2E tests for `orchestrator` against a real dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use metap_reconciler::orchestrator::{
    advance_wave, claim_due, record_failure, record_success, run_claimed_batch, WaveDecision,
};
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(20)
        .connect(&database_url)
        .await
        .unwrap()
}

async fn seed_pending(pool: &PgPool, entity_name: &str, tenant_ids: &[Uuid], desired_version: i64) {
    for tenant_id in tenant_ids {
        sqlx::query(
            "INSERT INTO reconciler_entity_deployments (tenant_id, entity_name, desired_version) VALUES ($1, $2, $3)
             ON CONFLICT (tenant_id, entity_name) DO UPDATE SET desired_version = EXCLUDED.desired_version, status = 'pending', attempts = 0",
        )
        .bind(tenant_id)
        .bind(entity_name)
        .bind(desired_version)
        .execute(pool)
        .await
        .unwrap();
    }
}

async fn cleanup(pool: &PgPool, entity_name: &str) {
    sqlx::query("DELETE FROM reconciler_entity_deployments WHERE entity_name = $1")
        .bind(entity_name)
        .execute(pool)
        .await
        .unwrap();
}

/// §6.2's core promise: N workers calling `claim_due` concurrently for overlapping work must
/// never claim the same `(tenant, entity)` twice, and together must claim exactly the pending
/// set — no row left behind, none double-counted.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn concurrent_claim_due_never_double_claims() {
    let pool = connect().await;
    let entity_name = "test.orchestrator_concurrent_claim";
    cleanup(&pool, entity_name).await;

    let tenant_ids: Vec<Uuid> = (0..40).map(|_| Uuid::new_v4()).collect();
    seed_pending(&pool, entity_name, &tenant_ids, 1).await;

    let mut handles = Vec::new();
    for i in 0..8 {
        let pool = pool.clone();
        let entity_name = entity_name.to_string();
        handles.push(tokio::spawn(async move {
            claim_due(&pool, &format!("worker-{i}"), Some(&entity_name), 5, 10)
                .await
                .unwrap()
        }));
    }
    let mut all_claimed = Vec::new();
    for h in handles {
        all_claimed.extend(h.await.unwrap());
    }

    let mut claimed_ids: Vec<Uuid> = all_claimed.iter().map(|c| c.tenant_id).collect();
    claimed_ids.sort();
    let mut expected = tenant_ids.clone();
    expected.sort();
    assert_eq!(
        claimed_ids, expected,
        "every pending row must be claimed exactly once, across all workers"
    );

    cleanup(&pool, entity_name).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn claimed_entity_is_not_reclaimed_until_failed_or_requeued() {
    let pool = connect().await;
    let entity_name = "test.orchestrator_no_reclaim";
    cleanup(&pool, entity_name).await;
    let tenant_id = Uuid::new_v4();
    seed_pending(&pool, entity_name, &[tenant_id], 1).await;

    let first = claim_due(&pool, "w1", Some(entity_name), 5, 10).await.unwrap();
    assert_eq!(first.len(), 1);
    let second = claim_due(&pool, "w2", Some(entity_name), 5, 10).await.unwrap();
    assert!(second.is_empty(), "a 'running' row must not be claimable again");

    cleanup(&pool, entity_name).await;
}

/// Regression for the finding in `AUDIT_2.md`: `lease_heartbeat` was written at claim time and
/// never read back anywhere — a worker that crashed mid-`reconcile_one` (after `claim_due`,
/// before `record_success`/`record_failure`) left that row `'running'` forever, permanently
/// excluded from `claim_due`'s own claim filter. `claim_due` now also reclaims a `'running'` row
/// whose `lease_heartbeat` is older than `LEASE_STALE_AFTER_MINUTES`.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn stale_running_lease_is_reclaimed() {
    let pool = connect().await;
    let entity_name = "test.orchestrator_stale_reclaim";
    cleanup(&pool, entity_name).await;
    let tenant_id = Uuid::new_v4();
    seed_pending(&pool, entity_name, &[tenant_id], 1).await;

    let first = claim_due(&pool, "w1", Some(entity_name), 5, 10).await.unwrap();
    assert_eq!(first.len(), 1, "first claim picks up the pending row");

    let too_soon = claim_due(&pool, "w2", Some(entity_name), 5, 10).await.unwrap();
    assert!(too_soon.is_empty(), "a fresh lease must not be reclaimed immediately");

    // Simulate a worker that crashed mid-`reconcile_one`: the lease is never refreshed and
    // neither `record_success` nor `record_failure` ever runs, so `status` stays `'running'`.
    sqlx::query(
        "UPDATE reconciler_entity_deployments SET lease_heartbeat = now() - interval '2 hours' \
         WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .execute(&pool)
    .await
    .unwrap();

    let reclaimed = claim_due(&pool, "w3", Some(entity_name), 5, 10).await.unwrap();
    assert_eq!(
        reclaimed.len(),
        1,
        "a lease stale past LEASE_STALE_AFTER_MINUTES must be reclaimed"
    );
    assert_eq!(reclaimed[0].tenant_id, tenant_id);

    cleanup(&pool, entity_name).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn record_success_and_failure_update_status_correctly() {
    let pool = connect().await;
    let entity_name = "test.orchestrator_record_outcome";
    cleanup(&pool, entity_name).await;
    let ok_tenant = Uuid::new_v4();
    let fail_tenant = Uuid::new_v4();
    seed_pending(&pool, entity_name, &[ok_tenant, fail_tenant], 1).await;

    claim_due(&pool, "w1", Some(entity_name), 5, 10).await.unwrap();

    record_success(&pool, ok_tenant, entity_name, 1).await.unwrap();
    let (status, applied): (String, Option<i64>) = sqlx::query_as(
        "SELECT status, applied_version FROM reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(ok_tenant)
    .bind(entity_name)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(status, "done");
    assert_eq!(applied, Some(1));

    let fake_data_error = anyhow::anyhow!("boom").context("outer");
    // classify_error only recognizes a real sqlx::Error in the chain; a plain anyhow error
    // falls through to Fatal, which is the behavior under test here (blocked, not retried).
    let class = record_failure(&pool, fail_tenant, entity_name, &fake_data_error)
        .await
        .unwrap();
    assert_eq!(class, metap_reconciler::orchestrator::FailureClass::Fatal);
    let status: String = sqlx::query_scalar(
        "SELECT status FROM reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(fail_tenant)
    .bind(entity_name)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(status, "blocked");

    // A blocked entity must not be claimable again automatically.
    let reclaim = claim_due(&pool, "w2", Some(entity_name), 5, 10).await.unwrap();
    assert!(reclaim.is_empty());

    cleanup(&pool, entity_name).await;
}

/// §6.4: one entity's failure inside a batch must not stop or affect any other entity's
/// outcome.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn run_claimed_batch_isolates_per_entity_failures() {
    let pool = connect().await;
    let entity_name = "test.orchestrator_batch_isolation";
    cleanup(&pool, entity_name).await;
    let ok_tenant = Uuid::new_v4();
    let fail_tenant = Uuid::new_v4();
    seed_pending(&pool, entity_name, &[ok_tenant, fail_tenant], 1).await;

    let claimed = claim_due(&pool, "w1", Some(entity_name), 5, 10).await.unwrap();
    assert_eq!(claimed.len(), 2);

    let outcomes = run_claimed_batch(&pool, claimed, 4, |entity| async move {
        if entity.tenant_id == fail_tenant {
            anyhow::bail!("simulated failure for this tenant only")
        } else {
            Ok(())
        }
    })
    .await;

    assert_eq!(outcomes.len(), 2);
    for outcome in &outcomes {
        if outcome.entity.tenant_id == ok_tenant {
            assert!(outcome.result.is_ok());
        } else {
            assert!(outcome.result.is_err());
        }
    }

    let ok_status: String = sqlx::query_scalar(
        "SELECT status FROM reconciler_entity_deployments WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(ok_tenant)
    .bind(entity_name)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(ok_status, "done");

    cleanup(&pool, entity_name).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn advance_wave_halts_when_prior_wave_error_rate_too_high() {
    let pool = connect().await;
    let entity_name = "test.orchestrator_wave_halt";
    cleanup(&pool, entity_name).await;
    let tenants: Vec<Uuid> = (0..10).map(|_| Uuid::new_v4()).collect();

    // Wave 0: canary (2 tenants) advances.
    let decision = advance_wave(&pool, entity_name, 5, &tenants, 0, 10).await.unwrap();
    assert_eq!(decision, WaveDecision::Advanced { tenants_in_wave: 2 });

    // Simulate both canary tenants failing badly.
    for tenant_id in &tenants[..2] {
        sqlx::query(
            "UPDATE reconciler_entity_deployments SET status = 'blocked' WHERE tenant_id = $1 AND entity_name = $2",
        )
        .bind(tenant_id)
        .bind(entity_name)
        .execute(&pool)
        .await
        .unwrap();
    }

    // Wave 1 must halt — 100% of the wave-0 cohort is blocked, way past a 10% threshold.
    let decision = advance_wave(&pool, entity_name, 5, &tenants, 1, 10).await.unwrap();
    assert!(
        matches!(decision, WaveDecision::Halted { .. }),
        "expected halt, got {decision:?}"
    );

    cleanup(&pool, entity_name).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn advance_wave_proceeds_when_prior_wave_is_healthy() {
    let pool = connect().await;
    let entity_name = "test.orchestrator_wave_healthy";
    cleanup(&pool, entity_name).await;
    let tenants: Vec<Uuid> = (0..10).map(|_| Uuid::new_v4()).collect();

    let w0 = advance_wave(&pool, entity_name, 5, &tenants, 0, 10).await.unwrap();
    assert_eq!(w0, WaveDecision::Advanced { tenants_in_wave: 2 });
    for tenant_id in &tenants[..2] {
        sqlx::query("UPDATE reconciler_entity_deployments SET status = 'done', applied_version = 5 WHERE tenant_id = $1 AND entity_name = $2")
            .bind(tenant_id)
            .bind(entity_name)
            .execute(&pool)
            .await
            .unwrap();
    }

    let w1 = advance_wave(&pool, entity_name, 5, &tenants, 1, 10).await.unwrap();
    assert_eq!(w1, WaveDecision::Advanced { tenants_in_wave: 2 }); // 5% of 10 = 1, canary floor 2 wins

    cleanup(&pool, entity_name).await;
}
