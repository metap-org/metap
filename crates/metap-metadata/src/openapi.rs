//! Mirrors `packages/core/src/core/metadata/openapi-generator.ts`. Same paths, same
//! field-kind → JSON Schema mapping, so `packages/platform-react`'s `generate:types`
//! (`openapi-typescript` against this document's JSON, unchanged — see
//! `docs/rust-core-viability.md`'s Schema & Codegen Strategy) needs no changes to keep
//! working against a Rust-served `/metadata/openapi.json`.
//!
//! The `components.schemas.EntitySummary` entry is hand-written here (mirroring
//! `entity-wire-schema.ts`'s `EntitySummarySchema` field-for-field) rather than derived
//! from a schema library, since this crate has no Zod-equivalent reflection step — the
//! wire shape is already fixed by `entity.rs`'s serde `rename_all = "camelCase"` structs,
//! this is just its JSON Schema description for `$ref`.

use serde_json::{json, Value};

use crate::entity::{EntityField, FieldKind};
use crate::registry::EntitySummary;

fn field_kind_json_schema(kind: FieldKind) -> Value {
    match kind {
        FieldKind::Id => json!({ "type": "string" }),
        FieldKind::String => json!({ "type": "string" }),
        FieldKind::Number => json!({ "type": "number" }),
        FieldKind::Boolean => json!({ "type": "boolean" }),
        FieldKind::Date => json!({ "type": "string", "format": "date" }),
        FieldKind::Datetime => json!({ "type": "string", "format": "date-time" }),
        FieldKind::Money => json!({ "type": "number" }),
        FieldKind::Enum => json!({ "type": "string" }),
        FieldKind::Reference => json!({ "type": "string" }),
        FieldKind::Json => json!({}),
    }
}

fn field_schema(field: &EntityField) -> Value {
    if matches!(field.kind, FieldKind::Enum) {
        return json!({
            "type": "string",
            "enum": field.enum_values.clone().unwrap_or_default(),
        });
    }
    field_kind_json_schema(field.kind)
}

fn entity_schema(entity: &EntitySummary) -> Value {
    let mut properties = serde_json::Map::new();
    for field in &entity.fields {
        properties.insert(field.name.clone(), field_schema(field));
    }
    let required: Vec<&str> = entity
        .fields
        .iter()
        .filter(|f| f.required.unwrap_or(false))
        .map(|f| f.name.as_str())
        .collect();
    json!({
        "type": "object",
        "properties": properties,
        "required": required,
    })
}

fn entity_field_json_schema() -> Value {
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
        },
        "required": ["name", "label", "kind"],
    })
}

fn entity_list_view_json_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "name": { "type": "string" },
            "label": { "type": "string" },
            "fields": { "type": "array", "items": { "type": "string" } },
            "filters": { "type": "array", "items": { "type": "string" } },
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
        },
        "required": ["action", "from", "to", "label"],
    })
}

fn entity_workflow_json_schema() -> Value {
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
            "version": { "type": "string" },
        },
        "required": ["name", "label", "fields", "listViews", "version"],
    })
}

pub fn generate_openapi_document(entities: &[EntitySummary]) -> Value {
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

    for entity in entities {
        let schema = entity_schema(entity);
        let list_path = format!("/api/{}", entity.name);
        let item_path = format!("/api/{}/{{id}}", entity.name);

        paths.insert(
            list_path,
            json!({
                "get": {
                    "summary": format!("List {}", entity.label),
                    "responses": { "200": { "description": "OK" } },
                },
                "post": {
                    "summary": format!("Create {}", entity.label),
                    "requestBody": {
                        "content": {
                            "application/json": { "schema": { "type": "object", "properties": { "data": schema } } },
                        },
                    },
                    "responses": { "201": { "description": "Created" } },
                },
            }),
        );

        paths.insert(
            item_path,
            json!({
                "patch": {
                    "summary": format!("Update {}", entity.label),
                    "requestBody": {
                        "content": {
                            "application/json": {
                                "schema": {
                                    "type": "object",
                                    "properties": { "version": { "type": "number" }, "data": schema },
                                },
                            },
                        },
                    },
                    "responses": { "200": { "description": "OK" } },
                },
            }),
        );

        if entity.workflow.is_some() {
            paths.insert(
                format!("/api/{}/{{id}}/transitions/{{action}}", entity.name),
                json!({
                    "post": {
                        "summary": format!("Transition {}", entity.label),
                        "requestBody": {
                            "content": {
                                "application/json": {
                                    "schema": { "type": "object", "properties": { "version": { "type": "number" } } },
                                },
                            },
                        },
                        "responses": { "200": { "description": "OK" } },
                    },
                }),
            );
        }
    }

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
    use crate::compiler;
    use crate::entity::{EntityDefinition, EntityField, FieldKind};

    #[test]
    fn generates_list_and_item_paths_per_entity() {
        let entity = EntityDefinition {
            name: "crm.customers".to_string(),
            label: "Customer".to_string(),
            table_name: "records".to_string(),
            fields: vec![EntityField {
                name: "name".to_string(),
                label: "Name".to_string(),
                kind: FieldKind::String,
                required: Some(true),
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: None,
            }],
            list_views: vec![],
            workflow: None,
        };
        let summary = EntitySummary {
            name: entity.name.clone(),
            label: entity.label.clone(),
            fields: entity.fields.clone(),
            list_views: entity.list_views.clone(),
            workflow: entity.workflow.clone(),
            version: compiler::hash(&entity).unwrap(),
        };
        let doc = generate_openapi_document(&[summary]);
        assert!(doc["paths"]["/api/crm.customers"]["post"].is_object());
        assert!(doc["paths"]["/api/crm.customers/{id}"]["patch"].is_object());
        assert!(doc["paths"]["/api/crm.customers/{id}/transitions/{action}"].is_null());
        assert_eq!(
            doc["paths"]["/api/crm.customers"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["data"]["properties"]["name"]["type"],
            "string"
        );
    }
}
