//! General-purpose audit trail for `metap-crud`'s `CrudService` — who changed what, on which
//! record, when, and why, for every `create`/`update`/`delete`/`transition`. Deliberately
//! separate from `metap-workflow`'s `workflow_events` (a narrower ledger of state-machine
//! transitions the workflow engine itself uses, not a general business audit trail — see
//! `../../metap-docs/docs/audits/05-crud-audit-trail-gap.md` for the full reasoning this crate
//! exists to close). Opt-in per entity (`metap_metadata::EntityDefinition.audit`) and pluggable
//! per deployment (`AuditTrailStore`) — a plain library, no HTTP, no business-entity knowledge,
//! same shape as `metap-workflow`/`metap-cron`.

mod diff;
mod entry;
mod postgres_store;
mod store;

pub use diff::diff_json_objects;
pub use entry::{redact_diff_fields, AuditAction, AuditEntry, AuditTrailEntryRow, JsonObject, REDACTED_MARKER};
pub use postgres_store::PostgresAuditTrailStore;
pub use store::AuditTrailStore;
