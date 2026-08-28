//! Admin-gated HTTP surface for `metap-lowcode`'s draft/publish/rollback storage
//! (`docs/roadmap.md` Phase 11 / Phase A sub-project 4, retargeted from
//! `docs/low-code-metadata-storage-design.md`). Same shape as `metap-http`'s
//! `routes/admin.rs`/`routes/cron.rs`: every handler uses `AdminContext`. Unlike
//! `routes/cron.rs`, entity metadata itself carries no `tenant_id` column — DB-authored entity
//! metadata is global *within whichever database it lives in*, by design for Phase A (see the
//! spec's "Các quyết định đã chốt"). That's not the same as "which database it lives in doesn't
//! matter": every handler resolves `Router::pool_for(caller's tenant)` (`resolve_pool`) before
//! touching storage, so a `DedicatedDb` tenant's low-code entities land in that tenant's own
//! database rather than always the platform's shared one (2026-08-25 fix — previously every
//! handler read `state.pool` directly, see `resolve_pool`'s doc comment for the concrete leak).
//!
//! **Deliberately its own crate, not a module inside `metap-http`** — the low-code control
//! plane is an optional platform capability, not core (`docs/roadmap.md` Phase 11 is a
//! trigger-based, in-progress phase; the base execution engine predates it and must keep
//! working without it). `metap-http` has zero dependency on this crate or on `metap-lowcode`;
//! a binary that wants this surface merges [`router`] into `metap_http::build_router`'s
//! `extra_routes` argument itself (see `apps/crm-server/src/main.rs`) — it is never wired in
//! automatically. A downstream project that doesn't want a low-code control plane at all can
//! depend on `metap-http`/`metap-crud`/`metap-metadata` and skip this crate entirely.
//!
//! `publish`/`rollback` are the only handlers that mutate `state.metadata` — both call
//! [`apply_registry`] after `metap_lowcode` writes a new version, swapping the already-
//! validated registry `metap_lowcode::publish`/`rollback` returns straight into
//! `state.metadata`'s `ArcSwap` before the handler responds. No restart required (Phase A
//! sub-project 2) — any request after the response comes back is guaranteed to see the new
//! registry.
//!
//! Route handlers are split one file per resource (`routes/entities`/`draft`/`publish`/`audit`/
//! `import_export`) purely to keep each file a manageable size — [`router`] wires them all
//! together unchanged, and `apply_registry` is still reachable at this crate's root.

pub mod openapi_paths;
mod routes;

use axum::response::Response;
use axum::routing::get;
use axum::Router;
use metap_http::error::{internal_error_response, router_unavailable_response};
use metap_http::AppState;
use metap_permission::RequestContext;
use sqlx::PgPool;

pub use routes::publish::apply_registry;

/// Resolves the `PgPool` a low-code request should actually read/write — `Router::pool_for`,
/// the same tenant→pool resolution every other tenant-scoped route uses. Low-code entity
/// definitions are still global *within that pool* (see this file's top doc comment — no
/// `tenant_id` column exists on `low_code_entity_drafts`/`..._versions`, deliberate Phase A
/// design), but which pool a given request lands in must still route through the caller's own
/// tenant: previously every handler here read `state.pool` directly (the platform's shared
/// pool, always — `AppState.pool` is never `Router`-resolved on its own), so a `DedicatedDb`
/// tenant (e.g. `apps/jira-server`'s tenant) publishing a low-code entity would have silently
/// written it into the *platform's* database instead of its own — the same class of leak
/// `dev-tools`/login already had and fixed (`docs/roadmap/28-dev-tools-tenant-aware.md`). For a
/// `Schema`-strategy tenant `pool_for` returns the same shared pool as before (no behavior
/// change there); for `DedicatedDb` it now correctly returns that tenant's own pool, so its
/// low-code entities live in its own database rather than mixed into the platform's.
pub(crate) async fn resolve_pool(state: &AppState, context: &RequestContext) -> Result<PgPool, Box<Response>> {
    let tenant_id = state
        .permissions
        .scoped_tenant(context)
        .map_err(|e| Box::new(internal_error_response(e)))?;
    state
        .router
        .pool_for(tenant_id.into())
        .await
        .map_err(|e| Box::new(router_unavailable_response(e)))
}

pub fn router() -> Router<AppState> {
    use routes::audit::{list_audit_events, list_recent_audit_events};
    use routes::draft::{get_draft, save_draft};
    use routes::entities::{list_entities, set_enabled};
    use routes::import_export::{export_entities, import_entities};
    use routes::publish::{get_published, list_versions, preview_publish, publish, rollback};

    Router::new()
        .route("/admin/lowcode/entities", get(list_entities))
        .route("/admin/lowcode/entities/{name}", axum::routing::patch(set_enabled))
        .route("/admin/lowcode/entities/{name}/draft", get(get_draft).put(save_draft))
        .route("/admin/lowcode/entities/{name}/publish", axum::routing::post(publish))
        .route(
            "/admin/lowcode/entities/{name}/publish/preview",
            axum::routing::post(preview_publish),
        )
        .route("/admin/lowcode/entities/{name}/rollback", axum::routing::post(rollback))
        .route("/admin/lowcode/entities/{name}/published", get(get_published))
        .route("/admin/lowcode/entities/{name}/versions", get(list_versions))
        .route("/admin/lowcode/entities/{name}/audit", get(list_audit_events))
        .route("/admin/lowcode/audit", get(list_recent_audit_events))
        .route("/admin/lowcode/export", get(export_entities))
        .route("/admin/lowcode/import", axum::routing::post(import_entities))
}
