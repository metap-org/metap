//! `packages/platform-react`'s `generate:types` (`openapi-typescript` against this
//! document's JSON) consumes `/metadata/openapi.json` over HTTP, not backend source — the
//! generator here just needs to keep producing the same field-kind → JSON Schema mapping.
//!
//! The `components.schemas.EntitySummary` entry is hand-written here (mirroring
//! `entity-wire-schema.ts`'s `EntitySummarySchema` field-for-field) rather than derived
//! from a schema library, since this crate has no Zod-equivalent reflection step — the
//! wire shape is already fixed by `entity.rs`'s serde `rename_all = "camelCase"` structs,
//! this is just its JSON Schema description for `$ref`.

use serde_json::{json, Value};

/// `pub` (unlike the rest of this file's schema builders) so `metap-lowcode-http` can describe
/// its draft/publish/export/import request-and-response bodies — which embed
/// `Vec<EntityField>`/`Vec<EntityListView>`/`Option<EntityWorkflow>` verbatim, the same wire
/// shape `EntitySummary` uses — without duplicating this mapping by hand.
pub fn entity_field_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "name": { "type": "string" },
            "label": { "type": "string" },
            "kind": {
                "type": "string",
                "enum": ["id", "string", "number", "boolean", "date", "datetime", "money", "enum", "reference", "json"],
            },
            "required": { "type": "boolean" },
            "indexed": { "type": "boolean" },
            "unique": { "type": "boolean" },
            "enumValues": { "type": "array", "items": { "type": "string" } },
            "refEntity": { "type": "string" },
            "refDisplayField": { "type": "string" },
            "searchable": { "type": "boolean" },
            "searchMode": { "type": "string", "enum": ["substring", "fts"] },
            "sortable": { "type": "boolean" },
            "storage": { "type": "string", "enum": ["native", "column"] },
            "min": { "type": "number" },
            "max": { "type": "number" },
            "minLength": { "type": "number" },
            "maxLength": { "type": "number" },
            "computed": computed_spec_json_schema(),
        },
        "required": ["name", "label", "kind"],
    })
}

/// See `EntityField.computed`/`ComputedSpec`'s doc comments (`entity.rs`) —
/// `docs/features/13-computed-derived-field.md`.
fn computed_spec_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "expression": { "type": "string" },
            "dependsOn": { "type": "array", "items": { "type": "string" } },
        },
        "required": ["expression", "dependsOn"],
    })
}

/// `pub` — see [`entity_field_json_schema`]'s doc comment.
pub fn entity_list_view_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "name": { "type": "string" },
            "label": { "type": "string" },
            "fields": { "type": "array", "items": { "type": "string" } },
            "filters": { "type": "array", "items": { "type": "string" } },
            "requiredFields": { "type": "array", "items": { "type": "string" } },
            "defaultSort": { "type": "string" },
            "maxLimit": { "type": "number" },
        },
        "required": ["name", "label", "fields", "filters", "maxLimit"],
    })
}

fn workflow_transition_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "action": { "type": "string" },
            "from": { "type": "string" },
            "to": { "type": "string" },
            "label": { "type": "string" },
            // PolicyCondition (metap-permission) is a recursive untagged enum (Attribute /
            // All / Any) — same reasoning as FieldKind::Json below: left untyped rather than
            // hand-modeling the recursion, since this generator's job is describing entity
            // `data` shape for CRUD forms, not re-deriving metap-permission's wire format.
            "guard": {},
            // `validator` (a second PolicyCondition, checked against the post-merge payload) and
            // `setFields` (declarative post-function) were on `entity.rs`'s WorkflowTransition but
            // missing here, so `platform-ui`'s generated types never learned they exist — exactly
            // the hand-maintained drift this file's own doc comment warns about, found 2026-09-03
            // (`platform-ui/docs/audits/02-auth-permission-workflow-diagram-audit.md` finding C1).
            // Both left untyped for the same reason `guard` is: PolicyCondition/PolicyValue are
            // metap-permission's wire format, not this generator's to re-derive.
            "validator": {},
            "setFields": { "type": "object" },
        },
        "required": ["action", "from", "to", "label"],
    })
}

/// `pub` — see [`entity_field_json_schema`]'s doc comment.
pub fn related_view_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "name": { "type": "string" },
            "label": { "type": "string" },
            "entity": { "type": "string" },
            "filterField": { "type": "string" },
            "fields": { "type": "array", "items": { "type": "string" } },
            "limit": { "type": "number" },
        },
        "required": ["name", "label", "entity", "filterField", "fields"],
    })
}

