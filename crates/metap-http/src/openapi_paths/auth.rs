//! `/auth/*` — login, session info, provider discovery, OIDC redirect/callback. See `super`'s
//! doc comment.

use serde_json::{json, Map, Value};

use super::insert;

pub(crate) fn auth_paths(paths: &mut Map<String, Value>) {
    insert(
        paths,
        "/auth/login",
        json!({
            "post": {
                "summary": "Local email/password login — mints a JWT",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": {
                        "email": { "type": "string" },
                        "password": { "type": "string" },
                        "tenantId": { "type": "string" },
                    },
                    "required": ["email", "password"],
                } } } },
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object",
                        "properties": { "data": { "type": "object", "properties": { "token": { "type": "string" } } } },
                    } } } },
                    "401": { "description": "Invalid credentials" },
                },
            },
        }),
    );
    insert(
        paths,
        "/auth/me",
        json!({
            "get": {
                "summary": "Identity + roles for the caller's own token",
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "object", "properties": {
                        "userId": { "type": "string" },
                        "tenantId": { "type": "string" },
                        "roles": { "type": "array", "items": { "type": "string" } },
                    } } },
                } } } } },
            },
        }),
    );
    insert(
        paths,
        "/auth/providers",
        json!({
            "get": {
                "summary": "Which login providers (local/basic/oidc) a tenant has enabled",
                "parameters": [{ "name": "tenantId", "in": "query", "required": true, "schema": { "type": "string" } }],
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "data": { "type": "object", "properties": {
                        "providers": { "type": "array", "items": { "type": "string" } },
                    } } },
                } } } } },
            },
        }),
    );
    insert(
        paths,
        "/auth/oidc/{tenant_id}/login",
        json!({
            "get": {
                "summary": "Redirects the browser to the tenant's configured IdP",
                "parameters": [{ "name": "tenant_id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "302": { "description": "Redirect to IdP" }, "404": { "description": "OIDC not configured for this tenant" } },
            },
        }),
    );
    insert(
        paths,
        "/auth/oidc/{tenant_id}/callback",
        json!({
            "get": {
                "summary": "OIDC callback — verifies the IdP response, JIT-provisions the user, mints a JWT",
                "parameters": [
                    { "name": "tenant_id", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "code", "in": "query", "required": true, "schema": { "type": "string" } },
                    { "name": "state", "in": "query", "required": true, "schema": { "type": "string" } },
                ],
                "responses": {
                    "302": { "description": "Redirect back to the tenant's frontend with `#token=...`" },
                    "400": { "description": "Invalid/expired OIDC flow state" },
                    "401": { "description": "OIDC verification failed" },
                },
            },
        }),
    );
}
