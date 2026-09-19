//! **Chaos test — real fault injection, not a mock.** Stops and restarts a real RabbitMQ
//! container by name via the `docker` CLI mid-run, proving `outbox_publisher::run`'s
//! reconnect-with-backoff path (`metap_infra::backoff_delay`/`rabbitmq_connector`) actually
//! recovers — this exact path had zero test coverage anywhere in the workspace before this file
//! (confirmed by survey: `crates/metap-infra/src/event_bus/resilient.rs`'s own module has none
//! either). See `testing/disruptive/README.md` for the philosophy (only ever targets this
//! workspace's own dev containers, never shared/production infra) and the full scenario table.
//!
//! `#[ignore]`d like every other e2e test in this repo, **plus** needs the `docker` CLI on PATH
//! and permission to stop/start `metap-rabbitmq-1` — a stricter precondition than the usual
//! "just needs DATABASE_URL", flagged explicitly rather than failing with a confusing error.
//!
//! Run: `docker compose up -d postgres rabbitmq` (from the `metap` repo root), then
//! `DATABASE_URL=postgres://metap:metap@localhost:5433/metap \
//!  RABBITMQ_URL=amqp://metap:metap@localhost:5672 \
//!  cargo test -p metap-outbox-publisher --test chaos_postgres -- --ignored --nocapture`.

use std::process::Command;
use std::time::{Duration, Instant};

use metap_infra::{enqueue_outbox_event, rabbitmq_connector, OutboxEvent};
use outbox_publisher::run;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

/// Default Compose project-derived name for the shared dev RabbitMQ (`docker-compose.yml`, no
/// `container_name:` set, so Compose's own `<project>-<service>-1` convention applies) — this
/// test only ever targets this one container, never anything a caller points `RABBITMQ_URL` at
/// that isn't this exact dev container (see testing/disruptive/README.md's blast-radius rule).
const RABBITMQ_CONTAINER: &str = "metap-rabbitmq-1";

async fn connect_postgres() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this chaos test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
}

fn docker(args: &[&str]) {
    let status = Command::new("docker")
        .args(args)
        .status()
        .unwrap_or_else(|e| panic!("docker CLI must be on PATH for this chaos test: {e}"));
    assert!(status.success(), "docker {args:?} exited non-zero");
}

#[tokio::test]
#[ignore = "chaos: stops/starts a real RabbitMQ container by name (metap-rabbitmq-1) — needs `docker` on PATH, see testing/disruptive/README.md"]
async fn outbox_publisher_recovers_after_rabbitmq_restarts_mid_run() {
    let pool = connect_postgres().await;
    let rabbitmq_url = std::env::var("RABBITMQ_URL").expect("RABBITMQ_URL required for this chaos test");

    const N: usize = 5;
    let mut aggregate_ids = Vec::with_capacity(N);
    for i in 0..N {
        let event = OutboxEvent {
            topic: "chaos.test.event".to_string(),
            aggregate_type: "chaos_test".to_string(),
            aggregate_id: Uuid::new_v4(),
            payload: serde_json::json!({ "i": i }),
        };
        enqueue_outbox_event(&pool, &event).await.unwrap();
        aggregate_ids.push(event.aggregate_id);
    }

    let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel::<()>();
    let run_pool = pool.clone();
    let handle = tokio::spawn(async move {
        run(&run_pool, rabbitmq_connector(rabbitmq_url), 200, 50, async {
            shutdown_rx.await.ok();
        })
        .await
    });

    // Let it publish at least once successfully first — proves the happy path works before
    // anything is broken, so a false pass here can't be mistaken for the reconnect path working.
    let happy_path_deadline = Instant::now() + Duration::from_secs(15);
    loop {
        let published: i64 = sqlx::query_scalar(
            "SELECT count(*) FROM outbox_events WHERE aggregate_id = ANY($1) AND published_at IS NOT NULL",
        )
        .bind(&aggregate_ids)
        .fetch_one(&pool)
        .await
        .unwrap();
        if published > 0 {
            break;
        }
        assert!(
            Instant::now() < happy_path_deadline,
            "no outbox row published at all within 15s even before the chaos step — \
             outbox_publisher::run isn't working under normal conditions, not a chaos-specific failure"
        );
        tokio::time::sleep(Duration::from_millis(300)).await;
    }

    println!("chaos: stopping {RABBITMQ_CONTAINER}...");
    docker(&["stop", RABBITMQ_CONTAINER]);

    // backoff_delay's schedule is 1/2/4/8/16/30s — stay down long enough to force at least 2-3
    // real reconnect attempts, not just the first.
    tokio::time::sleep(Duration::from_secs(10)).await;

    println!("chaos: starting {RABBITMQ_CONTAINER}...");
    docker(&["start", RABBITMQ_CONTAINER]);

    // Container "started" != AMQP listener accepting connections yet, plus the publisher's own
    // backoff needs time to notice and retry — generous deadline, this is what's under test.
    let recovery_deadline = Instant::now() + Duration::from_secs(60);
    loop {
        let published: i64 = sqlx::query_scalar(
            "SELECT count(*) FROM outbox_events WHERE aggregate_id = ANY($1) AND published_at IS NOT NULL",
        )
        .bind(&aggregate_ids)
        .fetch_one(&pool)
        .await
        .unwrap();
        if published as usize == N {
            break;
        }
        assert!(
            Instant::now() < recovery_deadline,
            "not all {N} outbox rows were published within 60s of RabbitMQ coming back (got {published}) — \
             outbox_publisher::run did not recover from the RabbitMQ outage"
        );
        tokio::time::sleep(Duration::from_secs(1)).await;
    }

    let _ = shutdown_tx.send(());
    let _ = handle.await;

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_id = ANY($1)")
        .bind(&aggregate_ids)
        .execute(&pool)
        .await
        .ok();
}
