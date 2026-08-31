//! Graceful-shutdown future — resolves on Ctrl+C or (unix) SIGTERM, whichever comes first. Found
//! byte-identical in `cron-scheduler`/`notification-worker`/`outbox-publisher`'s own hand-rolled
//! `shutdown_signal()` (17 lines each). `graphql-gateway` had reimplemented a narrower version
//! with no SIGTERM handling at all — a real gap, not just a style inconsistency: a service run
//! under a process manager that sends SIGTERM on deploy (not Ctrl+C, which only a local terminal
//! session delivers) would never shut down gracefully.
//!
//! No unit test here — signal delivery isn't meaningfully testable without spawning a real
//! subprocess and sending it a signal, more integration-test machinery than this one function
//! justifies. `tokio::select!` over 2 already-well-tested `tokio::signal` primitives is the whole
//! implementation.

pub async fn signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c().await.ok();
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("failed to install SIGTERM handler")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {}
        _ = terminate => {}
    }
}
