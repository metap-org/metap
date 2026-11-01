// Recursive-descent parser: OR lowest precedence, then AND, then NOT, then a comparison atom —
// turns a []jqlToken into a parsedJql.
package query

type jqlParser struct {
	tokens []jqlToken
	pos    int
}

func newJqlParser(tokens []jqlToken) *jqlParser { return &jqlParser{tokens: tokens} }

func (p *jqlParser) peek() jqlToken { return p.tokens[p.pos] }

func (p *jqlParser) advance() jqlToken {
	t := p.tokens[p.pos]
	if p.pos+1 < len(p.tokens) {
		p.pos++
	}
	return t
}

func (p *jqlParser) eat(kind jqlTokenKind) bool {
	if p.peek().kind == kind {
		p.advance()
		return true
	}
	return false
}

func (p *jqlParser) expect(kind jqlTokenKind, what string) error {
	if p.eat(kind) {
		return nil
	}
	_, err := jqlErr[struct{}]("Expected %s", what)
	return err
}

func (p *jqlParser) parseQuery() (*parsedJql, error) {
	var expr *jqlExpr
	if p.peek().kind != tokKwOrder && p.peek().kind != tokEof {
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		expr = e
	}

	var orderBy *jqlOrder
	if p.eat(tokKwOrder) {
		if err := p.expect(tokKwBy, "`BY` after `ORDER`"); err != nil {
			return nil, err
		}
		field, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		descending := false
		if p.eat(tokKwDesc) {
			descending = true
		} else {
			p.eat(tokKwAsc)
		}
		orderBy = &jqlOrder{field: field, descending: descending}
	}

	if p.peek().kind != tokEof {
		return jqlErr[*parsedJql]("Unexpected trailing input")
	}
	return &parsedJql{expr: expr, orderBy: orderBy}, nil
}

func (p *jqlParser) parseOr() (*jqlExpr, error) {
	first, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	parts := []jqlExpr{*first}
	for p.eat(tokKwOr) {
		next, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		parts = append(parts, *next)
	}
	if len(parts) == 1 {
		return &parts[0], nil
	}
	return &jqlExpr{kind: jqlExprOr, parts: parts}, nil
}

func (p *jqlParser) parseAnd() (*jqlExpr, error) {
	first, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	parts := []jqlExpr{*first}
	for p.eat(tokKwAnd) {
		next, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		parts = append(parts, *next)
	}
	if len(parts) == 1 {
		return &parts[0], nil
	}
	return &jqlExpr{kind: jqlExprAnd, parts: parts}, nil
}

func (p *jqlParser) parseNot() (*jqlExpr, error) {
	if p.eat(tokKwNot) {
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &jqlExpr{kind: jqlExprNot, inner: inner}, nil
	}
	return p.parsePrimary()
}

func (p *jqlParser) parsePrimary() (*jqlExpr, error) {
	if p.eat(tokLParen) {
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tokRParen, "`)`"); err != nil {
			return nil, err
		}
		return e, nil
	}
	return p.parseComparison()
}

func (p *jqlParser) expectIdent() (string, error) {
	t := p.advance()
	if t.kind == tokIdent {
		return t.text, nil
	}
	return jqlErr[string]("Expected a field name")
}

func (p *jqlParser) parseValue() (jqlValue, error) {
	t := p.advance()
	switch t.kind {
	case tokStr:
		return jqlStrValue(t.text), nil
	case tokNumber:
		return jqlStrValue(t.text), nil
	case tokKwTrue:
		return jqlBoolValue(true), nil
	case tokKwFalse:
		return jqlBoolValue(false), nil
	default:
		return jqlErr[jqlValue]("Expected a value (string, number, true, or false)")
	}
}

func (p *jqlParser) parseValueList() ([]jqlValue, error) {
	if err := p.expect(tokLParen, "`(` after `IN`"); err != nil {
		return nil, err
	}
	first, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	values := []jqlValue{first}
	for p.eat(tokComma) {
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	if err := p.expect(tokRParen, "`)` to close the value list"); err != nil {
		return nil, err
	}
	return values, nil
}

func (p *jqlParser) parseComparison() (*jqlExpr, error) {
	field, err := p.expectIdent()
	if err != nil {
		return nil, err
	}

	cmp := func(op jqlCompareOp) (*jqlExpr, error) {
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &jqlExpr{kind: jqlExprCompare, field: field, op: op, value: v}, nil
	}

	t := p.advance()
	switch t.kind {
	case tokEq:
		return cmp(jqlOpEq)
	case tokNe:
		return cmp(jqlOpNe)
	case tokGt:
		return cmp(jqlOpGt)
	case tokGte:
		return cmp(jqlOpGte)
	case tokLt:
		return cmp(jqlOpLt)
	case tokLte:
		return cmp(jqlOpLte)
	case tokTilde:
		return cmp(jqlOpContains)
	case tokNotTilde:
		return cmp(jqlOpNotContains)
	case tokKwIn:
		values, err := p.parseValueList()
		if err != nil {
			return nil, err
		}
		return &jqlExpr{kind: jqlExprIn, field: field, negate: false, values: values}, nil
	case tokKwNot:
		if err := p.expect(tokKwIn, "`IN` after `NOT`"); err != nil {
			return nil, err
		}
		values, err := p.parseValueList()
		if err != nil {
			return nil, err
		}
		return &jqlExpr{kind: jqlExprIn, field: field, negate: true, values: values}, nil
	case tokKwIs:
		negate := p.eat(tokKwNot)
		if err := p.expect(tokKwEmpty, "`EMPTY` after `IS`"); err != nil {
			return nil, err
		}
		return &jqlExpr{kind: jqlExprIsEmpty, field: field, negate: negate}, nil
	default:
		return jqlErr[*jqlExpr]("Expected an operator after field `%s`", field)
	}
}
