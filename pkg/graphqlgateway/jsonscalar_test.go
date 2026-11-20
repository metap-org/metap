package graphqlgateway

import (
	"encoding/json"
	"testing"

	"github.com/graphql-go/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An inline literal reaches the upstream with its JSON types: numbers stay numbers.
func TestJSONScalarLiteralKeepsNumbers(t *testing.T) {
	var got []byte
	schema, err := graphql.NewSchema(graphql.SchemaConfig{
		Query: graphql.NewObject(graphql.ObjectConfig{Name: "Query", Fields: graphql.Fields{
			"echo": &graphql.Field{
				Type: graphql.Boolean,
				Args: graphql.FieldConfigArgument{"value": &graphql.ArgumentConfig{Type: jsonScalar}},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					var err error
					got, err = json.Marshal(p.Args["value"])
					return true, err
				},
			},
		}}),
	})
	require.NoError(t, err)

	res := graphql.Do(graphql.Params{Schema: schema,
		RequestString: `{ echo(value: {days: 30, ratio: 0.5, name: "x", on: true, list: [1, "a"]}) }`})
	require.Empty(t, res.Errors)
	assert.JSONEq(t, `{"days":30,"ratio":0.5,"name":"x","on":true,"list":[1,"a"]}`, string(got))

	res = graphql.Do(graphql.Params{Schema: schema, RequestString: `{ echo(value: 30) }`})
	require.Empty(t, res.Errors)
	assert.Equal(t, "30", string(got))
}
