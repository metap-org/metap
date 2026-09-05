//! Library surface for the `graphql-gateway` binary (`src/main.rs`) — split out specifically so
//! its own e2e test (`tests/gateway_e2e_postgres.rs`) can call `schema_builder::build` directly
//! against real, locally-spun-up harness servers, the same "test the library, not the process"
//! shape every other binary+lib crate in this workspace already follows
//! (`outbox-publisher`/`notification-worker`/`cron-scheduler`).

pub mod config;
pub mod schema_builder;
pub mod server;
