//! The read side of `AUTH_CONTEXT_ENTITY` (`docs/features/03-organization-identity.md`) — looks
//! up the caller's own record on a configured entity by a `userId` field, generic over entity
//! shape (never becomes aware of what `entity_name` actually is, matching the "no `metap-*`
//! crate knows business entities" boundary). Lives here, not inline in `metap-http`'s
//! `AuthContext` extractor (found in code review, 2026-08-22 — the original inline version
//! violated CLAUDE.md's "route/handler code must not import `sqlx` directly" rule), same
//! reasoning `get_roles_for_user` already lives in `role_assignment.rs` rather than in
//! `metap-http` itself. Not a `CrudService::get` call — that would run the *target's own*
//! permission check against the very context being built, which is circular; this is a raw,
//! unauthenticated read of the caller's own identity data, same trust level as
//! `get_roles_for_user`.

use sqlx::{PgExecutor, Row};
use uuid::Uuid;

pub async fn fetch_context_attributes<'e, E: PgExecutor<'e>>(
    executor: E,
    tenant_id: Uuid,
    entity_name: &str,
    user_id: Uuid,
) -> anyhow::Result<Option<serde_json::Map<String, serde_json::Value>>> {
    let row = sqlx::query(
        "SELECT data FROM records \
         WHERE tenant_id = $1 AND entity = $2 AND deleted = false AND data ->> 'userId' = $3 LIMIT 1",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(user_id.to_string())
    .fetch_optional(executor)
    .await?;
    let Some(row) = row else {
        return Ok(None);
    };
    let data: serde_json::Value = row.try_get("data")?;
    Ok(data.as_object().cloned())
}
