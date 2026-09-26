//! OpenAPI `paths`/`components.schemas` for this crate's own static (non-entity) routes —
//! `routes::health`/`auth`/`attachments`/`workflow_events`/`audit_events`/`oauth2`. Derived from
//! each of those modules' `#[utoipa::path(...)]` annotations (2026-09-06, `../metap-lowcode/docs/
//! features/02-utoipa-migration.md`), not hand-written JSON anymore — see that doc for the
//! migration's full history (this was the third and final crate converted, after
//! `metap-control-http`/`metap-lowcode-http`).
//!
//! **`routes::{admin,cron,dashboards,preferences,platform_config,tenant_config,users}` are gone**
//! (2026-09-26, `../metap-docs/docs/roadmap/95-platform-graphql-fields.md`) — those groups moved
//! to GraphQL-only, hand-written fields (`metap-graphql-http::platform_fields`), which have no
//! OpenAPI document at all (`GET /graphql/schema.graphql`'s SDL is their schema-discovery
//! equivalent, same relationship `/api/:entity*`'s removal already established for entity CRUD
//! below). `routes::oauth2` lost only its 3 admin-CRUD routes the same way — its 5 protocol
//! routes (`/oauth/*`, `/.well-known/*`) are unaffected and still contribute a fragment here.
//!
//! `routes::metadata::openapi_json` merges [`static_paths`]/[`static_schemas`] into the served
//! document alongside `metap_metadata::generate_openapi_document`'s own `/metadata/*` static
//! paths and whatever optional platform capability the composition root wired in
//! (`AppState.extra_openapi_paths`/`extra_openapi_schemas`). **There is no `routes::records`
//! anymore** (removed 2026-09-21 alongside REST `/api/:entity*` — entity access is GraphQL-only
//! now, `/graphql/schema.graphql` from `metap-graphql-http` is its schema-discovery equivalent),
//! so `generate_openapi_document` no longer generates a per-entity path block either — this
//! module's own static fragments are the entire document now, `/metadata/*` aside.
//!
//! `GET /metrics` is deliberately omitted — it serves Prometheus text exposition format, not
//! JSON, so there's nothing here for `openapi-typescript` to usefully describe. `GET /auth/logout`
//! and `GET /auth/token` are also deliberately undocumented — plain `axum` routes with no
//! `#[utoipa::path]`, matching what the old hand-written fragments covered (they never documented
//! these two either).

use serde_json::{Map, Value};

/// Merges every converted module's own `OpenApi` fragment into one document — mirrors how
/// `static_paths()` used to call each hand-written group's `_paths(&mut paths)` function in
/// sequence, just via `utoipa::openapi::OpenApi::merge` instead of manual `Map` extension.
fn core_openapi() -> utoipa::openapi::OpenApi {
    let mut doc = crate::routes::health::openapi();
    for fragment in [
        crate::routes::workflow_events::openapi(),
        crate::routes::audit_events::openapi(),
        crate::routes::attachments::openapi(),
        crate::routes::auth::openapi(),
        crate::routes::oauth2::openapi(),
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
            "/auth/login",
            "/auth/me",
            "/api/{entity}/{record_id}/attachments",
            "/api/{entity}/{record_id}/workflow-events",
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
