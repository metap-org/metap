//! `/platform/config*`, `/admin/config*` and `/public/config` — the three config surfaces
//! (`routes::platform_config`, `routes::tenant_config`). See `super`'s doc comment.
//!
//! The `Operator` tier is absent because it is not reachable over HTTP at all: those keys are
//! settable through deployment configuration alone (see `metap_config::keys`'s doc comment), so
//! there is nothing about them for a generated client to call.

use serde_json::{json, Map, Value};

use super::insert;

pub(crate) fn platform_config_paths(paths: &mut Map<String, Value>) {
    insert(
        paths,
        "/platform/config",
        json!({
            "get": {
                "summary": "List every platform-writable config key with its effective value",
                "description": "Requires the platform_admin role. Covers both the platformGlobal tier and \
                                the fleet default of each tenant tier key (level/tenantOverridable say \
                                which). Operator-tier keys are never listed.",
                "responses": {
                    "200": { "description": "OK" },
                    "403": { "description": "Caller is not a platform admin" },
                },
            },
        }),
    );
    insert(
        paths,
        "/platform/config/{key}",
        json!({
            "put": {
                "summary": "Set one platform-global config key, or a tenant key's fleet default",
                "description": "Rejects an operator-tier key with 403 and an out-of-range value with 422. \
                                The response's appliesImmediately reports whether the change takes effect \
                                without a restart (false for the rate-limit keys, which are baked into a \
                                middleware layer at router-build time).",
                "parameters": [{ "name": "key", "in": "path", "required": true, "schema": { "type": "string" } }],
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "value": {} },
                    "required": ["value"],
                } } } },
                "responses": {
                    "200": { "description": "Stored" },
                    "403": { "description": "Key is not writable at this tier" },
                    "404": { "description": "No such config key" },
                    "422": { "description": "Value rejected by the key's validator" },
                },
            },
            "delete": {
                "summary": "Clear an override so the key falls back to its declared default",
                "parameters": [{ "name": "key", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": {
                    "200": { "description": "Reset, returns the default now in effect" },
                    "403": { "description": "Key is not writable at this tier" },
                    "404": { "description": "No such config key" },
                },
            },
        }),
    );
    insert(
        paths,
        "/admin/config",
        json!({
            "get": {
                "summary": "List this tenant's config keys with their effective values",
                "description": "Any authenticated caller. The tenant comes from the token, never from the \
                                request, so there is no way to read another tenant's values. `overridden` \
                                distinguishes a value this tenant set from one inherited from the fleet \
                                default.",
                "responses": {
                    "200": { "description": "OK" },
                    "401": { "description": "Not authenticated" },
                },
            },
        }),
    );
    insert(
        paths,
        "/admin/config/{key}",
        json!({
            "put": {
                "summary": "Set one config key for the caller's own tenant",
                "description": "Requires the admin role. Rejects a platform-global or operator-tier key \
                                with 403 and an out-of-range value with 422.",
                "parameters": [{ "name": "key", "in": "path", "required": true, "schema": { "type": "string" } }],
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "value": {} },
                    "required": ["value"],
                } } } },
                "responses": {
                    "200": { "description": "Stored" },
                    "403": { "description": "Key is not writable by a tenant admin" },
                    "404": { "description": "No such config key" },
                    "422": { "description": "Value rejected by the key's validator" },
                },
            },
            "delete": {
                "summary": "Clear this tenant's override so the key falls back to the fleet default",
                "parameters": [{ "name": "key", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": {
                    "200": { "description": "Reset, returns the inherited value now in effect" },
                    "403": { "description": "Key is not writable by a tenant admin" },
                    "404": { "description": "No such config key" },
                },
            },
        }),
    );
    insert(
        paths,
        "/public/config",
        json!({
            "get": {
                "summary": "Branding for the tenant serving this hostname, before any login",
                "description": "Unauthenticated. Returns only keys declared public in the config registry \
                                (theme colour, logo, display name) — never any other key, at any tier. The \
                                tenant is resolved from the Host header; an unrecognised hostname returns \
                                the fleet-wide values rather than a 404, so this endpoint cannot be used to \
                                discover which hostnames belong to a tenant.",
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );
}
