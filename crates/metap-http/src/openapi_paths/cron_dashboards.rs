//! `/admin/cron-jobs*`, `/dashboards/*` — scheduled job definitions and per-tenant dashboard
//! layout. See `super`'s doc comment.

use serde_json::{json, Map, Value};

use super::insert;

pub(crate) fn cron_dashboard_paths(paths: &mut Map<String, Value>) {
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
        paths,
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
        paths,
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
        paths,
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
        paths,
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
        paths,
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
}
