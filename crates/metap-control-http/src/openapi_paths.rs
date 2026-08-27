//! Hand-written OpenAPI path fragments for this crate's `/platform/tenants*` surface — same
//! reasoning and style as `metap_http::openapi_paths`/`metap_lowcode_http::openapi_paths`.
//! `apps/crm-server/src/main.rs` merges this into `AppState::extra_openapi_paths`.

use serde_json::{json, Map, Value};

fn insert(paths: &mut Map<String, Value>, path: &str, item: Value) {
    paths.insert(path.to_string(), item);
}

fn strategy_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "type": { "type": "string", "enum": ["schema", "dedicated_db"] },
            "schemaName": { "type": "string" },
            "dsnSecretRef": { "type": "string" },
        },
    })
}

fn tenant_summary_schema() -> Value {
    json!({
        "type": "object",
        "properties": {
            "id": { "type": "string" },
            "tier": { "type": "string" },
            "status": { "type": "string", "enum": ["active", "suspended", "deleted"] },
            "strategy": strategy_schema(),
            "createdAt": { "type": "string", "format": "date-time" },
            "trialExpiresAt": { "type": ["string", "null"], "format": "date-time" },
        },
    })
}

pub fn openapi_paths() -> Map<String, Value> {
    let mut paths = Map::new();

    insert(
        &mut paths,
        "/platform/tenants",
        json!({
            "get": {
                "summary": "List every tenant (platform-admin only)",
                "responses": { "200": { "description": "OK", "content": { "application/json": { "schema": {
                    "type": "object", "properties": { "data": { "type": "array", "items": tenant_summary_schema() } },
                } } } } },
            },
            "post": {
                "summary": "Provision a new tenant (schema or dedicated_db strategy) and its first admin user",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": {
                        "tenantId": { "type": "string" },
                        "strategy": { "type": "string", "enum": ["schema", "dedicated_db"] },
                        "adminEmail": { "type": "string" },
                        "adminPassword": { "type": "string" },
                        "dsnSecretRef": { "type": "string", "description": "Required when strategy is \"dedicated_db\"" },
                        "dedicatedDatabaseUrl": { "type": "string", "description": "Required when strategy is \"dedicated_db\"" },
                    },
                    "required": ["tenantId", "strategy", "adminEmail", "adminPassword"],
                } } } },
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": { "type": "object", "properties": {
                            "tenantId": { "type": "string" }, "adminUserId": { "type": "string" },
                        } } },
                    } } } },
                    "400": { "description": "Validation failed (unknown strategy, or missing dedicated_db fields)" },
                    "409": { "description": "A tenant with this id already exists" },
                },
            },
        }),
    );
    insert(
        &mut paths,
        "/platform/tenants/{id}",
        json!({
            "get": {
                "summary": "Get one tenant's routing info",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": { "type": "object", "properties": {
                            "id": { "type": "string" },
                            "status": { "type": "string", "enum": ["active", "suspended", "deleted"] },
                            "strategy": strategy_schema(),
                        } } },
                    } } } },
                    "404": { "description": "Not found" },
                },
            },
            "delete": {
                "summary": "Deprovision a tenant permanently — refuses future routing, does not touch or drop its data",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "responses": { "204": { "description": "No content" }, "404": { "description": "Not found" } },
            },
        }),
    );
    insert(
        &mut paths,
        "/platform/tenants/{id}/status",
        json!({
            "patch": {
                "summary": "Suspend or resume a tenant",
                "parameters": [{ "name": "id", "in": "path", "required": true, "schema": { "type": "string" } }],
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": { "status": { "type": "string", "enum": ["active", "suspended"] } },
                    "required": ["status"],
                } } } },
                "responses": { "200": { "description": "OK" }, "400": { "description": "Invalid status" }, "404": { "description": "Not found" } },
            },
        }),
    );
    insert(
        &mut paths,
        "/platform/reconciler/wave-rollout",
        json!({
            "post": {
                "summary": "Canary → wave rollout of a published low-code entity pack across a list of Schema-strategy tenants (platform-admin only)",
                "requestBody": { "content": { "application/json": { "schema": {
                    "type": "object",
                    "properties": {
                        "entityName": { "type": "string" },
                        "packVersion": { "type": "integer" },
                        "tenantIds": { "type": "array", "items": { "type": "string" } },
                        "wave": { "type": "integer", "description": "0 = canary, 1 = 5%, 2 = 25%, 3+ = 100%" },
                        "maxErrorRatePercent": { "type": "integer", "description": "Halts (does not advance) if the previous wave's blocked rate exceeds this" },
                    },
                    "required": ["entityName", "packVersion", "tenantIds", "wave", "maxErrorRatePercent"],
                } } } },
                "responses": {
                    "200": { "description": "OK", "content": { "application/json": { "schema": {
                        "type": "object", "properties": { "data": { "type": "object", "properties": {
                            "decision": { "type": "string", "enum": ["advanced", "halted"] },
                            "tenantsInWave": { "type": "integer" },
                            "errorRatePercent": { "type": "integer" },
                        } } },
                    } } } },
                },
            },
        }),
    );

    paths
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn covers_every_platform_tenant_route() {
        let paths = openapi_paths();
        for expected in [
            "/platform/tenants",
            "/platform/tenants/{id}",
            "/platform/tenants/{id}/status",
            "/platform/reconciler/wave-rollout",
        ] {
            assert!(paths.contains_key(expected), "missing path: {expected}");
        }
    }
}
