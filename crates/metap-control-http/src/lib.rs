//! Self-service tenant provisioning over HTTP (Phase 16 Giai đoạn 3, `docs/roadmap.md`) — the
//! HTTP counterpart of `dev-tools provision-tenant`, both calling the exact same
//! `metap_control::provision_schema_tenant`/`provision_dedicated_db_tenant` functions so a
//! CLI-provisioned tenant and an HTTP-provisioned one can't diverge. Every handler here uses
//! `metap_http::auth::PlatformAdminContext`, not `AdminContext` — a normal tenant admin (even
//! one with the `"admin"` role) is not authorized to create *other* tenants; only a user in
//! `metap_control::PLATFORM_TENANT_ID` with the `"platform_admin"` role is. Bootstrap the first
//! one with `dev-tools bootstrap-platform-admin` (no HTTP path exists for that, same
//! con-gà-quả-trứng reasoning as `seed-admin`/`create-user`).
//!
//! **Deliberately its own crate, not a module inside `metap-http`** — same reasoning as
//! `metap-lowcode-http`'s doc comment: self-service SaaS tenant provisioning is an optional
//! platform capability (Phase 16 is itself trigger-based), not something every downstream
//! project wants built into its binary. `metap-http` already depends on `metap-control` (for
//! `Router`), but has zero dependency on this crate — a binary that wants this surface merges
//! [`router`] into `metap_http::build_router`'s `extra_routes` argument itself (see
//! `apps/crm-server/src/main.rs`).
//!
//! **Not implemented yet, deliberately** (Phase 16 Giai đoạn 3 scope, `docs/roadmap.md`):
//! suspend/resume (`PATCH /platform/tenants/{id}` toggling `status`) and delete/deprovision —
//! both need a real answer for what happens to a tenant's data first, left for whenever that
//! need is concrete rather than guessed at now.

use axum::extract::{Path, State};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router as AxumRouter};
use metap_control::{PostgresTenantRegistry, TenantId, TenantRegistry, TenantRouting, TenantStrategy, TenantSummary};
use metap_http::auth::PlatformAdminContext;
use metap_http::error::{internal_error_response, service_error_response};
use metap_http::AppState;
use serde::Deserialize;
use serde_json::{json, Value};
use uuid::Uuid;

fn strategy_json(strategy: &TenantStrategy) -> Value {
    match strategy {
        TenantStrategy::Schema { schema_name } => json!({ "type": "schema", "schemaName": schema_name }),
        TenantStrategy::DedicatedDb { dsn_secret_ref } => {
            json!({ "type": "dedicated_db", "dsnSecretRef": dsn_secret_ref })
        }
    }
}

fn tenant_routing_json(id: Uuid, routing: &TenantRouting) -> Value {
    json!({
        "id": id,
        "status": routing.status.as_str(),
        "strategy": strategy_json(&routing.strategy),
    })
}

fn tenant_summary_json(summary: &TenantSummary) -> Value {
    json!({
        "id": summary.id,
        "tier": summary.tier,
        "status": summary.status.as_str(),
        "strategy": strategy_json(&summary.strategy),
        "createdAt": summary.created_at,
        "trialExpiresAt": summary.trial_expires_at,
    })
}

/// Downcasts the way `metap-crud`'s `router_unavailable`/`unique_violation` already do —
/// `provision_schema_tenant`/`provision_dedicated_db_tenant` bubble a plain
/// `sqlx::Error::Database` (unique-violation on `control.tenants`'s primary key) up through
/// `?` into `anyhow::Error` untouched, so it's still downcastable at this layer.
fn duplicate_tenant_id_response(error: &anyhow::Error) -> Option<Response> {
    let sqlx::Error::Database(db_err) = error.downcast_ref::<sqlx::Error>()? else {
        return None;
    };
    if !db_err.is_unique_violation() {
        return None;
    }
    Some(service_error_response(
        409,
        "tenant_already_exists",
        Some("A tenant with this id already exists."),
        None,
    ))
}

