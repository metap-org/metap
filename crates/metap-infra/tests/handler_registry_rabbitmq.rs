//! Live verification of `HandlerRegistry` (Phase 40) against a real RabbitMQ — dispatch to every
//! matching handler on one shared subscription, ack only once all of them succeed, and retry
//! (via `RetryPolicy`) when one fails. Needs a running `docker compose up -d rabbitmq`
//! (`RABBITMQ_URL`, same default as `../metap-demo-crm/.env`). Run via
//! `cargo test -p metap-infra -- --ignored`.

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use metap_infra::{EventBus, HandlerRegistry, RabbitEventBus, RetryPolicy};

fn rabbitmq_url() -> String {
    std::env::var("RABBITMQ_URL").unwrap_or_else(|_| "amqp://metap:metap@localhost:5672".to_string())
}

async fn wait_until<F: Fn() -> bool>(condition: F, timeout: Duration) -> bool {
    let deadline = tokio::time::Instant::now() + timeout;
    while tokio::time::Instant::now() < deadline {
        if condition() {
            return true;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    condition()
}

/// One event, two handlers with overlapping patterns (a narrow exact match and a broad `#`
/// catch-all) — both must run, and the delivery must ack only after both have (confirmed
/// indirectly: no redelivery arrives once both ran).
#[tokio::test]
#[ignore]
async fn dispatches_to_every_matching_handler_and_acks_once_all_succeed() {
    let suffix = uuid::Uuid::new_v4().simple().to_string();
    let queue = format!("test.handler-registry.{suffix}");
    let routing_key = format!("test.handler-registry.{suffix}.record.created");

    let ran: Arc<Mutex<Vec<&'static str>>> = Arc::new(Mutex::new(Vec::new()));
    let ran_a = ran.clone();
    let ran_b = ran.clone();

    let registry = HandlerRegistry::new()
        .on(routing_key.clone(), move |_event| {
            let ran = ran_a.clone();
            async move {
                ran.lock().unwrap().push("narrow");
                Ok(())
            }
        })
        .on(format!("test.handler-registry.{suffix}.#"), move |_event| {
            let ran = ran_b.clone();
            async move {
                ran.lock().unwrap().push("broad");
                Ok(())
            }
        });

    let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel::<()>();
    let connect = {
        let url = rabbitmq_url();
        move || {
            let url = url.clone();
            async move { RabbitEventBus::connect(&url).await }
        }
    };
    let queue_for_task = queue.clone();
    let task = tokio::spawn(async move {
        registry
            .run(&queue_for_task, None, connect, async {
                shutdown_rx.await.ok();
            })
            .await
    });

    // Give the subscription time to actually bind before publishing — same ordering lesson as
    // `retry_policy_rabbitmq.rs` (publishing before a queue is bound drops the message, silently
    // — RabbitMQ has nowhere to route it). `retry_policy_rabbitmq.rs` avoids the race entirely by
    // awaiting `EventBus::subscribe()` directly before publishing; `HandlerRegistry::run` spawns
    // the whole consume loop as a background task with no readiness signal exposed to the caller
    // (adding one would mean changing `run_resilient_consumer`'s signature, shared by
    // `cron-scheduler`/`notification-worker`'s production consumers too — out of scope for a
    // test-only flake fix), so this is a fixed delay, not a deterministic wait. Found live
    // (2026-08-28): 500ms was long enough locally but not under CI's contended, parallel e2e run
    // — the connect+declare+bind round trip can genuinely take longer than that when every e2e
    // test binary is fighting for CPU/network at once. 2s is a wide enough margin for that.
    tokio::time::sleep(Duration::from_millis(2000)).await;
    let publisher = RabbitEventBus::connect(&rabbitmq_url())
        .await
        .expect("connect publisher");
    publisher
        .publish(&routing_key, &serde_json::json!({"seq": 1}))
        .await
        .expect("publish");

    let both_ran = wait_until(
        || {
            let ran = ran.lock().unwrap();
            ran.len() == 2 && ran.contains(&"narrow") && ran.contains(&"broad")
        },
        Duration::from_secs(5),
    )
    .await;
    assert!(both_ran, "both matching handlers must run: {:?}", ran.lock().unwrap());

    // No redelivery should follow — give it a beat, then confirm the count stayed at 2 (an ack
    // failure would show up as a 3rd/4th run from redelivery).
    tokio::time::sleep(Duration::from_millis(800)).await;
    assert_eq!(
        ran.lock().unwrap().len(),
        2,
        "delivery must have acked after both handlers succeeded — no redelivery expected"
    );

    shutdown_tx.send(()).ok();
    task.await
        .expect("registry task panicked")
        .expect("registry run returned Err");
    publisher.close().await.ok();
}

/// A handler that fails on its first invocation and succeeds on its second — confirms a failing
/// handler routes the whole event through `RetryPolicy` (not lost, not hot-looped) and that the
/// redelivered attempt reaches the handler again.
#[tokio::test]
#[ignore]
async fn retries_the_whole_event_when_a_handler_fails_then_succeeds_on_redelivery() {
    let suffix = uuid::Uuid::new_v4().simple().to_string();
    let queue = format!("test.handler-registry.{suffix}");
    let routing_key = format!("test.handler-registry.{suffix}.record.created");

    let attempts = Arc::new(AtomicUsize::new(0));
    let attempts_handler = attempts.clone();

    let registry = HandlerRegistry::new().on(routing_key.clone(), move |_event| {
        let attempts = attempts_handler.clone();
        async move {
            let n = attempts.fetch_add(1, Ordering::SeqCst);
            if n == 0 {
                anyhow::bail!("simulated transient failure on first attempt");
            }
            Ok(())
        }
    });

    let policy = RetryPolicy {
        delays: vec![Duration::from_millis(800)],
    };

    let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel::<()>();
    let connect = {
        let url = rabbitmq_url();
        move || {
            let url = url.clone();
            async move { RabbitEventBus::connect(&url).await }
        }
    };
    let queue_for_task = queue.clone();
    let policy_for_task = policy.clone();
    let task = tokio::spawn(async move {
        registry
            .run(&queue_for_task, Some(&policy_for_task), connect, async {
                shutdown_rx.await.ok();
            })
            .await
    });

    // See the sibling test's comment above for why this is a fixed delay (not a deterministic
    // wait) and why 2s — same fix, same root cause.
    tokio::time::sleep(Duration::from_millis(2000)).await;
    let publisher = RabbitEventBus::connect(&rabbitmq_url())
        .await
        .expect("connect publisher");
    publisher
        .publish(&routing_key, &serde_json::json!({"seq": 1}))
        .await
        .expect("publish");

    let succeeded = wait_until(|| attempts.load(Ordering::SeqCst) >= 2, Duration::from_secs(5)).await;
    assert!(
        succeeded,
        "expected exactly 2 attempts (fail, then succeed on redelivery), got {}",
        attempts.load(Ordering::SeqCst)
    );

    shutdown_tx.send(()).ok();
    task.await
        .expect("registry task panicked")
        .expect("registry run returned Err");
    publisher.close().await.ok();
}
