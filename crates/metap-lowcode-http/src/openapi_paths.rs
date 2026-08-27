//! Hand-written OpenAPI path fragments for this crate's `/admin/lowcode/*` surface — same
//! reasoning and style as `metap_http::openapi_paths` (this crate doesn't touch
//! `MetadataRegistry`'s dynamic per-entity generation at all, so there's nothing to derive
//! this from). `apps/crm-server/src/main.rs` merges this into `AppState::extra_openapi_paths`
//! before `build_router` — `metap-http` has zero dependency on this crate (see this crate's top
//! doc comment), so the merge has to happen at the composition root, not inside `metap-http`.

use metap_metadata::openapi::{entity_field_json_schema, entity_list_view_json_schema, entity_workflow_json_schema};
use serde_json::{json, Map, Value};

fn insert(paths: &mut Map<String, Value>, path: &str, item: Value) {
    paths.insert(path.to_string(), item);
}

/// The wire shape `LowCodeEntityDefinition` round-trips as (`metap-lowcode`'s `definition.rs`,
/// `#[serde(rename_all = "camelCase")]`) — reuses the same field/listView/workflow schemas
/// `EntitySummary` does, since it's `EntityField`/`EntityListView`/`EntityWorkflow` verbatim.
/// `with_name = false` omits `name` for the draft PUT body (the entity name is the `{name}`
/// path segment, never repeated in the body).
fn lowcode_definition_schema(with_name: bool) -> Value {
    let mut properties = serde_json::Map::new();
    if with_name {
        properties.insert("name".to_string(), json!({ "type": "string" }));
    }
    properties.insert("label".to_string(), json!({ "type": "string" }));
    properties.insert(
        "fields".to_string(),
        json!({ "type": "array", "items": entity_field_json_schema() }),
    );
    properties.insert(
        "listViews".to_string(),
        json!({ "type": "array", "items": entity_list_view_json_schema() }),
    );
    properties.insert("workflow".to_string(), entity_workflow_json_schema());
    json!({ "type": "object", "properties": properties, "required": ["label"] })
}

fn audit_event_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "entityName": { "type": "string" },
            "action": { "type": "string", "enum": ["draft_saved", "published", "rolled_back", "enabled", "disabled"] },
            "actorUserId": { "type": ["string", "null"] },
            "actorTenantId": { "type": ["string", "null"] },
            "versionNumber": { "type": ["integer", "null"] },
            "restoredFromVersion": { "type": ["integer", "null"] },
            "occurredAt": { "type": "string", "format": "date-time" },
        },
    })
}

