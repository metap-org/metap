//! Table-per-entity reconciler (`docs/multi-tenant-platform-design.md` §5-§6,
//! `docs/features/04-table-per-entity.md` steps 2-5): `reconcile(desired) = introspect(actual) →
//! diff → plan → execute` for one `(tenant, entity)` (`reconcile`), declarative migration ops
//! with preflight/quarantine for data that can't transform cleanly (`migration`, `quarantine`),
//! multi-tenant fan-out primitives (`orchestrator`) — pull-based claim, failure classification,
//! wave rollout — and the one-shot generic-table-to-dedicated-table data move for an entity
//! already live on `records` (`migrate`, `docs/features/12-migration-generic-to-dedicated-table.md`).
//! No HTTP, no business-entity knowledge — a plain library, same shape as
//! `metap-permission`/`metap-cron`.

pub mod backfill;
pub mod compile;
pub mod diff;
pub mod executor;
pub mod introspect;
pub mod migrate;
pub mod migration;
pub mod normalize;
pub mod orchestrator;
pub mod quarantine;
pub mod reconcile;
pub mod schema;
mod sqlfmt;
pub mod status;
pub mod watchdog;

pub use compile::{
    check_table_name_length, compile, qualified_table_name_for, qualified_table_name_in, table_name_for,
    ENTITY_SCHEMA,
};
pub use diff::{diff, DdlOp};
pub use introspect::introspect;
pub use migrate::{copy_generic_records, migrate_generic_to_dedicated, CopySummary, MigrateOutcome, MIGRATE_OP_ID};
pub use migration::{run_migration, MigrationOp, MigrationOutcome, PreflightReport, QuarantinePolicy};
pub use reconcile::{reconcile, ReconcileOutcome};
pub use schema::{
    ColumnOrigin, ColumnSpec, Cost, ExecutionMode, FkSpec, IndexSpec, OnDelete, PhysicalSchema, UniqueSpec,
};
pub use status::EntityStatus;
