//! Mirrors `packages/core/src/core/crud/crud-service.ts`. No injected `QueryPlanner`/
//! `WorkflowEngine`/`OutboxService` — those are the free-function modules built in
//! Migration Order steps 5/6 (`metap_query::plan_list`, `metap_workflow::*`), called
//! directly instead of held as constructor dependencies that wrap nothing.
//!
//! `entity` is fetched as an owned, cloned `EntityDefinition` at the top of every method
//! rather than borrowed from `self.metadata` — a deliberate simplicity choice (entities are
//! small; this sidesteps any borrow-across-`.await` friction) that can be revisited if
//! profiling ever shows it matters, not a performance decision made ahead of evidence.
//!
//! Split into one file per operation (`list`/`get`/`create`/`update`/`transition`/`delete`,
//! this file keeping only the struct/constructor and the two cross-cutting helper methods every
//! operation shares) purely to keep each file a manageable size — a single `impl CrudService`
//! spans multiple files, which Rust allows freely; no behavior or public-API change.

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use arc_swap::ArcSwap;
use metap_control::Router;
use metap_metadata::{EntityDefinition, EntityField, FieldKind, MetadataRegistry};
use metap_permission::{EntityAction, PermissionService, PermissionSnapshot, RequestContext};
use serde_json::Value;
use uuid::Uuid;

use crate::dto::{JsonObject, RecordDto};

mod aggregate;
mod audit_events;
mod check_permission;
mod create;
mod delete;
mod get;
mod get_many;
mod helpers;
mod list;
mod transition;
mod update;

/// `metadata`/`permissions` are `Arc`, not owned — `crates/metap-http` (Migration Order step
/// 8) needs to share the same registry/permission service across route handlers (direct
/// `/metadata/*` routes, the auth extractor's role lookups) without cloning them, and
/// `Arc<T>: Clone + Send + Sync` is exactly what a multi-handler async server needs.
///
/// `metadata` is an `ArcSwap`, not a plain `Arc<MetadataRegistry>` (`docs/roadmap.md` Phase
/// 11 / Phase A sub-project 2) — a DB-authored entity publish/rollback swaps in a new merged
/// registry while the server keeps running, no restart. Every method here loads a snapshot
/// once, synchronously, near the top (via `get_entity`, or directly in `list`) and uses that
/// same snapshot for the rest of the call — a request is never torn between two registry
/// versions even if a publish happens mid-request.
pub struct CrudService {
    router: Router,
    metadata: Arc<ArcSwap<MetadataRegistry>>,
    permissions: Arc<PermissionService>,
    /// Opt-in general-purpose audit trail (`metap-audit`,
    /// `../../metap-docs/docs/audits/05-crud-audit-trail-gap.md`) — `None` (via `new`) preserves
    /// every existing deployment's behavior exactly; `with_audit` is the second, additive
    /// constructor a binary opts into, mirroring `PermissionService::with_cache`'s own shape.
    audit: Option<Arc<dyn metap_audit::AuditTrailStore>>,
    /// Closes the gap `../CLAUDE.md`'s REST-entity-CRUD-removal note flags: automatic
    /// `AUTH_CONTEXT_ENTITY` cache invalidation used to live only inside the now-deleted REST
    /// `routes::records::update_record` handler, so a GraphQL/gRPC update to that entity has
    /// always left `ContextAttributesCache` stale until its TTL or an explicit
    /// `POST /admin/users/{userId}/context/invalidate`. `None` (via `new`/`with_audit`) preserves
    /// every existing deployment's behavior exactly — see [`Self::with_context_invalidation`],
    /// the additive opt-in. Chainable rather than a 3rd/4th fixed constructor (unlike `audit`
    /// above) because it's genuinely orthogonal to `audit` and a fixed-constructor combinatorial
    /// explosion (`new`/`with_audit`/`with_context_invalidation`/`with_audit_and_context_invalidation`)
    /// isn't worth it for 2 independent opt-ins.
    context_invalidation: Option<ContextInvalidationConfig>,
}

/// See [`CrudService::with_context_invalidation`]'s doc comment.
struct ContextInvalidationConfig {
    /// The entity name `AUTH_CONTEXT_ENTITY` resolves to (`AppState.auth_context_entity`'s
    /// existing shape) — only an `update()` on *this* entity triggers invalidation.
    entity_name: String,
    cache: metap_control::ContextAttributesCache,
}

