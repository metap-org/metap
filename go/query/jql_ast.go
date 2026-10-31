// The parsed JQL AST — jqlParser builds it, jql codegen compiles it to SQL.
package query

type jqlCompareOp int

const (
	jqlOpEq jqlCompareOp = iota
	jqlOpNe
	jqlOpGt
	jqlOpGte
	jqlOpLt
	jqlOpLte
	jqlOpContains
	jqlOpNotContains
)

type jqlValue struct {
	isBool bool
	str    string
	b      bool
}

func jqlStrValue(s string) jqlValue { return jqlValue{str: s} }
func jqlBoolValue(b bool) jqlValue  { return jqlValue{isBool: true, b: b} }

// jqlExpr is a tagged union over the 5 JQL expression kinds — exactly one of the typed fields is
// set per node, mirroring the Rust enum.
type jqlExpr struct {
	kind jqlExprKind

	// Compare
	field string
	op    jqlCompareOp
	value jqlValue

	// In
	negate bool
	values []jqlValue

	// And/Or
	parts []jqlExpr

	// Not
	inner *jqlExpr
}

type jqlExprKind int

const (
	jqlExprCompare jqlExprKind = iota
	jqlExprIn
	jqlExprIsEmpty
	jqlExprAnd
	jqlExprOr
	jqlExprNot
)

type jqlOrder struct {
	field      string
	descending bool
}

type parsedJql struct {
	expr    *jqlExpr
	orderBy *jqlOrder
}
