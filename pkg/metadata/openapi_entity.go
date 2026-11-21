package metadata

import "github.com/metap-org/metap/pkg/kit/standard"

// The generic /api/{entity}* routes (roadmap 116). The entity is a path parameter, never a
// per-entity path: which fields a record has is the entity's own metadata (EntitySummary), so this
// documents the contract every entity shares — parameters, envelopes and errors.

func queryParam(name, description string, schema map[string]any) map[string]any {
	return map[string]any{"name": name, "in": "query", "required": false, "description": description, "schema": schema}
}

func pathParam(name, description string) map[string]any {
	return map[string]any{"name": name, "in": "path", "required": true, "description": description, "schema": map[string]any{"type": "string"}}
}

var (
	strSchema  = map[string]any{"type": "string"}
	intSchema  = map[string]any{"type": "integer"}
	boolSchema = map[string]any{"type": "boolean"}
)

func jsonBody(schema map[string]any) map[string]any {
	return map[string]any{"required": true, "content": map[string]any{standard.MediaTypeJSON: map[string]any{"schema": schema}}}
}

func jsonResponse(description string, schema map[string]any) map[string]any {
	return map[string]any{"description": description, "content": map[string]any{standard.MediaTypeJSON: map[string]any{"schema": schema}}}
}

func errorResponses() map[string]any {
	errRef := jsonResponse("Failure: {\"error\":{\"code\",\"message\",\"fieldErrors\",\"details\"}}", map[string]any{"$ref": "#/components/schemas/Error"})
	return map[string]any{"400": errRef, "401": errRef, "403": errRef, "404": errRef, "409": errRef}
}

func withErrors(responses map[string]any) map[string]any {
	for k, v := range errorResponses() {
		if _, ok := responses[k]; !ok {
			responses[k] = v
		}
	}
	return responses
}

func recordEnvelope() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"data": map[string]any{"$ref": "#/components/schemas/Record"}}}
}

