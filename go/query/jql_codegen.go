// AST -> SQL fragment, validated against the entity's own metadata: field names checked against
// entity.Fields, operators gated by FieldKind, every value bound as a parameter — nothing from
// the query text is ever interpolated into the SQL string.
package query

import (
	"strconv"
	"strings"

	"github.com/metap-org/metap-go/metadata"
)

type jqlFieldType int

const (
	jqlFieldText jqlFieldType = iota
	jqlFieldNumber
	jqlFieldMoney
	jqlFieldBoolean
	jqlFieldDate
	jqlFieldDatetime
	jqlFieldUUID
)

func jqlCastSuffix(t jqlFieldType) string {
	switch t {
	case jqlFieldNumber:
		return "::double precision"
	case jqlFieldMoney:
		return "::numeric"
	case jqlFieldBoolean:
		return "::boolean"
	case jqlFieldDate:
		return "::date"
	case jqlFieldDatetime:
		return "::timestamptz"
	case jqlFieldUUID:
		return "::uuid"
	default:
		return ""
	}
}

// resolveJqlField: field name -> (SQL expression yielding that field's value, its comparison
// type). createdAt/updatedAt are real columns on every table so they resolve first, same special
// case sortFieldExpression carries. Everything else must be a real entry in entity.Fields — an
// unknown name is the one thing this whole module treats as a hard error instead of the
// "silently ignore" convention PlanList's plain query-param filters use, since a JQL typo the
// caller can't see any other way deserves a visible error.
func resolveJqlField(fieldName string, entity metadata.EntityDefinition, params *ParamBuilder) (string, jqlFieldType, error) {
	switch fieldName {
	case "createdAt":
		return "created_at", jqlFieldDatetime, nil
	case "updatedAt":
		return "updated_at", jqlFieldDatetime, nil
	}
	var field *metadata.EntityField
	for i := range entity.Fields {
		if entity.Fields[i].Name == fieldName {
			field = &entity.Fields[i]
			break
		}
	}
	if field == nil {
		_, err := jqlErr[string]("Unknown field: %s", fieldName)
		return "", 0, err
	}
	var ftype jqlFieldType
	switch field.Kind {
	case metadata.KindString, metadata.KindEnum, metadata.KindJSON:
		ftype = jqlFieldText
	case metadata.KindNumber:
		ftype = jqlFieldNumber
	case metadata.KindMoney:
		ftype = jqlFieldMoney
	case metadata.KindBoolean:
		ftype = jqlFieldBoolean
	case metadata.KindDate:
		ftype = jqlFieldDate
	case metadata.KindDatetime:
		ftype = jqlFieldDatetime
	case metadata.KindReference, metadata.KindID:
		ftype = jqlFieldUUID
	}
	if metadata.FieldHasRealColumn(*field) {
		return `"` + fieldName + `"`, ftype, nil
	}
	ph := params.Push(TextBind(fieldName))
	return "jsonb_extract_path_text(data, " + ph + ")", ftype, nil
}

func validateJqlOp(op jqlCompareOp, ftype jqlFieldType, field string) error {
	ok := false
	switch op {
	case jqlOpEq, jqlOpNe:
		ok = true
	case jqlOpGt, jqlOpGte, jqlOpLt, jqlOpLte:
		ok = ftype == jqlFieldNumber || ftype == jqlFieldMoney || ftype == jqlFieldDate || ftype == jqlFieldDatetime
	case jqlOpContains, jqlOpNotContains:
		ok = ftype == jqlFieldText
	}
	if ok {
		return nil
	}
	_, err := jqlErr[struct{}]("Operator not supported on field `%s`", field)
	return err
}

func jqlValueToBindText(v jqlValue) string {
	if v.isBool {
		return strconv.FormatBool(v.b)
	}
	return v.str
}

