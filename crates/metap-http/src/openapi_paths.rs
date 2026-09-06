//! OpenAPI `paths`/`components.schemas` for this crate's own static (non-entity) routes —
//! `routes::health`/`preferences`/`users`/`auth`/`admin`/`cron`/`dashboards`/`attachments`/
//! `workflow_events`/`platform_config`/`tenant_config`. Derived from each of those modules'
//! `#[utoipa::path(...)]` annotations (2026-09-06, `../metap-lowcode/docs/features/
//! 02-utoipa-migration.md`), not hand-written JSON anymore — see that doc for the migration's
//! full history (this was the third and final crate converted, after `metap-control-http`/
//! `metap-lowcode-http`).
//!
//! `routes::metadata::openapi_json` merges [`static_paths`]/[`static_schemas`] into the served
//! document alongside the per-entity dynamic ones (`metap_metadata::generate_openapi_document`)
//! and whatever optional platform capability the composition root wired in
//! (`AppState.extra_openapi_paths`/`extra_openapi_schemas`). `routes::records`'s `/api/{entity}*`
//! CRUD paths stay solely `generate_openapi_document`'s job — entities aren't known at compile
//! time, incompatible with `utoipa`'s macro model, so that generator stays hand-written forever.
//!
//! `GET /metrics` is deliberately omitted — it serves Prometheus text exposition format, not
//! JSON, so there's nothing here for `openapi-typescript` to usefully describe. `GET /auth/logout`,
//! `GET /auth/token`, and `GET /admin/cron-jobs/{jobId}/runs/{runId}/workflow-run` are also
//! deliberately undocumented — plain `axum` routes with no `#[utoipa::path]`, matching what the
//! old hand-written fragments covered (they never documented these three either).

use serde_json::{Map, Value};

/// Merges every converted module's own `OpenApi` fragment into one document — mirrors how
/// `static_paths()` used to call each hand-written group's `_paths(&mut paths)` function in
/// sequence, just via `utoipa::openapi::OpenApi::merge` instead of manual `Map` extension.
fn core_openapi() -> utoipa::openapi::OpenApi {
    let mut doc = crate::routes::health::openapi();
    for fragment in [
        crate::routes::users::openapi(),
        crate::routes::preferences::openapi(),
        crate::routes::workflow_events::openapi(),
        crate::routes::attachments::openapi(),
        crate::routes::auth::openapi(),
        crate::routes::admin::openapi(),
        crate::routes::cron::openapi(),
        crate::routes::dashboards::openapi(),
        crate::routes::platform_config::openapi(),
        crate::routes::tenant_config::openapi(),
    ] {
        doc.merge(fragment);
    }
    doc
}

fn core_openapi_value() -> Value {
    serde_json::to_value(core_openapi()).expect("utoipa::openapi::OpenApi always serializes")
}

pub fn static_paths() -> Map<String, Value> {
    core_openapi_value()
        .get("paths")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default()
}

/// `components.schemas` for every type `static_paths()`'s operations reference via `$ref` —
/// `routes::metadata::openapi_json` must merge this alongside `static_paths()`, or those `$ref`s
/// resolve to nothing in the served document (see `AppState.extra_openapi_schemas`'s doc comment
/// for the general shape of this problem, first found and fixed for `metap-control-http`).
pub fn static_schemas() -> Map<String, Value> {
    core_openapi_value()
        .get("components")
        .and_then(|c| c.get("schemas"))
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn covers_every_static_route_group() {
        let paths = static_paths();
        for expected in [
            "/health",
            "/preferences",
            "/users",
            "/auth/login",
            "/auth/me",
            "/admin/users",
            "/admin/policies",
            "/admin/cron-jobs",
            "/dashboards/me",
            "/api/{entity}/{record_id}/attachments",
            "/api/{entity}/{record_id}/workflow-events",
            "/platform/config",
            "/platform/config/{key}",
            "/admin/config",
            "/admin/config/{key}",
            "/public/config",
        ] {
            assert!(paths.contains_key(expected), "missing path: {expected}");
        }
    }

    /// Every `$ref` this crate's own fragments produce must resolve into `static_schemas()` —
    /// the exact class of bug found live in `metap-control-http` (2026-09-06): a response DTO's
    /// schema is always a `$ref`, never inlined, so a merge that forgets `components.schemas`
    /// leaves it dangling in the served document.
    #[test]
    fn every_dollar_ref_resolves_into_static_schemas() {
        let schemas = static_schemas();
        let mut refs = Vec::new();
        collect_refs(&Value::Object(static_paths()), &mut refs);
        for r in refs {
            let name = r.rsplit('/').next().unwrap();
            assert!(schemas.contains_key(name), "dangling $ref: {r}");
        }
    }

    fn collect_refs(value: &Value, out: &mut Vec<String>) {
        match value {
            Value::Object(map) => {
                if let Some(Value::String(r)) = map.get("$ref") {
                    out.push(r.clone());
                }
                for v in map.values() {
                    collect_refs(v, out);
                }
            }
            Value::Array(items) => {
                for v in items {
                    collect_refs(v, out);
                }
            }
            _ => {}
        }
    }
}
