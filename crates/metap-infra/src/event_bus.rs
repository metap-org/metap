//! `EventBus` — a trait in front of `RabbitPublisher` rather than a concrete type (see
//! `docs/architectures/09-adr.md`). `RabbitEventBus` is its only implementation today;
//! the point of the trait is that a second one (Kafka/NATS, or an in-memory bus for tests)
//! is a new `impl EventBus`, not a rewrite of every call site — see
//! `docs/modular-spi-architecture.md` for the target this generalizes toward.
//!
//! `subscribe` is the read-side counterpart, added once a real consumer (the notification
//! worker, see `crates/metap-notification-worker`) needed one — see `docs/roadmap.md` Phase 5's
//! note on the stub `<entity>.workflow.transitioned` topic. `ConsumedEvent` hides the
//! `lapin`-specific delivery/ack machinery behind the same backend-agnostic shape `publish`
//! already uses, so a future non-Rabbit `EventBus` impl doesn't leak here either.
//!
//! Split into `types` (the trait/`ConsumedEvent`/`RetryPolicy`), `resilient`
//! (`run_resilient_consumer`), `rabbit` (`RabbitEventBus`), and `handler_registry`
//! (`HandlerRegistry`) purely to keep each file a manageable size — every item this module used
//! to export directly is re-exported here unchanged.

mod handler_registry;
mod rabbit;
mod resilient;
mod types;

pub use handler_registry::HandlerRegistry;
pub use rabbit::RabbitEventBus;
pub use resilient::{backoff_delay, run_resilient_consumer, sleep_or_shutdown};
pub use types::{ConsumedEvent, EventBus, RetryPolicy};

#[cfg(test)]
mod tests {
    use super::handler_registry::topic_matches;

    #[test]
    fn exact_match() {
        assert!(topic_matches(
            "crm.customers.record.created",
            "crm.customers.record.created"
        ));
        assert!(!topic_matches(
            "crm.customers.record.created",
            "crm.customers.record.updated"
        ));
    }

    #[test]
    fn star_matches_exactly_one_word() {
        assert!(topic_matches(
            "crm.customers.workflow.*",
            "crm.customers.workflow.transitioned"
        ));
        assert!(!topic_matches(
            "crm.customers.workflow.*",
            "crm.customers.workflow.transitioned.extra"
        ));
        assert!(!topic_matches("crm.customers.workflow.*", "crm.customers.workflow"));
    }

    #[test]
    fn hash_matches_zero_or_more_words() {
        assert!(topic_matches("#", "crm.customers.record.created"));
        assert!(topic_matches(
            "#.workflow.transitioned",
            "crm.customers.workflow.transitioned"
        ));
        assert!(topic_matches("#.workflow.transitioned", "workflow.transitioned"));
        assert!(!topic_matches(
            "#.workflow.transitioned",
            "crm.customers.workflow.updated"
        ));
        assert!(topic_matches("crm.customers.#", "crm.customers.record.created"));
        assert!(topic_matches("crm.customers.#", "crm.customers"));
    }

    #[test]
    fn no_match_on_prefix_or_suffix_only() {
        assert!(!topic_matches("crm.customers", "crm.customers.record.created"));
        assert!(!topic_matches("crm.customers.record.created", "crm.customers"));
    }
}