// jqlIlikePattern: same escaping PlanList's substring-search branch uses for a searchable
// field's ILIKE pattern — kept in sync deliberately (a ~ value must not let %/_ act as SQL
// wildcards just because the caller typed them).
func jqlIlikePattern(value string) string {
	var b strings.Builder
	for _, c := range value {
		if c == '\\' || c == '%' || c == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return "%" + b.String() + "%"
}

func compileJqlExpr(expr *jqlExpr, entity metadata.EntityDefinition, params *ParamBuilder) (string, error) {
	switch expr.kind {
	case jqlExprAnd:
		parts := make([]string, len(expr.parts))
		for i := range expr.parts {
			s, err := compileJqlExpr(&expr.parts[i], entity, params)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "(" + joinSQL(parts, " AND ") + ")", nil
	case jqlExprOr:
		parts := make([]string, len(expr.parts))
		for i := range expr.parts {
			s, err := compileJqlExpr(&expr.parts[i], entity, params)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "(" + joinSQL(parts, " OR ") + ")", nil
	case jqlExprNot:
		s, err := compileJqlExpr(expr.inner, entity, params)
		if err != nil {
			return "", err
		}
		return "(NOT " + s + ")", nil
	case jqlExprIsEmpty:
		base, ftype, err := resolveJqlField(expr.field, entity, params)
		if err != nil {
			return "", err
		}
		lhs := "(" + base + ")" + jqlCastSuffix(ftype)
		if expr.negate {
			return "(" + lhs + " IS NOT NULL)", nil
		}
		return "(" + lhs + " IS NULL)", nil
	case jqlExprCompare:
		base, ftype, err := resolveJqlField(expr.field, entity, params)
		if err != nil {
			return "", err
		}
		if err := validateJqlOp(expr.op, ftype, expr.field); err != nil {
			return "", err
		}
		lhs := "(" + base + ")" + jqlCastSuffix(ftype)
		if expr.op == jqlOpContains || expr.op == jqlOpNotContains {
			if expr.value.isBool {
				return jqlErr[string]("`~`/`!~` need a string value for field `%s`", expr.field)
			}
			ph := params.Push(TextBind(jqlIlikePattern(expr.value.str)))
			if expr.op == jqlOpContains {
				return "(" + lhs + " ILIKE " + ph + ")", nil
			}
			return "(" + lhs + " NOT ILIKE " + ph + ")", nil
		}
		ph := params.Push(TextBind(jqlValueToBindText(expr.value)))
		cast := jqlCastSuffix(ftype)
		var sqlOp string
		switch expr.op {
		case jqlOpEq:
			sqlOp = "="
		case jqlOpNe:
			sqlOp = "!="
		case jqlOpGt:
			sqlOp = ">"
		case jqlOpGte:
			sqlOp = ">="
		case jqlOpLt:
			sqlOp = "<"
		default:
			sqlOp = "<="
		}
		return "(" + lhs + " " + sqlOp + " " + ph + cast + ")", nil
	case jqlExprIn:
		if len(expr.values) == 0 {
			return jqlErr[string]("`IN`/`NOT IN` needs at least one value for field `%s`", expr.field)
		}
		base, ftype, err := resolveJqlField(expr.field, entity, params)
		if err != nil {
			return "", err
		}
		lhs := "(" + base + ")" + jqlCastSuffix(ftype)
		cast := jqlCastSuffix(ftype)
		placeholders := make([]string, len(expr.values))
		for i, v := range expr.values {
			ph := params.Push(TextBind(jqlValueToBindText(v)))
			placeholders[i] = ph + cast
		}
		sqlOp := "IN"
		if expr.negate {
			sqlOp = "NOT IN"
		}
		return "(" + lhs + " " + sqlOp + " (" + joinSQL(placeholders, ", ") + "))", nil
	default:
		return "", nil
	}
}

func isJqlSortable(fieldName string, entity metadata.EntityDefinition) bool {
	if fieldName == "createdAt" || fieldName == "updatedAt" {
		return true
	}
	for _, f := range entity.Fields {
		if f.Name == fieldName && f.Sortable != nil && *f.Sortable {
			return true
		}
	}
	return false
}

// ParseAndCompileJQL parses and compiles a JQL string against one entity. Returns the compiled
// WHERE fragment (already fully parenthesized, ready to AND into PlanList's other conditions —
// "" with ok=false if the query was only an ORDER BY with no filter expression) plus an optional
// (field, descending) pair for its ORDER BY clause.
func ParseAndCompileJQL(jql string, entity metadata.EntityDefinition, params *ParamBuilder) (whereSQL string, hasWhere bool, orderField string, orderDesc bool, hasOrder bool, err error) {
	tokens, err := jqlTokenize(jql)
	if err != nil {
		return "", false, "", false, false, err
	}
	parser := newJqlParser(tokens)
	parsed, err := parser.parseQuery()
	if err != nil {
		return "", false, "", false, false, err
	}

	if parsed.expr != nil {
		sql, err := compileJqlExpr(parsed.expr, entity, params)
		if err != nil {
			return "", false, "", false, false, err
		}
		whereSQL, hasWhere = sql, true
	}

	if parsed.orderBy != nil {
		if !isJqlSortable(parsed.orderBy.field, entity) {
			_, err := jqlErr[struct{}]("Field `%s` is not sortable", parsed.orderBy.field)
			return "", false, "", false, false, err
		}
		orderField, orderDesc, hasOrder = parsed.orderBy.field, parsed.orderBy.descending, true
	}

	return whereSQL, hasWhere, orderField, orderDesc, hasOrder, nil
}