func entityRoutePaths() map[string]any {
	entity := pathParam("entity", "Entity name, e.g. waf.zones (see /metadata/entities).")
	id := pathParam("id", "Record id.")
	recordData := map[string]any{"type": "object", "additionalProperties": true, "description": "Field values; the shape is the entity's metadata."}

	listParams := []any{
		entity,
		queryParam("limit", "Page size, 1-200 (default 30; a list view may cap it lower).", intSchema),
		queryParam("sort", "One sortable field, `-` prefix for descending (`-createdAt`). Default: the list view's, else `-createdAt`.", strSchema),
		queryParam("cursor", "Keyset cursor from page.nextCursor; cannot be combined with offset.", strSchema),
		queryParam("offset", "Rows to skip for numbered pages (max 100000).", intSchema),
		queryParam("withTotal", "true: also count every matching row into page.total.", boolSchema),
		queryParam("search", "Free text matched against the entity's searchable string/enum fields.", strSchema),
		queryParam("listView", "List view name; its filters list the fields `field=value` may use.", strSchema),
		queryParam("jql", "Boolean query: AND/OR/NOT with parentheses, =, !=, >, >=, <, <=, ~ (contains), IN, IS EMPTY, ORDER BY. Use this for anything beyond `field=value`.", strSchema),
		queryParam("ids", "Comma-separated record ids (at most 200): those that exist and are readable, in one call. Other list parameters are ignored.", strSchema),
		queryParam("fields", "Comma-separated field names: keep only those in each record's data. Unknown name: 400. Default: every readable field.", strSchema),
		queryParam("expand", "Comma-separated reference fields to resolve into record.expanded (across services when the API gateway serves the call).", strSchema),
		queryParam("{field}", "Any field named by the list view's filters, `?status=open`; an empty value means \"unset\".", strSchema),
	}

	return map[string]any{
		"/api/{entity}": map[string]any{
			"get": map[string]any{
				"summary": "List records of an entity", "parameters": listParams,
				"responses": withErrors(map[string]any{"200": jsonResponse("OK", map[string]any{
					"type": "object",
					"properties": map[string]any{
						"data":         map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Record"}},
						"page":         map[string]any{"$ref": "#/components/schemas/Page"},
						"capabilities": map[string]any{"type": "object", "description": "What the caller may do with the entity as a whole."},
					},
				})}),
			},
			"post": map[string]any{
				"summary": "Create a record", "parameters": []any{entity},
				"requestBody": jsonBody(map[string]any{"type": "object", "properties": map[string]any{"data": recordData, "reason": strSchema}, "required": []string{"data"}}),
				"responses":   withErrors(map[string]any{"201": jsonResponse("Created", recordEnvelope())}),
			},
		},
		"/api/{entity}/{id}": map[string]any{
			"get": map[string]any{
				"summary":    "Get a record",
				"parameters": []any{entity, id, queryParam("fields", "As on the list.", strSchema), queryParam("expand", "As on the list.", strSchema)},
				"responses":  withErrors(map[string]any{"200": jsonResponse("OK (the record plus its capabilities)", recordEnvelope())}),
			},
			"patch": map[string]any{
				"summary": "Update a record (optimistic lock on version)", "parameters": []any{entity, id},
				"requestBody": jsonBody(map[string]any{"type": "object", "properties": map[string]any{"version": intSchema, "data": recordData, "reason": strSchema}, "required": []string{"version", "data"}}),
				"responses":   withErrors(map[string]any{"200": jsonResponse("OK", recordEnvelope())}),
			},
			"delete": map[string]any{
				"summary": "Delete a record (optimistic lock on version)", "parameters": []any{entity, id},
				"requestBody": jsonBody(map[string]any{"type": "object", "properties": map[string]any{"version": intSchema, "reason": strSchema}, "required": []string{"version"}}),
				"responses":   withErrors(map[string]any{"200": jsonResponse("OK", recordEnvelope())}),
			},
		},
		"/api/{entity}/aggregate": map[string]any{
			"post": map[string]any{
				"summary": "Aggregate records (counts, sums, groups, time buckets)", "parameters": []any{entity},
				"requestBody": jsonBody(map[string]any{"type": "object", "properties": map[string]any{
					"metrics": map[string]any{"type": "array", "items": strSchema}, "groupBy": strSchema, "bucket": strSchema, "timeField": strSchema,
					"filters": map[string]any{"type": "object", "additionalProperties": strSchema}, "since": strSchema, "until": strSchema, "limit": intSchema,
				}}),
				"responses": withErrors(map[string]any{"200": jsonResponse("OK", map[string]any{"type": "object", "properties": map[string]any{"data": map[string]any{}}})}),
			},
		},
		"/api/{entity}/{id}/transitions/{action}": map[string]any{
			"post": map[string]any{
				"summary": "Move a record through a workflow transition", "parameters": []any{entity, id, pathParam("action", "Transition action name.")},
				"requestBody": jsonBody(map[string]any{"type": "object", "properties": map[string]any{"version": intSchema, "data": recordData, "reason": strSchema}, "required": []string{"version"}}),
				"responses":   withErrors(map[string]any{"200": jsonResponse("OK", recordEnvelope())}),
			},
		},
		"/api/{entity}/{id}/actions/{action}": map[string]any{
			"post": map[string]any{
				"summary": "Run a declared record-scope action", "parameters": []any{entity, id, pathParam("action", "Action name declared on the entity.")},
				"requestBody": jsonBody(map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "object", "additionalProperties": true}}}),
				"responses":   withErrors(map[string]any{"200": jsonResponse("OK", map[string]any{"type": "object", "properties": map[string]any{"data": map[string]any{}}})}),
			},
		},
		"/api/{entity}/actions/{action}": map[string]any{
			"post": map[string]any{
				"summary": "Run a declared entity-scope action", "parameters": []any{entity, pathParam("action", "Action name declared on the entity.")},
				"requestBody": jsonBody(map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "object", "additionalProperties": true}}}),
				"responses":   withErrors(map[string]any{"200": jsonResponse("OK", map[string]any{"type": "object", "properties": map[string]any{"data": map[string]any{}}})}),
			},
		},
	}
}

func entityCommonSchemas() map[string]any {
	return map[string]any{
		"Record": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": strSchema, "entity": strSchema, "code": map[string]any{"type": []string{"string", "null"}}, "status": map[string]any{"type": []string{"string", "null"}},
				"data": map[string]any{"type": "object", "additionalProperties": true}, "version": intSchema,
				"createdAt": map[string]any{"type": "string", "format": "date-time"}, "updatedAt": map[string]any{"type": "string", "format": "date-time"},
				"relatedDisplay": map[string]any{"type": "object", "additionalProperties": strSchema},
				"expanded":       map[string]any{"type": "object", "description": "Present with ?expand=: the referenced records by field name, null when gone or not readable."},
			},
			"required": []string{"id", "entity", "data", "version"},
		},
		"Page": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": intSchema, "nextCursor": map[string]any{"type": []string{"string", "null"}}, "offset": intSchema, "total": intSchema,
			},
		},
		"Error": map[string]any{
			"type": "object",
			"properties": map[string]any{"error": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code": strSchema, "message": strSchema, "details": map[string]any{},
					"fieldErrors": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": strSchema}},
				},
				"required": []string{"code", "message"},
			}},
		},
	}
}
