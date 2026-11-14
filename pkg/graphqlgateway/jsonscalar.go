// JSON scalar — every entity's "data" field and every custom mutation's free-form argument/
// return value goes through this, matching metap-graphql's own Json scalar: this gateway is a
// forwarding layer, not a typed schema generator, so arbitrary record/business-action payloads
// pass through as opaque JSON rather than being modeled field-by-field.
package graphqlgateway

import (
	"fmt"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"
)

var jsonScalar = graphql.NewScalar(graphql.ScalarConfig{
	Name:        "Json",
	Description: "An arbitrary JSON value (object, array, string, number, boolean or null).",
	Serialize: func(value interface{}) interface{} {
		return value
	},
	ParseValue: func(value interface{}) interface{} {
		return value
	},
	ParseLiteral: func(valueAST ast.Value) interface{} {
		return parseLiteral(valueAST)
	},
})

// parseLiteral handles a JSON value written inline in a GraphQL query string (as opposed to
// passed via $variables, which graphql-go already decodes through encoding/json before this
// scalar ever sees it).
func parseLiteral(valueAST ast.Value) interface{} {
	switch v := valueAST.(type) {
	case *ast.StringValue:
		return v.Value
	case *ast.IntValue:
		return v.Value
	case *ast.FloatValue:
		return v.Value
	case *ast.BooleanValue:
		return v.Value
	case *ast.EnumValue:
		// graphql-go has no dedicated null-literal AST node in this version — a bare `null` in
		// an inline literal parses as an EnumValue with this exact text.
		if v.Value == "null" {
			return nil
		}
		return v.Value
	case *ast.ListValue:
		out := make([]interface{}, len(v.Values))
		for i, item := range v.Values {
			out[i] = parseLiteral(item)
		}
		return out
	case *ast.ObjectValue:
		out := make(map[string]interface{}, len(v.Fields))
		for _, field := range v.Fields {
			out[field.Name.Value] = parseLiteral(field.Value)
		}
		return out
	default:
		return fmt.Sprintf("%v", valueAST)
	}
}