pub fn openapi_paths() -> Map<String, Value> {
    let mut paths = Map::new();

    insert(
        &mut paths,
        "/admin/lowcode/entities",
        json!({
            "get": {
                "summary": "List every DB-authored entity (draft/published status, enabled flag)",
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "object", "properties": { "entities": { "type": "array", "items": {
                        "type": "object",
                        "properties": {
                            "name": { "type": "string" },
                            "published": { "type": "boolean" },
                            "enabled": { "type": "boolean" },
                        },
                    } } } } },
                } } } } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}",
        json!({
            "patch": {
                "summary": "Enable/disable a published entity — takes effect immediately, no restart",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "enabled": { "type": "boolean" } }, "required": ["enabled"],
                } } } },
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}/draft",
        json!({
            "get": {
                "summary": "Get the current draft for an entity",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": lowcode_definition_schema(true) },
                    } } } },
                    "404": { "description": "No draft exists for this entity" },
                },
            },
            "put": {
                "summary": "Save (create or overwrite) the draft for an entity",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "requestBody": { "content": { "application/json": { "schema": lowcode_definition_schema(false) } } },
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}/publish",
        json!({
            "post": {
                "summary": "Publish the current draft as a new version — validates shape, name-reservation, cross-reference",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": { "type": "object", "properties": {
                            "versionNumber": { "type": "integer" },
                        } } },
                    } } } },
                    "404": { "description": "No draft exists for this entity" },
                    "409": { "description": "Entity name reserved by a code-authored entity" },
                    "422": { "description": "Draft failed shape validation" },
                },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}/publish/preview",
        json!({
            "post": {
                "summary": "Dry-run `publish` — same checks, no side effect",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "data": { "type": "object", "properties": {
                        "wouldBeVersion": { "type": "integer" },
                        "valid": { "type": "boolean" },
                        "impact": {},
                    } } },
                } } } } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}/rollback",
        json!({
            "post": {
                "summary": "Restore a previously published version as the new current version",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "toVersionNumber": { "type": "integer" } },
                    "required": ["toVersionNumber"],
                } } } },
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": { "type": "object", "properties": {
                            "versionNumber": { "type": "integer" },
                        } } },
                    } } } },
                    "404": { "description": "Version not found" },
                },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}/published",
        json!({
            "get": {
                "summary": "Get the currently published version's definition",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": { "type": "object", "properties": {
                            "versionNumber": { "type": "integer" },
                            "definition": lowcode_definition_schema(true),
                            "publishedAt": { "type": "string", "format": "date-time" },
                            "restoredFromVersion": { "type": ["integer", "null"] },
                        } } },
                    } } } },
                    "404": { "description": "Never published" },
                },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}/versions",
        json!({
            "get": {
                "summary": "List every published version's metadata (newest first)",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "array", "items": { "type": "object", "properties": {
                        "versionNumber": { "type": "integer" },
                        "publishedAt": { "type": "string", "format": "date-time" },
                        "restoredFromVersion": { "type": ["integer", "null"] },
                    } } } },
                } } } } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/entities/{name}/audit",
        json!({
            "get": {
                "summary": "Audit log for one entity (draft-saved/published/rolled-back/enabled/disabled)",
                "parameters": [{ "name": "name", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "data": { "type": "array", "items": audit_event_schema() } },
                } } } } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/audit",
        json!({
            "get": {
                "summary": "Cross-entity audit feed for the caller's tenant, newest first",
                "parameters": [{ "name": "limit", "in": "query", "required": false, "schema": { "type": "integer", "maximum": 200 } }],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "data": { "type": "array", "items": audit_event_schema() } },
                } } } } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/export",
        json!({
            "get": {
                "summary": "Export published entity definitions as a portable snapshot",
                "parameters": [{ "name": "entities", "in": "query", "required": false, "schema": { "type": "string" }, "description": "Comma-separated entity names; omitted exports every published entity" }],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "object", "properties": {
                        "entities": { "type": "array", "items": { "type": "object", "properties": {
                            "name": { "type": "string" }, "definition": lowcode_definition_schema(true),
                        } } },
                        "notFound": { "type": "array", "items": { "type": "string" } },
                    } } },
                } } } } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/lowcode/import",
        json!({
            "post": {
                "summary": "Import a snapshot as drafts (never auto-publishes) — best-effort, per-entity outcome",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "entities": { "type": "array", "items": { "type": "object", "properties": {
                        "name": { "type": "string" }, "definition": lowcode_definition_schema(false),
                    }, "required": ["name", "definition"] } } },
                    "required": ["entities"],
                } } } },
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "object", "properties": {
                        "imported": { "type": "array", "items": { "type": "string" } },
                        "failed": { "type": "array", "items": { "type": "object", "properties": {
                            "name": { "type": "string" }, "error": { "type": "string" },
                        } } },
                    } } },
                } } } } },
            },
        }),
    );

    paths
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn covers_every_lowcode_route() {
        let paths = openapi_paths();
        for expected in [
            "/admin/lowcode/entities",
            "/admin/lowcode/entities/{name}",
            "/admin/lowcode/entities/{name}/draft",
            "/admin/lowcode/entities/{name}/publish",
            "/admin/lowcode/entities/{name}/publish/preview",
            "/admin/lowcode/entities/{name}/rollback",
            "/admin/lowcode/entities/{name}/published",
            "/admin/lowcode/entities/{name}/versions",
            "/admin/lowcode/entities/{name}/audit",
            "/admin/lowcode/audit",
            "/admin/lowcode/export",
            "/admin/lowcode/import",
        ] {
            assert!(paths.contains_key(expected), "missing path: {expected}");
        }
    }
}
