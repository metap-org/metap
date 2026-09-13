use uuid::Uuid;

use crate::entry::AuditEntry;

/// Swappable audit-trail sink — same shape as this codebase's other pluggable backends
/// (`metap_control::SecretStore`, `metap_storage::ObjectStore`, `metap_cache::Cache`): a trait
/// with `Send + Sync`, one impl per backend, wired in once at a binary's own composition root as
/// an `Arc<dyn AuditTrailStore>`.
///
/// **Owns its own client/pool rather than borrowing the caller's open transaction** — unlike
/// `metap_infra::outbox::enqueue` (generic over `sqlx::PgExecutor<'_>`, sharing the caller's
/// transaction). This is a deliberate choice, not an oversight: an object-safe
/// `Arc<dyn AuditTrailStore>` that can point at a genuinely different database (or a non-Postgres
/// backend entirely) is structurally incompatible with sharing the caller's `Transaction<'_,
/// Postgres>` — a trait method generic over a borrowed executor could never write anywhere but
/// whatever connection the caller handed it. The direct consequence: `CrudService` calls
/// `record()` only *after* its own business transaction has committed, never before or as part of
/// it — an audit row must never claim a write happened that then rolled back. This makes the
/// audit write best-effort (a crash in the narrow window between commit and this call drops that
/// one entry) rather than atomically guaranteed; accepted for this iteration, with a
/// transactional-outbox-relay hardening path available later if ever needed (route through
/// `metap_infra::outbox::enqueue` in the same business `tx` instead, drain via a small consumer)
/// — not built now, since nothing today requires zero-loss delivery.
#[async_trait::async_trait]
pub trait AuditTrailStore: Send + Sync {
    /// `tenant_id` is also on `entry.tenant_id` — passed again here explicitly as its own
    /// parameter for the same tenant-safety discipline `ObjectStore`/`Cache` already establish
    /// (every method takes `tenant_id` as a real parameter, not just something buried inside an
    /// opaque payload) — an impl is expected to assert the two match rather than trust the caller
    /// silently.
    async fn record(&self, tenant_id: Uuid, entry: AuditEntry) -> anyhow::Result<()>;
}
