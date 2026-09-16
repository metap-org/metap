//! The read side of `AUTH_CONTEXT_ENTITY` (`docs/features/03-organization-identity.md`) — looks
//! up the caller's own record on a configured entity by a `userId` field, generic over entity
//! shape (never becomes aware of what the configured entity's *fields* are, matching the "no
//! `metap-*` crate knows business entities" boundary — it does need that entity's `table_name`,
//! see below). Lives here, not inline in `metap-http`'s `AuthContext` extractor (found in code
//! review, 2026-08-22 — the original inline version violated CLAUDE.md's "route/handler code
//! must not import `sqlx` directly" rule), same reasoning `get_roles_for_user` already lives in
//! `role_assignment.rs` rather than in `metap-http` itself. Not a `CrudService::get` call — that
//! would run the *target's own* permission check against the very context being built, which is
//! circular; this is a raw, unauthenticated read of the caller's own identity data, same trust
//! level as `get_roles_for_user`.

use sqlx::{PgExecutor, Row};
use uuid::Uuid;

/// `table_name` (not an entity name) — every entity is on its own dedicated, schema-qualified
/// table now (`crates/migrations/0033_drop_records_table.sql` dropped the shared `records` table
/// this used to query via an `entity = $2` discriminator column), so the caller resolves
/// `AUTH_CONTEXT_ENTITY`'s configured entity name to its real `table_name` via a
/// `MetadataRegistry` lookup before calling this — see `metap_control::resolve_request_context`,
/// the only caller. `table_name` is trusted, unparameterized SQL interpolation, safe only because
/// it always comes from `EntityDefinition.table_name` post-`table_name_ok` validation (the same
/// `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$` schema-qualified-name check every `metap-crud`/
/// `metap-query` call site interpolating a table name already relies on) — never from
/// unvalidated caller input.
pub async fn fetch_context_attributes<'e, E: PgExecutor<'e>>(
    executor: E,
    tenant_id: Uuid,
    table_name: &str,
    user_id: Uuid,
) -> anyhow::Result<Option<serde_json::Map<String, serde_json::Value>>> {
    let row = sqlx::query(&format!(
        "SELECT data FROM {table_name} \
         WHERE tenant_id = $1 AND deleted = false AND data ->> 'userId' = $2 LIMIT 1"
    ))
    .bind(tenant_id)
    .bind(user_id.to_string())
    .fetch_optional(executor)
    .await?;
    let Some(row) = row else {
        return Ok(None);
    };
    let data: serde_json::Value = row.try_get("data")?;
    Ok(data.as_object().cloned())
}
