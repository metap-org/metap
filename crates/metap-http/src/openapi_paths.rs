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

use serde_json::{json, Map, Value};

fn insert(paths: &mut Map<String, Value>, path: &str, item: Value) {
    paths.insert(path.to_string(), item);
}

pub fn static_paths() -> Map<String, Value> {
    let mut paths = Map::new();

    insert(
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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

    insert(
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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

    insert(
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
        "/admin/policies/{id}",
        json!({
            "delete": {
                "summary": "Delete a policy",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "204": { "description": "No content" } },
            },
        }),
    );

    let cron_job_body = json!({
        "type": "object",
        "properties": {
            "name": { "type": "string" },
            "triggerType": { "type": "string", "enum": ["schedule", "on_transition", "on_record_event"] },
            "triggerConfig": {},
            "cronExpr": { "type": "string" },
            "timezone": { "type": "string" },
            "targetType": { "type": "string", "enum": ["workflow_transition", "bulk_query_action", "webhook", "email"] },
            "targetConfig": {},
            "dispatchMode": { "type": "string", "enum": ["outbox", "direct"] },
            "maxAttempts": { "type": "integer" },
            "retryBackoffSeconds": { "type": "integer" },
            "enabled": { "type": "boolean" },
        },
    });
    insert(
        &mut paths,
        "/admin/cron-jobs",
        json!({
            "get": { "summary": "List cron jobs for the caller's tenant", "responses": { "200": { "description": "OK" } } },
            "post": {
                "summary": "Create a cron job",
                "requestBody": { "content": { "application/json": { "schema": cron_job_body } } },
                "responses": { "201": { "description": "Created" } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/cron-jobs/{id}",
        json!({
            "get": {
                "summary": "Get one cron job",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "200": { "description": "OK" }, "404": { "description": "Not found" } },
            },
            "patch": {
                "summary": "Update a cron job (partial)",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "200": { "description": "OK" }, "404": { "description": "Not found" } },
            },
            "delete": {
                "summary": "Delete a cron job",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "204": { "description": "No content" } },
            },
        }),
    );
    insert(
        &mut paths,
        "/admin/cron-jobs/{id}/runs",
        json!({
            "get": {
                "summary": "List recent runs for a cron job",
                "parameters": [
                    { "name": "id", "in": "path", "required": true, "schema": { "type": "string" } },
                    { "name": "limit", "in": "query", "required": false, "schema": { "type": "integer", "maximum": 200 } },
                ],
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );

    let dashboard_config_schema = json!({
        "type": "object",
        "properties": {
            "id": { "type": "string" },
            "ownerUserId": { "type": ["string", "null"] },
            "layout": {},
            "updatedAt": { "type": "string", "format": "date-time" },
        },
    });
    insert(
        &mut paths,
        "/dashboards/me",
        json!({
            "get": {
                "summary": "Get the caller's own dashboard layout (falls back to the tenant default)",
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "data": dashboard_config_schema },
                } } } } },
            },
            "put": {
                "summary": "Save the caller's own dashboard layout",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "layout": {} }, "required": ["layout"],
                } } } },
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );
    insert(
        &mut paths,
        "/dashboards/tenant-default",
        json!({
            "get": {
                "summary": "Get the tenant's default dashboard layout",
                "responses": { "200": { "description": "OK" } },
            },
            "put": {
                "summary": "Save the tenant's default dashboard layout (admin only)",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "layout": {} }, "required": ["layout"],
                } } } },
                "responses": { "200": { "description": "OK" } },
            },
        }),
    );

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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        &mut paths,
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
        ] {
            assert!(paths.contains_key(expected), "missing path: {expected}");
        }
    }
}
