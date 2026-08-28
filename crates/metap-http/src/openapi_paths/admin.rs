//! `/admin/users*`, `/admin/policies*` — platform-admin user/role and policy management. See
//! `super`'s doc comment.

use serde_json::{json, Map, Value};

use super::insert;

pub(crate) fn admin_paths(paths: &mut Map<String, Value>) {
    insert(
        paths,
        "/admin/users",
        json!({
            "get": {
                "summary": "List every user + their role assignments in the caller's tenant",
                "responses": { "200": { "description": "OK" } },
            },
            "post": {
                "summary": "Provision a new local-login user, optionally assigning roles",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": {
                        "email": { "type": "string" },
                        "password": { "type": "string" },
                        "roles": { "type": "array", "items": { "type": "string" } },
                    },
                    "required": ["email", "password"],
                } } } },
                "responses": { "201": { "description": "Created" }, "409": { "description": "Email already taken" } },
            },
        }),
    );
    insert(
        paths,
        "/admin/users/{userId}/roles",
        json!({
            "post": {
                "summary": "Assign a role to a user",
                "parameters": [{ "name": "userId", "in": "path", "required": true, "schema": { "type": "string" } }],
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "role": { "type": "string" } }, "required": ["role"],
                } } } },
                "responses": { "201": { "description": "Created" } },
            },
        }),
    );
    insert(
        paths,
        "/admin/users/{userId}/roles/{role}",
        json!({
            "delete": {
                "summary": "Revoke a role from a user",
                "parameters": [
                    { "name": "userId", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "role", "in": "path", "required": true, "schema": { "type": "string" } },
                ],
                "responses": { "204": { "description": "No content" } },
            },
        }),
    );
    insert(
        paths,
        "/admin/users/{userId}/context/invalidate",
        json!({
            "post": {
                "summary": "Invalidate this user's cached `AUTH_CONTEXT_ENTITY` attributes",
                "parameters": [{ "name": "userId", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "204": { "description": "No content" } },
            },
        }),
    );
    insert(
        paths,
        "/admin/policies",
        json!({
            "get": {
                "summary": "List policies for the caller's tenant, optionally filtered by entity",
                "parameters": [{ "name": "entity", "in": "query", "required": false, "schema": { "type": "string" } }],
                "responses": { "200": { "description": "OK" } },
            },
            "post": {
                "summary": "Create a policy",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": {
                        "entity": { "type": "string" },
                        "action": { "type": "string" },
                        "roles": { "type": "array", "items": { "type": "string" } },
                        "condition": {},
                        "field": { "type": "string" },
                        "subject": { "type": "string", "enum": ["context", "record"] },
                        "effect": { "type": "string", "enum": ["allow", "deny"] },
                    },
                    "required": ["entity", "action"],
                } } } },
                "responses": { "201": { "description": "Created" } },
            },
        }),
    );
    insert(
        paths,
        "/admin/policies/seed-defaults",
        json!({
            "post": {
                "summary": "Bulk-create one context-subject, no-condition RBAC policy per action for `roles` on `entity`",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": {
                        "entity": { "type": "string" },
                        "roles": { "type": "array", "items": { "type": "string" } },
                        "actions": { "type": "array", "items": { "type": "string" } },
                    },
                    "required": ["entity", "roles"],
                } } } },
                "responses": { "201": { "description": "Created" } },
            },
        }),
    );
    insert(
        paths,
        "/admin/policies/explain",
        json!({
            "post": {
                "summary": "Explain why a given entity/action/field/record would be allowed or denied for the caller",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": {
                        "entity": { "type": "string" },
                        "action": { "type": "string" },
                        "field": { "type": "string" },
                        "record": { "type": "object" },
                    },
                    "required": ["entity", "action"],
                } } } },
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );
    insert(
        paths,
        "/admin/policies/{id}",
        json!({
            "delete": {
                "summary": "Delete a policy",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "204": { "description": "No content" } },
            },
        }),
    );
}
