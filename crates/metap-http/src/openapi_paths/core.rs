//! `/health`, `/preferences`, `/users` — see `super`'s doc comment.

use serde_json::{json, Map, Value};

use super::insert;

pub(crate) fn core_paths(paths: &mut Map<String, Value>) {
    insert(
        paths,
        "/health",
        json!({
            "get": {
                "summary": "Liveness/readiness check",
                "responses": {
                    "200": {
                        "description": "OK",
                        "content": { "application/json": { "schema": {
                            "type": "object",
                            "properties": {
                                "status": { "type": "string", "enum": ["ok", "degraded"] },
                                "checks": { "type": "object", "properties": { "database": { "type": "boolean" } } },
                            },
                        } } },
                    },
                },
            },
        }),
    );

    insert(
        paths,
        "/preferences",
        json!({
            "get": {
                "summary": "Get the caller's own preferences",
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "object", "properties": { "locale": { "type": "string" } } } },
                } } } } },
            },
            "put": {
                "summary": "Update the caller's own preferences",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "locale": { "type": "string", "enum": ["en", "vi"] } },
                    "required": ["locale"],
                } } } },
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );

    insert(
        paths,
        "/users",
        json!({
            "get": {
                "summary": "List every user in the caller's tenant (id + email only)",
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "array", "items": { "type": "object", "properties": {
                        "id": { "type": "string" }, "email": { "type": "string" },
                    } } } },
                } } } } },
            },
        }),
    );
}