impl CrudService {
    pub fn new(router: Router, metadata: Arc<ArcSwap<MetadataRegistry>>, permissions: Arc<PermissionService>) -> Self {
        Self {
            router,
            metadata,
            permissions,
            audit: None,
            context_invalidation: None,
        }
    }

    /// Same as `new`, plus an audit trail sink every `create`/`update`/`delete`/`transition`
    /// records to — but only for an entity whose own `EntityAuditConfig.enabled` is `true`; an
    /// entity with no `audit` config at all still produces zero audit rows even with a store
    /// configured here.
    pub fn with_audit(
        router: Router,
        metadata: Arc<ArcSwap<MetadataRegistry>>,
        permissions: Arc<PermissionService>,
        audit: Arc<dyn metap_audit::AuditTrailStore>,
    ) -> Self {
        Self {
            router,
            metadata,
            permissions,
            audit: Some(audit),
            context_invalidation: None,
        }
    }

    /// Opt-in `AUTH_CONTEXT_ENTITY` cache invalidation — see [`ContextInvalidationConfig`]'s doc
    /// comment for what this closes. Chainable onto either [`Self::new`] or [`Self::with_audit`]'s
    /// result, e.g. `CrudService::new(router, metadata, permissions)
    /// .with_context_invalidation(entity_name, context_attributes_cache)`; `cache` should be the
    /// *same* `ContextAttributesCache` instance `AuthContext`/`resolve_request_context` reads
    /// from (e.g. `AppState.context_attributes_cache.clone()` — cheap, shares the underlying
    /// `moka` cache) — a separate instance would invalidate a cache nothing actually reads.
    pub fn with_context_invalidation(
        mut self,
        entity_name: String,
        cache: metap_control::ContextAttributesCache,
    ) -> Self {
        self.context_invalidation = Some(ContextInvalidationConfig { entity_name, cache });
        self
    }

    fn get_entity(&self, entity_name: &str) -> Option<EntityDefinition> {
        self.metadata.load().get_entity(entity_name).cloned()
    }

    /// Records one audit-trail entry if (a) an `AuditTrailStore` is configured on this service at
    /// all, and (b) `entity.audit` opts this specific entity in. No-ops instantly otherwise — the
    /// common case for a deployment that hasn't adopted this feature pays nothing beyond the
    /// two field reads. Called *after* the caller's own business write has already committed
    /// (see `metap_audit::AuditTrailStore`'s doc comment for why this can't be inside that same
    /// transaction) — a store error is only logged, never surfaced to the caller, since an
    /// audit-write failure must not turn an already-successful business write into a failed
    /// response.
    async fn record_audit(&self, entity: &EntityDefinition, mut entry: metap_audit::AuditEntry) {
        let Some(store) = &self.audit else { return };
        let Some(config) = entity.audit.as_ref().filter(|c| c.enabled) else {
            return;
        };
        // Write-side redaction, applied here rather than at each of the 4 call sites precisely
        // because this is the one choke point all of them pass through — a new write path cannot
        // persist a redacted field's value by forgetting a step. See
        // `EntityAuditConfig::redacted_fields` for why this is a separate mechanism from the
        // per-caller masking the read path applies.
        metap_audit::redact_diff_fields(&mut entry.diff, &config.redacted_fields);
        let tenant_id = entry.tenant_id;
        if let Err(e) = store.record(tenant_id, entry).await {
            tracing::error!(entity = %entity.name, error = %e, "failed to record audit trail entry");
        }
    }

