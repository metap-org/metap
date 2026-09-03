//! `/platform/config*` — fleet-wide platform tunables (`routes::platform_config`). See `super`'s
//! doc comment.
//!
//! Only the `PlatformGlobal` tier is described here because only that tier is reachable over HTTP
//! at all: `metap_config`'s `Operator` keys are settable through deployment configuration alone
//! (see that module's doc comment), so there is nothing about them for a generated client to call.

use serde_json::{json, Map, Value};

use super::insert;

pub(crate) fn platform_config_paths(paths: &mut Map<String, Value>) {
    insert(
        paths,
        "/platform/config",
        json!({
            "get": {
                "summary": "List every platform-global config key with its effective value",
                "description": "Requires the platform_admin role. Operator-tier keys are never listed.",
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
                "summary": "Set one platform-global config key",
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
}
