package metadata

// EntityFieldJSONSchema mirrors the Rust entity_field_json_schema, hand-written since this
// package has no reflection-derived schema step — the wire shape is already fixed by this
// file's own JSON tags.
func EntityFieldJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  map[string]any{"type": "string"},
			"label": map[string]any{"type": "string"},
			"kind": map[string]any{
				"type": "string",
				"enum": []string{"id", "string", "number", "boolean", "date", "datetime", "money", "enum", "reference", "json"},
			},
			"required":        map[string]any{"type": "boolean"},
			"indexed":         map[string]any{"type": "boolean"},
			"unique":          map[string]any{"type": "boolean"},
			"enumValues":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"refEntity":       map[string]any{"type": "string"},
			"refDisplayField": map[string]any{"type": "string"},
			"searchable":      map[string]any{"type": "boolean"},
			"searchMode":      map[string]any{"type": "string", "enum": []string{"substring", "fts"}},
			"sortable":        map[string]any{"type": "boolean"},
			"storage":         map[string]any{"type": "string", "enum": []string{"native", "column"}},
			"min":             map[string]any{"type": "number"},
			"max":             map[string]any{"type": "number"},
			"minLength":       map[string]any{"type": "number"},
			"maxLength":       map[string]any{"type": "number"},
			"computed":        computedSpecJSONSchema(),
		},
		"required": []string{"name", "label", "kind"},
	}
}

func computedSpecJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expression": map[string]any{"type": "string"},
			"dependsOn":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"expression", "dependsOn"},
	}
}

func EntityListViewJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":           map[string]any{"type": "string"},
			"label":          map[string]any{"type": "string"},
			"fields":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"filters":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"requiredFields": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"defaultSort":    map[string]any{"type": "string"},
			"maxLimit":       map[string]any{"type": "number"},
		},
		"required": []string{"name", "label", "fields", "filters", "maxLimit"},
	}
}

func workflowTransitionJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string"},
			"from":   map[string]any{"type": "string"},
			"to":     map[string]any{"type": "string"},
			"label":  map[string]any{"type": "string"},
			// PolicyCondition (permission package) is a recursive untagged shape — left
			// untyped rather than hand-modeling the recursion here, same reasoning as Json
			// below: this generator describes entity data shape, not permission's wire format.
			"guard":     map[string]any{},
			"validator": map[string]any{},
			"setFields": map[string]any{"type": "object"},
		},
		"required": []string{"action", "from", "to", "label"},
	}
}

func RelatedViewJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":        map[string]any{"type": "string"},
			"label":       map[string]any{"type": "string"},
			"entity":      map[string]any{"type": "string"},
			"filterField": map[string]any{"type": "string"},
			"fields":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"limit":       map[string]any{"type": "number"},
		},
		"required": []string{"name", "label", "entity", "filterField", "fields"},
	}
}

func FieldDisplayHintJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"field":      map[string]any{"type": "string"},
			"resolveVia": map[string]any{"type": "string"},
			"enumTones":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		},
		"required": []string{"field"},
	}
}

func EntityWorkflowJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"stateField":     map[string]any{"type": "string"},
			"initialState":   map[string]any{"type": "string"},
			"terminalStates": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"transitions":    map[string]any{"type": "array", "items": workflowTransitionJSONSchema()},
		},
		"required": []string{"stateField", "initialState", "terminalStates", "transitions"},
	}
}

func entitySummaryJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":              map[string]any{"type": "string"},
			"label":             map[string]any{"type": "string"},
			"fields":            map[string]any{"type": "array", "items": EntityFieldJSONSchema()},
			"listViews":         map[string]any{"type": "array", "items": EntityListViewJSONSchema()},
			"workflow":          EntityWorkflowJSONSchema(),
			"relatedViews":      map[string]any{"type": "array", "items": RelatedViewJSONSchema()},
			"fieldDisplayHints": map[string]any{"type": "array", "items": FieldDisplayHintJSONSchema()},
			"audit":             entityAuditConfigJSONSchema(),
			"version":           map[string]any{"type": "string"},
		},
		"required": []string{"name", "label", "fields", "listViews", "version"},
	}
}

func entityAuditConfigJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"enabled":        map[string]any{"type": "boolean"},
			"redactedFields": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"enabled"},
	}
}

// GenerateOpenAPIDocument is /metadata/*'s own static paths, plus the EntitySummary component
// schema every one of them can reference. No per-entity /api/{entity}* CRUD path — REST entity
// access is gone (GraphQL-only), matching the Rust version's own current state.
func GenerateOpenAPIDocument() map[string]any {
	paths := map[string]any{
		"/metadata/entities": map[string]any{
			"get": map[string]any{
				"summary": "List entity metadata",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "OK",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"data": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/EntitySummary"}},
									},
								},
							},
						},
					},
				},
			},
		},
		"/metadata/entities/{entity}": map[string]any{
			"get": map[string]any{
				"summary": "Get one entity's metadata",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "OK",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":       "object",
									"properties": map[string]any{"data": map[string]any{"$ref": "#/components/schemas/EntitySummary"}},
								},
							},
						},
					},
					"404": map[string]any{"description": "Not found"},
				},
			},
		},
		"/metadata/actions": map[string]any{
			"get": map[string]any{
				"summary": "List the fixed set of actions a policy can grant",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "OK",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":       "object",
									"properties": map[string]any{"data": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
								},
							},
						},
					},
				},
			},
		},
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "Metap API", "version": "1.0.0"},
		"paths":   paths,
		"components": map[string]any{
			"schemas": map[string]any{"EntitySummary": entitySummaryJSONSchema()},
		},
	}
}