    /// No-ops unless `context_invalidation` is configured *and* `entity_name` matches its
    /// `entity_name` — the common case (feature unused) pays nothing beyond 1-2 field reads,
    /// mirroring `record_audit`'s shape. `data` must be the full, unmasked write payload (the
    /// `AUTH_CONTEXT_ENTITY` record's own `userId` field is business data, unrelated to which
    /// fields the *actor performing this update* can read back — reading it from a
    /// permission-masked `RecordDto` instead could silently skip invalidation for an actor
    /// without read access to `userId`, even though the write itself succeeded). Only ever
    /// called from `update()`, same as the REST handler this restores parity with — a `delete()`
    /// of an `AUTH_CONTEXT_ENTITY` record leaves a stale cache entry until its TTL, a known,
    /// narrower gap than before this method existed at all, not a new one.
    async fn invalidate_context_cache_if_configured(&self, entity_name: &str, tenant_id: Uuid, data: &JsonObject) {
        let Some(config) = &self.context_invalidation else {
            return;
        };
        if config.entity_name != entity_name {
            return;
        }
        let Some(user_id) = data
            .get("userId")
            .and_then(Value::as_str)
            .and_then(|s| Uuid::parse_str(s).ok())
        else {
            return;
        };
        config.cache.invalidate(tenant_id, user_id).await;
    }

    /// Cross-record permission conditions (`docs/roadmap.md`'s permission-review findings,
    /// 2026-08-21, item #3): a record-level policy's condition may reference a related record
    /// via a dotted attribute path (e.g. `"project.ownerId"`), resolved one hop through a
    /// `FieldKind::Reference` field. Builds a *copy* of `record` with each such relation field's
    /// value replaced by the related record's own data (never mutates the caller's copy — other
    /// call-site logic, e.g. workflow guards or `writable_fields`, still needs the original
    /// reference-id value, not the expanded object) — used only as the subject passed into
    /// `PermissionSnapshot::can_perform_record_condition`.
    ///
    /// Only runs when `snapshot.required_relation_fields(action)` (for the union of `actions`)
    /// is non-empty, so an entity with no cross-record conditions pays zero extra query cost —
    /// this only ever runs for single-record operations (get/update/delete/transition), never
    /// `list()`, which has no way to resolve a relation inside a SQL `WHERE` clause (see
    /// `metap_query::condition_to_sql`'s doc comment on why that's rejected there instead of
    /// silently mismatching).
    async fn enrich_record_for_actions(
        &self,
        entity: &EntityDefinition,
        snapshot: &PermissionSnapshot,
        actions: &[EntityAction],
        tenant_id: Uuid,
        record: &JsonObject,
    ) -> anyhow::Result<JsonObject> {
        let mut relation_fields: Vec<String> = Vec::new();
        for &action in actions {
            for field in snapshot.required_relation_fields(action) {
                if !relation_fields.contains(&field) {
                    relation_fields.push(field);
                }
            }
        }
        if relation_fields.is_empty() {
            return Ok(record.clone());
        }

        let mut enriched = record.clone();
        let mut tx = self.router.begin(tenant_id.into()).await?;
        for field_name in relation_fields {
            let Some(field) = entity.fields.iter().find(|f| f.name == field_name) else {
                continue;
            };
            if field.kind != FieldKind::Reference {
                continue;
            }
            let Some(ref_entity) = &field.ref_entity else {
                continue;
            };
            let Some(ref_id) = record
                .get(&field_name)
                .and_then(Value::as_str)
                .and_then(|s| Uuid::parse_str(s).ok())
            else {
                continue;
            };
            let Some(ref_entity_def) = self.get_entity(ref_entity) else {
                continue;
            };
            if let Some(related_data) =
                helpers::fetch_related_data(&mut *tx, ref_id, tenant_id, &ref_entity_def).await?
            {
                enriched.insert(field_name, Value::Object(related_data));
            }
        }
        tx.commit().await?;
        Ok(enriched)
    }

