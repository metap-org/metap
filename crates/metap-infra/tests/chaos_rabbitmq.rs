//! **Chaos test — real fault injection, not a mock.** Stops and restarts a real RabbitMQ
//! container by name via the `docker` CLI mid-run, proving `run_resilient_consumer` — the shared
//! primitive `notification-worker`, `cron-scheduler`'s dispatch loop, and its trigger listener
//! all build on — actually reconnects and resumes consuming after a real connection loss, not
//! just in a unit test against a mock. This exact path had zero test coverage anywhere in the
//! workspace before this file (confirmed by survey). See `testing/disruptive/README.md` for the
//! philosophy (only ever targets this workspace's own dev containers) and the full scenario
//! table.
//!
//! Targets `run_resilient_consumer` directly rather than going through `notification-worker`'s
//! own `run()` wrapper — that wrapper's handler (`notify`) only logs via `tracing`, with no
//! return channel a test could observe; this uses a counting handler instead, which exercises
//! the exact same resilience code every real caller shares.
//!
//! `#[ignore]`d like every other e2e test in this crate, **plus** needs the `docker` CLI on PATH
//! and permission to stop/start `metap-rabbitmq-1`. Run:
//! `RABBITMQ_URL=amqp://metap:metap@localhost:5672 cargo test -p metap-infra --test chaos_rabbitmq -- --ignored --nocapture`
//! (after `docker compose up -d rabbitmq` from the `metap` repo root).

use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use metap_infra::{rabbitmq_connector, run_resilient_consumer, EventBus, RabbitEventBus};

const RABBITMQ_CONTAINER: &str = "metap-rabbitmq-1";

fn rabbitmq_url() -> String {
    std::env::var("RABBITMQ_URL").unwrap_or_else(|_| "amqp://metap:metap@localhost:5672".to_string())
}

fn docker(args: &[&str]) {
    let status = Command::new("docker")
        .args(args)
        .status()
        .unwrap_or_else(|e| panic!("docker CLI must be on PATH for this chaos test: {e}"));
    assert!(status.success(), "docker {args:?} exited non-zero");
}

async fn wait_until<F: Fn() -> bool>(condition: F, timeout: Duration) -> bool {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if condition() {
            return true;
        }
        tokio::time::sleep(Duration::from_millis(200)).await;
    }
    condition()
}

#[tokio::test]
#[ignore = "chaos: stops/starts a real RabbitMQ container by name (metap-rabbitmq-1) — needs `docker` on PATH, see testing/disruptive/README.md"]
async fn resilient_consumer_reconnects_and_resumes_after_rabbitmq_restarts() {
    let suffix = uuid::Uuid::new_v4().simple().to_string();
    let queue = format!("test.chaos.{suffix}");
    let routing_key = format!("test.chaos.{suffix}.event");

    let received = Arc::new(AtomicUsize::new(0));
    let received_handler = received.clone();

    let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel::<()>();
    let connect = rabbitmq_connector(rabbitmq_url());
    let queue_for_task = queue.clone();
    let task = tokio::spawn(async move {
        run_resilient_consumer(
            &queue_for_task,
            &routing_key,
            None,
            connect,
            move |event| {
                let received = received_handler.clone();
                async move {
                    received.fetch_add(1, Ordering::SeqCst);
                    event.ack().await.ok();
                }
            },
            async {
                shutdown_rx.await.ok();
            },
        )
        .await
    });

    // Give the subscription time to actually bind before publishing — same fixed-delay
    // reasoning `handler_registry_rabbitmq.rs`'s own tests document (no readiness signal exposed
    // by `run_resilient_consumer`, adding one would change a signature 3 production callers
    // share).
    tokio::time::sleep(Duration::from_millis(2000)).await;

    let routing_key_publish = format!("test.chaos.{suffix}.event");
    let publisher = RabbitEventBus::connect(&rabbitmq_url())
        .await
        .expect("connect publisher");
    publisher
        .publish(&routing_key_publish, &serde_json::json!({"phase": "before"}))
        .await
        .expect("publish before chaos");

    let happy_path = wait_until(|| received.load(Ordering::SeqCst) >= 1, Duration::from_secs(5)).await;
    assert!(
        happy_path,
        "no event received within 5s even before the chaos step — run_resilient_consumer isn't \
         working under normal conditions, not a chaos-specific failure"
    );
    publisher.close().await.ok();

    println!("chaos: stopping {RABBITMQ_CONTAINER}...");
    docker(&["stop", RABBITMQ_CONTAINER]);

    // backoff_delay's schedule is 1/2/4/8/16/30s — stay down long enough to force at least 2-3
    // real reconnect attempts, not just the first.
    tokio::time::sleep(Duration::from_secs(10)).await;

    println!("chaos: starting {RABBITMQ_CONTAINER}...");
    docker(&["start", RABBITMQ_CONTAINER]);

    // Container "started" != AMQP listener accepting connections yet, plus the consumer's own
    // backoff needs time to notice and retry — generous deadline, this is what's under test.
    // The queue itself is durable but this test only cares about a *new* message published after
    // recovery (proving the consumer, not RabbitMQ's own queue persistence, did the work) — retry
    // the publish since the broker may still be finishing startup right after `docker start`.
    let recovery_deadline = Instant::now() + Duration::from_secs(60);
    let mut published_after = false;
    while Instant::now() < recovery_deadline {
        if let Ok(publisher) = RabbitEventBus::connect(&rabbitmq_url()).await {
            if publisher
                .publish(&routing_key_publish, &serde_json::json!({"phase": "after"}))
                .await
                .is_ok()
            {
                publisher.close().await.ok();
                published_after = true;
                break;
            }
            publisher.close().await.ok();
        }
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
    assert!(
        published_after,
        "could not publish the post-recovery event within 60s of restart"
    );

    let recovered = wait_until(
        || received.load(Ordering::SeqCst) >= 2,
        recovery_deadline - Instant::now(),
    )
    .await;
    assert!(
        recovered,
        "consumer did not receive the post-recovery event (count={}) — run_resilient_consumer did \
         not reconnect after the RabbitMQ outage",
        received.load(Ordering::SeqCst)
    );

    shutdown_tx.send(()).ok();
    let _ = task.await;
}
