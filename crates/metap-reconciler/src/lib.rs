//! Table-per-entity reconciler (`docs/multi-tenant-platform-design.md` §5-§6,
//! `docs/features/04-table-per-entity.md` steps 2-5): `reconcile(desired) = introspect(actual) →
//! diff → plan → execute` for one `(tenant, entity)` (`reconcile`), declarative migration ops
//! with preflight/quarantine for data that can't transform cleanly (`migration`, `quarantine`),
//! and multi-tenant fan-out primitives (`orchestrator`) — pull-based claim, failure
//! classification, wave rollout. No HTTP, no business-entity knowledge — a plain library, same
//! shape as `metap-permission`/`metap-cron`.

pub mod backfill;
pub mod compile;
pub mod diff;
pub mod executor;
pub mod introspect;
pub mod migration;
pub mod normalize;
pub mod orchestrator;
pub mod quarantine;
pub mod reconcile;
pub mod schema;
mod sqlfmt;
pub mod status;
pub mod watchdog;

pub use compile::compile;
pub use diff::{diff, DdlOp};
pub use introspect::introspect;
pub use migration::{run_migration, MigrationOp, MigrationOutcome, PreflightReport, QuarantinePolicy};
pub use reconcile::{reconcile, ReconcileOutcome};
pub use schema::{
    ColumnOrigin, ColumnSpec, Cost, ExecutionMode, FkSpec, IndexSpec, OnDelete, PhysicalSchema, UniqueSpec,
};
pub use status::EntityStatus;
