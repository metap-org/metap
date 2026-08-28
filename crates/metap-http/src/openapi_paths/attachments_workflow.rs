//! `/api/{entity}/{record_id}/attachments*`, `/api/{entity}/{record_id}/workflow-events` — the
//! two generic per-record capabilities every entity gets for free. See `super`'s doc comment.

use serde_json::{json, Map, Value};

use super::insert;

pub(crate) fn attachment_workflow_paths(paths: &mut Map<String, Value>) {
    let attachment_schema = json!({
        "type": "object",
        "properties": {
            "id": { "type": "string" },
            "entityName": { "type": "string" },
            "recordId": { "type": "string" },
            "filename": { "type": "string" },
            "key": { "type": "string" },
            "size": { "type": "integer" },
            "contentType": { "type": ["string", "null"] },
            "createdAt": { "type": "string", "format": "date-time" },
        },
    });
    insert(
        paths,
        "/api/{entity}/{record_id}/attachments",
        json!({
            "get": {
                "summary": "List attachments on a record",
                "parameters": [
                    { "name": "entity", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "record_id", "in": "path", "required": true, "schema": { "type": "string" } },
                ],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "data": { "type": "array", "items": attachment_schema.clone() } },
                } } } } },
            },
            "post": {
                "summary": "Upload an attachment (multipart/form-data, one file field)",
                "parameters": [
                    { "name": "entity", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "record_id", "in": "path", "required": true, "schema": { "type": "string" } },
                ],
                "requestBody": { "content": { "multipart/form-data": { "schema": { "type": "object" } } } },
                "responses": {
                    "201": { "description": "Created", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": attachment_schema.clone() },
                    } } } },
                    "503": { "description": "Object storage not configured" },
                },
            },
        }),
    );
    insert(
        paths,
        "/api/{entity}/{record_id}/attachments/{attachment_id}/download",
        json!({
            "get": {
                "summary": "Download an attachment's bytes",
                "parameters": [
                    { "name": "entity", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "record_id", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "attachment_id", "in": "path", "required": true, "schema": { "type": "string" } },
                ],
                "responses": {
                    "200": { "description": "OK", "content": { "application/octet-stream": {} } },
                    "404": { "description": "Not found" },
                    "503": { "description": "Object storage not configured" },
                },
            },
        }),
    );
    insert(
        paths,
        "/api/{entity}/{record_id}/attachments/{attachment_id}",
        json!({
            "delete": {
                "summary": "Delete an attachment",
                "parameters": [
                    { "name": "entity", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "record_id", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "attachment_id", "in": "path", "required": true, "schema": { "type": "string" } },
                ],
                "responses": { "204": { "description": "No content" }, "503": { "description": "Object storage not configured" } },
            },
        }),
    );

    insert(
        paths,
        "/api/{entity}/{record_id}/workflow-events",
        json!({
            "get": {
                "summary": "Transition history for one record",
                "parameters": [
                    { "name": "entity", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "record_id", "in": "path", "required": true, "schema": { "type": "string" } },
                ],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "array", "items": { "type": "object", "properties": {
                        "fromState": { "type": ["string", "null"] },
                        "toState": { "type": "string" },
                        "action": { "type": "string" },
                        "occurredAt": { "type": "string", "format": "date-time" },
                    } } } },
                } } } } } },
        }),
    );
}
