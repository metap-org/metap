// A minimal, hand-rolled parameter tracker: composable SQL fragments (WHERE/ORDER BY built
// separately, concatenated into one SELECT later) sharing one correctly-numbered $N placeholder
// sequence.
package query

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/metadata"
)

type bindKind int

const (
	bindText bindKind = iota
	bindBool
	bindUUID
)

type BindValue struct {
	kind bindKind
	text string
	b    bool
	u    uuid.UUID
}

func TextBind(s string) BindValue    { return BindValue{kind: bindText, text: s} }
func BoolBind(b bool) BindValue      { return BindValue{kind: bindBool, b: b} }
func UUIDBind(u uuid.UUID) BindValue { return BindValue{kind: bindUUID, u: u} }

// Arg returns the Go value pgx should bind for this parameter, in encounter order — the runtime
// counterpart to ParamBuilder's $N numbering. pgx matches positional args to $1, $2, ... in the
// order given, so a caller executing a PlannedListQuery/PlannedAggregateQuery just passes
// ArgsOf(params) as the query's variadic args.
func (v BindValue) Arg() any {
	switch v.kind {
	case bindText:
		return v.text
	case bindBool:
		return v.b
	case bindUUID:
		return v.u
	default:
		return nil
	}
}

// IsUUID reports whether this param was bound via UUIDBind — used by tests to confirm a
// comparison picked the native UUID binding rather than a text one.
func (v BindValue) IsUUID() bool { return v.kind == bindUUID }

// ArgsOf converts an ordered []BindValue into the []any pgx expects for Query/QueryRow/Exec.
func ArgsOf(params []BindValue) []any {
	args := make([]any, len(params))
	for i, p := range params {
		args[i] = p.Arg()
	}
	return args
}

type ParamBuilder struct {
	Params []BindValue
	// fields: the entity's declared fields, by name, so a condition that names a field resolves to
	// that field's typed column. Set by the planner via SetEntity; unset (a bare ConditionToSQL
	// call) means no field is known and a field condition resolves to NULL.
	fields map[string]metadata.EntityField
}

// SetEntity tells the builder which entity the fragments being built belong to.
func (p *ParamBuilder) SetEntity(entity metadata.EntityDefinition) {
	p.fields = make(map[string]metadata.EntityField, len(entity.Fields))
	for _, f := range entity.Fields {
		p.fields[f.Name] = f
	}
}

func (p *ParamBuilder) field(name string) (metadata.EntityField, bool) {
	f, ok := p.fields[name]
	return f, ok
}

func NewParamBuilder() *ParamBuilder { return &ParamBuilder{} }

// Push appends a value and returns its $N placeholder text.
func (p *ParamBuilder) Push(value BindValue) string {
	p.Params = append(p.Params, value)
	return fmt.Sprintf("$%d", len(p.Params))
}