#[derive(Deserialize)]
struct ProvisionTenantBody {
    #[serde(rename = "tenantId")]
    tenant_id: Uuid,
    /// `"schema"` (trial) or `"dedicated_db"` (paid) — see
    /// `metap_control::provision_schema_tenant`/`provision_dedicated_db_tenant`'s doc comments
    /// for what each actually does. `"schema"` still pins `schema_name` to `"public"` — no
    /// real per-tenant schema isolation yet (`docs/roadmap.md` Phase 16).
    strategy: String,
    #[serde(rename = "adminEmail")]
    admin_email: String,
    #[serde(rename = "adminPassword")]
    admin_password: String,
    /// Required (and only meaningful) when `strategy == "dedicated_db"` — the env var name
    /// `Router`'s `EnvStore` will look up to resolve this tenant's DSN.
    #[serde(rename = "dsnSecretRef")]
    dsn_secret_ref: Option<String>,
    /// Required (and only meaningful) when `strategy == "dedicated_db"` — the connection
    /// string this function migrates and creates the admin user on. The caller is responsible
    /// for actually setting `dsnSecretRef=<this value>` in the serving process's environment
    /// before the tenant is routed to; provisioning only writes the registry row.
    #[serde(rename = "dedicatedDatabaseUrl")]
    dedicated_database_url: Option<String>,
}

async fn provision_tenant(
    State(state): State<AppState>,
    PlatformAdminContext(_context): PlatformAdminContext,
    Json(body): Json<ProvisionTenantBody>,
) -> Response {
    let registry = PostgresTenantRegistry::new(state.pool.clone());

    let result = match body.strategy.as_str() {
        "schema" => {
            metap_control::provision_schema_tenant(
                &state.pool,
                &registry,
                body.tenant_id,
                &body.admin_email,
                &body.admin_password,
            )
            .await
        }
        "dedicated_db" => {
            let (Some(dsn_secret_ref), Some(dedicated_database_url)) =
                (&body.dsn_secret_ref, &body.dedicated_database_url)
            else {
                return service_error_response(
                    400,
                    "validation_failed",
                    Some("`dsnSecretRef` and `dedicatedDatabaseUrl` are required when strategy is \"dedicated_db\"."),
                    None,
                );
            };
            metap_control::provision_dedicated_db_tenant(
                &registry,
                body.tenant_id,
                dsn_secret_ref,
                dedicated_database_url,
                &body.admin_email,
                &body.admin_password,
            )
            .await
        }
        other => {
            return service_error_response(
                400,
                "validation_failed",
                Some(&format!(
                    "Unknown strategy \"{other}\" — must be \"schema\" or \"dedicated_db\"."
                )),
                None,
            )
        }
    };

    match result {
        Ok(provisioned) => Json(json!({
            "data": { "tenantId": provisioned.tenant_id, "adminUserId": provisioned.admin_user_id }
        }))
        .into_response(),
        Err(e) => duplicate_tenant_id_response(&e).unwrap_or_else(|| internal_error_response(e)),
    }
}

async fn list_tenants(State(state): State<AppState>, PlatformAdminContext(_context): PlatformAdminContext) -> Response {
    let registry = PostgresTenantRegistry::new(state.pool.clone());
    match registry.list().await {
        Ok(summaries) => {
            let data: Vec<_> = summaries.iter().map(tenant_summary_json).collect();
            Json(json!({ "data": data })).into_response()
        }
        Err(e) => internal_error_response(e),
    }
}

async fn get_tenant(
    State(state): State<AppState>,
    Path(id): Path<Uuid>,
    PlatformAdminContext(_context): PlatformAdminContext,
) -> Response {
    let registry = PostgresTenantRegistry::new(state.pool.clone());
    match registry.get(TenantId::from(id)).await {
        Ok(Some(routing)) => Json(json!({ "data": tenant_routing_json(id, &routing) })).into_response(),
        Ok(None) => service_error_response(404, "tenant_not_found", None, None),
        Err(e) => internal_error_response(e),
    }
}

/// Merge this into `metap_http::build_router`'s `extra_routes` argument to expose the
/// platform-tenant admin API on a running server — never merged automatically by `metap-http`
/// itself.
pub fn router() -> AxumRouter<AppState> {
    AxumRouter::new()
        .route("/platform/tenants", post(provision_tenant).get(list_tenants))
        .route("/platform/tenants/{id}", get(get_tenant))
}
