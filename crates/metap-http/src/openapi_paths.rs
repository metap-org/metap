//! Hand-written OpenAPI path fragments for this crate's own static (non-entity) routes —
//! `routes::health`/`preferences`/`users`/`auth`/`admin`/`cron`/`dashboards`/`attachments`/
//! `workflow_events`. Same style as `metap_metadata::generate_openapi_document` (this crate has
//! no Zod-equivalent reflection step either — see that function's doc comment), extended here
//! rather than there because these routes aren't derived from `MetadataRegistry`: they're fixed
//! per binary, not per registered entity. `routes::metadata::openapi_json` merges this map into
//! `paths` alongside the per-entity ones. `routes::records`'s `/api/{entity}*` paths stay solely
//! `generate_openapi_document`'s job — this file only covers what that function can't reach.
//!
//! `GET /metrics` is deliberately omitted — it serves Prometheus text exposition format, not
//! JSON, so there's nothing here for `openapi-typescript` to usefully describe.
//!
//! Split into one file per resource group (`core`/`auth`/`admin`/`cron_dashboards`/
//! `attachments_workflow`) purely to keep each file a manageable size — [`static_paths`] just
//! calls each group's function in the same order the paths used to be inserted, so the returned
//! map is unchanged.

mod admin;
mod attachments_workflow;
mod auth;
mod core;
mod cron_dashboards;
mod platform_config;

use serde_json::{Map, Value};

use admin::admin_paths;
use attachments_workflow::attachment_workflow_paths;
use auth::auth_paths;
use core::core_paths;
use cron_dashboards::cron_dashboard_paths;
use platform_config::platform_config_paths;

pub(crate) use metap_runtime::openapi::insert;

pub fn static_paths() -> Map<String, Value> {
    let mut paths = Map::new();
    core_paths(&mut paths);
    auth_paths(&mut paths);
    admin_paths(&mut paths);
    cron_dashboard_paths(&mut paths);
    attachment_workflow_paths(&mut paths);
    platform_config_paths(&mut paths);
    paths
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
        ] {
            assert!(paths.contains_key(expected), "missing path: {expected}");
        }
    }
}
