// Query depth/complexity limits — the same two guards the Rust gateway applied through
// async-graphql's limit_depth/limit_complexity (defaults 10 / 1000, overridable with
// GRAPHQL_MAX_DEPTH / GRAPHQL_MAX_COMPLEXITY). graphql-go has neither built in, so the check
// walks the parsed document itself before execution.
//
// Every resolved field here can cost an upstream gRPC or REST call (a reference field is a Get on
// another service), so these bound how much upstream work one authenticated operation can
// trigger: depth bounds nesting, complexity bounds the total number of field selections —
// aliases and fragment spreads included, which is what makes "the same cheap field 500 times
// under 500 aliases" expensive rather than free.
package graphqlgateway

import (
	"fmt"

	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/parser"
	"github.com/graphql-go/graphql/language/source"
)

type Limits struct {
	MaxDepth      int
	MaxComplexity int
}

func DefaultLimits() Limits { return Limits{MaxDepth: 10, MaxComplexity: 1000} }

// checkLimits returns a non-nil error when the operation(s) in query exceed l. A query that
// doesn't parse returns nil — graphql.Do then reports the real syntax error.
func checkLimits(query, operationName string, l Limits) error {
	doc, err := parser.Parse(parser.ParseParams{Source: source.NewSource(&source.Source{Body: []byte(query)})})
	if err != nil {
		return nil
	}
	fragments := map[string]*ast.FragmentDefinition{}
	var operations []*ast.OperationDefinition
	for _, def := range doc.Definitions {
		switch d := def.(type) {
		case *ast.FragmentDefinition:
			if d.Name != nil {
				fragments[d.Name.Value] = d
			}
		case *ast.OperationDefinition:
			if operationName == "" || (d.Name != nil && d.Name.Value == operationName) {
				operations = append(operations, d)
			}
		}
	}
	for _, op := range operations {
		m := &measure{fragments: fragments, active: map[string]bool{}}
		m.walk(op.SelectionSet, 0)
		if m.depth > l.MaxDepth {
			return fmt.Errorf("query is nested too deeply: depth %d exceeds the limit of %d", m.depth, l.MaxDepth)
		}
		if m.complexity > l.MaxComplexity {
			return fmt.Errorf("query is too complex: %d field selections exceed the limit of %d", m.complexity, l.MaxComplexity)
		}
	}
	return nil
}

type measure struct {
	fragments  map[string]*ast.FragmentDefinition
	active     map[string]bool // fragments currently being expanded (a cycle is a validation error; don't loop on it)
	depth      int
	complexity int
}

func (m *measure) walk(set *ast.SelectionSet, depth int) {
	if set == nil {
		return
	}
	for _, sel := range set.Selections {
		switch s := sel.(type) {
		case *ast.Field:
			m.complexity++
			if depth+1 > m.depth {
				m.depth = depth + 1
			}
			m.walk(s.SelectionSet, depth+1)
		case *ast.InlineFragment:
			m.walk(s.SelectionSet, depth)
		case *ast.FragmentSpread:
			if s.Name == nil {
				continue
			}
			name := s.Name.Value
			frag, ok := m.fragments[name]
			if !ok || m.active[name] {
				continue
			}
			m.active[name] = true
			m.walk(frag.SelectionSet, depth)
			delete(m.active, name)
		}
	}
}