/// `pub` — see [`entity_field_json_schema`]'s doc comment.
pub fn field_display_hint_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "field": { "type": "string" },
            "resolveVia": { "type": "string" },
            "enumTones": { "type": "object", "additionalProperties": { "type": "string" } },
        },
        "required": ["field"],
    })
}

/// `pub` — see [`entity_field_json_schema`]'s doc comment.
pub fn entity_workflow_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "stateField": { "type": "string" },
            "initialState": { "type": "string" },
            "terminalStates": { "type": "array", "items": { "type": "string" } },
            "transitions": { "type": "array", "items": workflow_transition_json_schema() },
        },
        "required": ["stateField", "initialState", "terminalStates", "transitions"],
    })
}

fn entity_summary_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "name": { "type": "string" },
            "label": { "type": "string" },
            "fields": { "type": "array", "items": entity_field_json_schema() },
            "listViews": { "type": "array", "items": entity_list_view_json_schema() },
            "workflow": entity_workflow_json_schema(),
            "relatedViews": { "type": "array", "items": related_view_json_schema() },
            "fieldDisplayHints": { "type": "array", "items": field_display_hint_json_schema() },
            "audit": entity_audit_config_json_schema(),
            "version": { "type": "string" },
        },
        "required": ["name", "label", "fields", "listViews", "version"],
    })
}

/// See `metap_metadata::EntityAuditConfig`'s own doc comment. Hand-maintained like the rest of
/// this file, so it has to be kept in step with that struct by hand — `redactedFields` is
/// optional here because it is `#[serde(default)]` there.
fn entity_audit_config_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "enabled": { "type": "boolean" },
            "redactedFields": { "type": "array", "items": { "type": "string" } },
        },
        "required": ["enabled"],
    })
}

/// `/metadata/*`'s own static paths, plus the `EntitySummary` component schema every one of them
/// can reference. **No longer takes an `entities: &[EntitySummary]` parameter** (removed
/// 2026-09-21) — this used to also generate a per-entity `/api/{entity}*` CRUD path block
/// (list/create/get/update/delete/transition), dropped alongside `metap-http`'s REST
/// `/api/:entity*` surface itself (entity access is GraphQL-only now — see that crate's own
/// `CLAUDE.md` bullet). `/graphql/schema.graphql` (`metap-graphql-http`) is that API's own
/// schema-discovery equivalent, not this document.
pub fn generate_openapi_document() -> Value {
    let mut paths = serde_json::Map::new();

    paths.insert(
        "/metadata/entities".to_string(),
        json!({
            "get": {
                "summary": "List entity metadata",
                "responses": {
                    "200": {
                        "description": "OK",
                        "content": {
                            "application/json": {
                                "schema": {
                                    "type": "object",
                                    "properties": {
                                        "data": {
                                            "type": "array",
                                            "items": { "$ref": "#/components/schemas/EntitySummary" },
                                        },
                                    },
                                },
                            },
                        },
                    },
                },
            },
        }),
    );

    paths.insert(
        "/metadata/entities/{entity}".to_string(),
        json!({
            "get": {
                "summary": "Get one entity's metadata",
                "responses": {
                    "200": {
                        "description": "OK",
                        "content": {
                            "application/json": {
                                "schema": {
                                    "type": "object",
                                    "properties": { "data": { "$ref": "#/components/schemas/EntitySummary" } },
                                },
                            },
                        },
                    },
                    "404": { "description": "Not found" },
                },
            },
        }),
    );

    paths.insert(
        "/metadata/actions".to_string(),
        json!({
            "get": {
                "summary": "List the fixed set of actions a policy can grant",
                "responses": {
                    "200": {
                        "description": "OK",
                        "content": {
                            "application/json": {
                                "schema": {
                                    "type": "object",
                                    "properties": {
                                        "data": { "type": "array", "items": { "type": "string" } },
                                    },
                                },
                            },
                        },
                    },
                },
            },
        }),
    );

    json!({
        "openapi": "3.1.0",
        "info": { "title": "Metap API", "version": "1.0.0" },
        "paths": Value::Object(paths),
        "components": {
            "schemas": {
                "EntitySummary": entity_summary_json_schema(),
            },
        },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_the_static_metadata_paths_are_generated() {
        let doc = generate_openapi_document();
        assert!(doc["paths"]["/metadata/entities"]["get"].is_object());
        assert!(doc["paths"]["/metadata/entities/{entity}"]["get"].is_object());
        assert!(doc["paths"]["/metadata/actions"]["get"].is_object());
        // No per-entity `/api/{entity}*` CRUD path — REST entity access is gone, this document
        // no longer takes an `entities` list to generate one from at all.
        assert_eq!(doc["paths"].as_object().unwrap().len(), 3);
    }
}
