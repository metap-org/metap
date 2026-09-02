//! Live verification of `RetryPolicy`/`ConsumedEvent::retry_or_give_up` (Phase 39) against a
//! real RabbitMQ — the TTL+DLX "delay queue" chain only RabbitMQ itself can actually exercise
//! (queue declarations, `x-message-ttl`, `x-death`-based `retry_count`), so this can't be a pure
//! unit test. Needs a running `docker compose up -d rabbitmq` (`RABBITMQ_URL`, same default as
//! `../metap-demo-crm/.env`). Run via `cargo test -p metap-infra -- --ignored`.

use std::time::Duration;

use futures_util::StreamExt;
use metap_infra::{EventBus, RabbitEventBus, RetryPolicy};

fn rabbitmq_url() -> String {
    std::env::var("RABBITMQ_URL").unwrap_or_else(|_| "amqp://metap:metap@localhost:5672".to_string())
}

/// One message, one retry tier (short TTL so the test stays fast): publish → consume → fail
/// (retry_or_give_up, attempt 0 < 1 tier) → published to `.retry.1` → TTL expires → RabbitMQ
/// dead-letters it back to the main queue → consume again, `retry_count() == 1` → fail again
/// (attempt 1 >= 1 tier) → final give-up (`.dlq`) → confirm it landed there via a plain
/// `basic_get` on a second connection, independent of the `EventBus` under test.
#[tokio::test]
#[ignore]
async fn retry_or_give_up_requeues_via_ttl_then_dead_letters_once_exhausted() {
    let suffix = uuid::Uuid::new_v4().simple().to_string();
    let queue = format!("test.retry-policy.{suffix}");
    let routing_key = format!("test.retry-policy.{suffix}");

    let policy = RetryPolicy {
        delays: vec![Duration::from_millis(800)],
    };

    let bus = RabbitEventBus::connect(&rabbitmq_url())
        .await
        .expect("connect to rabbitmq");

    // Subscribe first — declares + binds the queue, so the exchange has somewhere to route the
    // publish below. Publishing before any queue is bound would just drop the message.
    let mut events = bus
        .subscribe(&queue, &routing_key, Some(&policy))
        .await
        .expect("subscribe");

    bus.publish(&routing_key, &serde_json::json!({"seq": 1}))
        .await
        .expect("publish");

    // First delivery: attempt 0, still within the 1-tier policy — retries.
    let first = tokio::time::timeout(Duration::from_secs(5), events.next())
        .await
        .expect("first delivery did not arrive in time")
        .expect("stream ended unexpectedly");
    assert_eq!(
        first.retry_count(),
        0,
        "first delivery must have no prior x-death entries"
    );
    assert_eq!(
        first.routing_key, routing_key,
        "first delivery's routing key must be the one published"
    );
    first
        .retry_or_give_up(&policy)
        .await
        .expect("retry_or_give_up (attempt 0)");

    // Redelivered from `.retry.1` after its ~800ms TTL — RabbitMQ dead-letters it back onto the
    // main queue on its own, no scheduler/poller involved on our side.
    let second = tokio::time::timeout(Duration::from_secs(5), events.next())
        .await
        .expect("redelivery via retry tier did not arrive in time")
        .expect("stream ended unexpectedly");
    assert_eq!(
        second.retry_count(),
        1,
        "redelivered message must carry exactly one x-death entry (one retry hop)"
    );
    assert_eq!(
        second.routing_key, routing_key,
        "redelivered message's routing key must still be the original topic, not the retry-tier's \
         dead-letter target (the plain queue name) — this is the bug ORIGINAL_ROUTING_KEY_HEADER fixes"
    );
    second
        .retry_or_give_up(&policy)
        .await
        .expect("retry_or_give_up (attempt 1, exhausts the 1-tier policy)");

    // Attempt 1 >= policy.delays.len() (1), so this must have given up (final DLQ), not
    // published another retry tier — confirm nothing else arrives on the main queue.
    let nothing_else = tokio::time::timeout(Duration::from_millis(1500), events.next()).await;
    assert!(
        nothing_else.is_err(),
        "no further redelivery expected once the retry policy is exhausted"
    );

    // Confirm it actually landed in `<queue>.dlq` — a fresh connection/channel, independent of
    // the `EventBus`/`ConsumedEvent` machinery under test.
    let conn = lapin::Connection::connect(&rabbitmq_url(), lapin::ConnectionProperties::default())
        .await
        .expect("connect for dlq verification");
    let channel = conn.create_channel().await.expect("create channel");
    let dlq_name = format!("{queue}.dlq");
    let dlq_message = channel
        .basic_get(&dlq_name, lapin::options::BasicGetOptions::default())
        .await
        .expect("basic_get on dlq");
    let dlq_message = dlq_message.expect("expected exactly one message in the dead-letter queue");
    let payload: serde_json::Value = serde_json::from_slice(&dlq_message.data).expect("dlq payload is valid json");
    assert_eq!(payload["seq"], 1, "dead-lettered message must be the original payload");
    dlq_message
        .ack(lapin::options::BasicAckOptions::default())
        .await
        .expect("ack dlq message (cleanup)");
    conn.close(200, "test done").await.ok();

    bus.close().await.ok();
}