    /// "Mode 2" batch display hydration (`docs/roadmap.md`) — the list-only counterpart to
    /// `enrich_record_for_actions` above, but for a different purpose: that method resolves
    /// relations *permission conditions* need, invisibly, never returned to the caller; this
    /// resolves relations a *list view* wants to display (any `Reference` field that declares
    /// `refDisplayField`), and its result — `RecordDto.related_display` — is exactly what gets
    /// serialized back. Solves the same problem a client fetching one relation's display value
    /// per row (N HTTP round trips for an N-row page) would otherwise hit: one batched
    /// `WHERE id = ANY($1)` query per such field for the whole page, not per row. Shares
    /// `list()`'s own transaction (`tx`) rather than opening a second one — `list()`'s
    /// `tx.commit()` now happens after this returns, not before.
    ///
    /// Zero extra cost for the overwhelmingly common case (no `Reference` field declares
    /// `refDisplayField`, or none of this page's rows have a value for one) — matches every
    /// other "pay for what you use" mechanism in this file. Runs after field-level read masking
    /// (`mask_record_for_read`) so a field the caller isn't allowed to see at all can't get a
    /// display value either, skips a field entirely if the caller can't read its target entity
    /// at all (`can_read_entity`), and — found in code review, 2026-08-22, since this used to
    /// stop at that coarse entity-level check — evaluates each related record's own record-level
    /// read policy (`PermissionSnapshot::can_perform_record_condition`, the same mechanism
    /// `get()` uses) before including its display value: a display convenience must not leak a
    /// value the caller would get a `403` for reading directly. Not a SQL-level `JOIN` (that's
    /// `metap_query::condition_to_sql`'s "not built" gap, Mode 3 in
    /// `docs/features/05-cross-entity-relations.md`) — the related rows are fetched first, then
    /// filtered in Rust, same shape `enrich_record_for_actions` already uses for its one-hop
    /// resolution.
    async fn hydrate_related_display(
        &self,
        entity: &EntityDefinition,
        context: &RequestContext,
        tenant_id: Uuid,
        tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
        records: Vec<RecordDto>,
    ) -> anyhow::Result<Vec<RecordDto>> {
        let display_fields: Vec<&EntityField> = entity
            .fields
            .iter()
            .filter(|f| f.kind == FieldKind::Reference && f.ref_display_field.is_some())
            .collect();
        if display_fields.is_empty() {
            return Ok(records);
        }

        // field name -> (related record id -> resolved display value)
        let mut resolved: HashMap<String, HashMap<Uuid, String>> = HashMap::new();
        for field in &display_fields {
            let ref_entity = field
                .ref_entity
                .as_deref()
                .expect("Reference field always has ref_entity");
            let display_field = field.ref_display_field.as_deref().expect("filtered on is_some above");

            let decision = self.permissions.can_read_entity(context, ref_entity).await?;
            if !decision.allowed {
                continue;
            }

            let ids: Vec<Uuid> = records
                .iter()
                .filter_map(|r| {
                    r.data
                        .get(&field.name)
                        .and_then(Value::as_str)
                        .and_then(|s| Uuid::parse_str(s).ok())
                })
                .collect::<HashSet<_>>()
                .into_iter()
                .collect();
            if ids.is_empty() {
                continue;
            }
            let Some(ref_entity_def) = self.get_entity(ref_entity) else {
                continue;
            };

            let related = helpers::fetch_related_records_batch(&mut **tx, &ids, tenant_id, &ref_entity_def).await?;
            if related.is_empty() {
                continue;
            }

            // Same record-level enforcement `get()` applies to a single related record
            // (`enrich_record_for_actions` + `can_perform_record_condition`), just evaluated
            // per row here instead of per relation-hop — a row this caller couldn't read
            // directly must not surface its display value through this entity's list either.
            let related_snapshot = self.permissions.load_snapshot(tenant_id, ref_entity).await?;
            let mut values: HashMap<Uuid, String> = HashMap::new();
            for (id, related_data) in related {
                let record_decision =
                    related_snapshot.can_perform_record_condition(context, &related_data, EntityAction::Read);
                if !record_decision.allowed {
                    continue;
                }
                if let Some(value) = related_data.get(display_field).and_then(Value::as_str) {
                    values.insert(id, value.to_string());
                }
            }
            resolved.insert(field.name.clone(), values);
        }

        if resolved.is_empty() {
            return Ok(records);
        }

        Ok(records
            .into_iter()
            .map(|record| {
                let mut display: HashMap<String, String> = HashMap::new();
                for field in &display_fields {
                    let Some(values) = resolved.get(&field.name) else {
                        continue;
                    };
                    let Some(id) = record
                        .data
                        .get(&field.name)
                        .and_then(Value::as_str)
                        .and_then(|s| Uuid::parse_str(s).ok())
                    else {
                        continue;
                    };
                    if let Some(value) = values.get(&id) {
                        display.insert(field.name.clone(), value.clone());
                    }
                }
                if display.is_empty() {
                    record
                } else {
                    RecordDto {
                        related_display: Some(display),
                        ..record
                    }
                }
            })
            .collect())
    }
}
