//! Shared `tracing` subscriber init for every binary in this workspace (`crm-server`,
//! `outbox-publisher`, `notification-worker`, `db-migrate`) — one copy instead
//! of four near-identical ones (`dev-tools` deliberately excluded, its stdout is CLI
//! output, not a log stream — see `docs/roadmap.md` Phase 8 Hardening). Call this before
//! anything else in `main()`, ahead of
//! `load_config()`, so boot-sequence logging (DB connect, entity registration, etc.) is
//! captured too.

use tracing_subscriber::EnvFilter;

/// Reads `RUST_LOG` (standard `tracing_subscriber::EnvFilter` syntax, e.g.
/// `info,metap_http=debug`), defaulting to `info` if unset or invalid. Logs to stderr as
/// plain text — no JSON/OTLP exporter wired up yet (no aggregator to send to; see
/// `docs/roadmap.md` Phase 8 Hardening's deployment-topology gap).
pub fn init_tracing() {
    // `.env` may set RUST_LOG; `load_config()` also calls this, but callers that want
    // RUST_LOG picked up here (logging starts before load_config's own dotenv call) need it
    // loaded now too. Safe to call twice — dotenvy never overwrites a var that's already set.
    dotenvy::dotenv().ok();

    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    tracing_subscriber::fmt()
        .with_env_filter(filter)
        .with_target(true)
        .init();
}
